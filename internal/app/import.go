package app

import (
	"errors"
	"fmt"

	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/importer"
	"github.com/githubflyideas/unagikuma/internal/store"
)

// KumaOptions say where an Uptime Kuma import goes and who runs it.
type KumaOptions struct {
	// Area is the $SCOPE section for the monitors ("" = global).
	Area string
	// Role is given to Uptime Kuma's accounts (default operator). With an
	// Area, operators and viewers are limited to it.
	Role string
	// Manager is an existing account put in charge of Area: a viewer
	// becomes an operator of it, a limited operator gains it.
	Manager string
	// Source is the tool the data comes from: "" (Uptime Kuma) or
	// "nagios" (object configuration).
	Source string
}

// convert turns the data into records according to its source.
func (a *App) convert(data []byte, o KumaOptions) (*importer.Result, error) {
	opts := importer.Options{Area: o.Area, File: "upload", SecretExists: a.secrets.Has,
		UserExists: func(name string) bool { _, err := a.store.UserID(name); return err == nil }}
	switch o.Source {
	case "nagios":
		return importer.Nagios(data, a.Config().Config, opts)
	case "gatus":
		return importer.Gatus(data, a.Config().Config, opts)
	case "checkmate":
		return importer.Checkmate(data, a.Config().Config, opts)
	case "", "kuma":
		return importer.Kuma(data, a.Config().Config, opts)
	default:
		return nil, fmt.Errorf("unknown import source")
	}
}

// UserNote is what happened to one account during an import.
type UserNote struct {
	Username string `json:"username"`
	Result   string `json:"result"` // created | exists | inactive | bad_hash | manager
}

// PreviewKuma converts an Uptime Kuma database or backup without changing
// anything.
func (a *App) PreviewKuma(data []byte, o KumaOptions) (*importer.Result, error) {
	return a.convert(data, o)
}

// ImportKuma adds an Uptime Kuma database's or backup's monitors, channels,
// policies and accounts. Credentials are stored as secrets first (the
// records refer to them); they are removed again if the records are
// refused.
func (a *App) ImportKuma(data []byte, author string, o KumaOptions) (ConfigState, *importer.Result, []UserNote, error) {
	if o.Role == "" {
		o.Role = store.RoleOperator
	}
	if !store.ValidRole(o.Role) {
		return ConfigState{}, nil, nil, fmt.Errorf("unknown role %q", o.Role)
	}
	var mgrID int64
	if o.Manager != "" {
		id, err := a.store.UserID(o.Manager)
		if err != nil {
			return ConfigState{}, nil, nil, fmt.Errorf("user %s not found", o.Manager)
		}
		mgrID = id
	}
	res, err := a.convert(data, o)
	if err != nil {
		return ConfigState{}, nil, nil, err
	}
	if err := res.ActivationError(); err != nil {
		return ConfigState{}, nil, nil, err
	}
	if (o.Source == "gatus" || o.Source == "checkmate") && o.Manager != "" {
		return ConfigState{}, nil, nil, fmt.Errorf("account grants are not supported for this import source")
	}
	from := res.Source
	if o.Source == "nagios" {
		from = "Nagios"
	}
	var stored []string
	undo := func() {
		for _, n := range stored {
			_ = a.secrets.Delete(n)
		}
	}
	for name, value := range res.Secrets {
		if a.secrets.Has(name) {
			undo()
			return ConfigState{}, nil, nil, fmt.Errorf("secret %s already exists; was this backup imported before?", name)
		}
		if err := a.secrets.Put(name, value, nil); err != nil {
			undo()
			return ConfigState{}, nil, nil, err
		}
		stored = append(stored, name)
	}
	st := a.Config()
	if len(res.Records) > 0 {
		st, err = a.EditConfig(author, fmt.Sprintf("import from %s: %d monitors, %d channels", from, res.Monitors, res.Channels),
			func(f *config.File) error { return f.AppendBlock(res.Render(f.EndArea())) })
		if err != nil {
			undo()
			return st, res, nil, err
		}
	}
	// Accounts once the section exists.
	var areas []string
	if o.Area != "" && o.Role != store.RoleAdmin {
		areas = []string{o.Area}
	}
	var notes []UserNote
	for _, u := range res.Users {
		n := UserNote{Username: u.Username, Result: "created"}
		if !u.Active {
			n.Result = "inactive" // disabled in Uptime Kuma: not carried over
		} else if a.AllowAdd("users") != nil {
			n.Result = "quota"
		} else if _, err := a.store.CreateUserHash(u.Username, u.Hash, o.Role, areas); err != nil {
			n.Result = resultOf(err)
		}
		notes = append(notes, n)
	}
	// Uptime Kuma push URLs keep working.
	for name, tok := range res.PushTokens {
		if err := a.store.SetPushToken(name, tok); err != nil {
			res.Notes = append(res.Notes, "The push URL of "+name+" could not be kept ("+err.Error()+"); use the new one shown on the monitor.")
		}
	}
	if mgrID != 0 && o.Area != "" {
		if err := a.grantArea(mgrID, o.Area); err != nil {
			return st, res, notes, err
		}
		notes = append(notes, UserNote{Username: o.Manager, Result: "manager"})
	}
	return st, res, notes, nil
}

func resultOf(err error) string {
	if errors.Is(err, store.ErrUserExists) {
		return "exists"
	}
	return "bad_hash"
}

// grantArea puts a user in charge of a section: unrestricted operators
// and administrators already are.
func (a *App) grantArea(id int64, area string) error {
	users, err := a.store.Users()
	if err != nil {
		return err
	}
	for _, u := range users {
		if u.ID != id {
			continue
		}
		switch {
		case u.Role == store.RoleAdmin, u.Role == store.RoleOperator && len(u.Areas) == 0:
			return nil
		case u.Role == store.RoleViewer:
			return a.store.SetRole(id, store.RoleOperator, []string{area})
		}
		for _, x := range u.Areas {
			if x == area {
				return nil
			}
		}
		return a.store.SetRole(id, store.RoleOperator, append(u.Areas, area))
	}
	return store.ErrNotFound
}

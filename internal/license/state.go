package license

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Kinds of state.
const (
	KindUnregistered = "unregistered"
	KindLicensed     = "licensed"
	KindExpired      = "expired"
)

// State is what the rest of unagikuma asks. Every failure lands on
// unregistered, never on something more permissive.
type State struct {
	Kind           string   `json:"kind"`
	Registered     bool     `json:"registered"`
	Reason         string   `json:"reason,omitempty"`
	Customer       string   `json:"customer,omitempty"`
	Licensee       string   `json:"licensee,omitempty"`
	Type           string   `json:"type,omitempty"`
	Tier           string   `json:"tier,omitempty"`
	InstallationID string   `json:"installation_id"`
	KeyID          string   `json:"key_id,omitempty"`
	Fingerprint    string   `json:"fingerprint,omitempty"`
	Days           int      `json:"days"` // left; -1 = never expires
	Expires        string   `json:"expires,omitempty"`
	UpdatesUntil   string   `json:"updates_until,omitempty"`
	MaxNodes       int      `json:"max_nodes"`
	MaxUsers       int      `json:"max_users"`
	Features       []string `json:"features"`
}

// Unregistered is the state without a valid licence.
func Unregistered(reason string) State {
	return State{Kind: KindUnregistered, Reason: reason, Features: []string{}}
}

// Check turns a licence file into a State. Trials name the installation
// they are for; paid licences need not, so they survive a move.
func Check(ring Keyring, f File, installID string, now time.Time) State {
	key, err := ring.Verify(f)
	if err != nil {
		s := Unregistered(err.Error())
		if key.Note != "" {
			s.Reason += " (" + key.Note + ")"
		}
		return s
	}
	if f.InstallationID != "" && f.InstallationID != installID {
		return Unregistered("license: issued for installation " + f.InstallationID)
	}
	s := State{Kind: KindLicensed, Registered: true, Customer: f.Customer, Licensee: f.Licensee, Type: f.License, Tier: f.Tier,
		KeyID: key.ID, Fingerprint: Fingerprint(f), Days: -1, MaxNodes: f.MaxNodes, MaxUsers: f.MaxUsers,
		Features: CanonFeatures(f.Features)}
	if f.UpdatesUntil != nil {
		s.UpdatesUntil = *f.UpdatesUntil
	}
	if f.Expires != nil && *f.Expires != "" {
		end, err := Day(*f.Expires)
		if err != nil {
			return Unregistered(err.Error())
		}
		s.Expires = *f.Expires
		if now.After(end) {
			s.Kind, s.Registered, s.Reason, s.Days, s.Features = KindExpired, false, "license: expired on "+*f.Expires, 0, []string{}
			return s
		}
		s.Days = int(math.Ceil(end.Sub(now).Hours() / 24))
	}
	return s
}

// Allowed reports whether a registered-only feature may be used.
func (s State) Allowed(feature string) bool {
	if !s.Registered {
		return false
	}
	for _, f := range s.Features {
		if f == feature {
			return true
		}
	}
	return false
}

// Parse reads a licence from JSON.
func Parse(b []byte) (File, error) {
	var f File
	if err := json.Unmarshal(b, &f); err != nil {
		return File{}, fmt.Errorf("license: %w", err)
	}
	return f, nil
}

// InstallationFile holds this installation's number, apart from the
// licence so replacing license.json keeps it.
const InstallationFile = "installation"

// Checker holds the licence state of a data directory and re-reads it, so
// a licence dropped into the directory takes effect without a restart.
type Checker struct {
	dir     string
	install string
	ring    Keyring
	mu      sync.Mutex
	st      State
}

// Open prepares the licence state of a data directory.
func Open(dir string) (*Checker, error) {
	return OpenWith(dir, Release())
}

// OpenWith uses another keyring (tests).
func OpenWith(dir string, ring Keyring) (*Checker, error) {
	id, err := installation(dir)
	if err != nil {
		return nil, err
	}
	c := &Checker{dir: dir, install: id, ring: ring}
	c.Check(time.Now())
	return c, nil
}

func installation(dir string) (string, error) {
	path := filepath.Join(dir, InstallationFile)
	if b, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(b)); id != "" {
			return id, nil
		}
	}
	n, err := rand.Int(rand.Reader, big.NewInt(90000000))
	if err != nil {
		return "", err
	}
	id := fmt.Sprintf("%08d", n.Int64()+10000000)
	return id, os.WriteFile(path, []byte(id+"\n"), 0o644)
}

// InstallationID is this installation's number, for trial requests.
func (c *Checker) InstallationID() string { return c.install }

// State returns the last checked state.
func (c *Checker) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.st
}

// Check reads license.json again.
func (c *Checker) Check(now time.Time) State {
	st := Unregistered("")
	if b, err := os.ReadFile(filepath.Join(c.dir, FileName)); err == nil {
		if f, err := Parse(b); err != nil {
			st = Unregistered(err.Error())
		} else {
			st = Check(c.ring, f, c.install, now)
		}
	}
	st.InstallationID = c.install
	c.mu.Lock()
	c.st = st
	c.mu.Unlock()
	return st
}

// Install checks a licence and, only when it is valid, writes it as
// license.json.
func (c *Checker) Install(data []byte, now time.Time) (State, error) {
	f, err := Parse(data)
	if err != nil {
		return c.State(), err
	}
	st := Check(c.ring, f, c.install, now)
	if !st.Registered {
		return c.State(), fmt.Errorf("%s", st.Reason)
	}
	b, _ := json.MarshalIndent(f, "", "  ")
	tmp := filepath.Join(c.dir, FileName+".new")
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return c.State(), err
	}
	if err := os.Rename(tmp, filepath.Join(c.dir, FileName)); err != nil {
		return c.State(), err
	}
	return c.Check(now), nil
}

// Remove deletes license.json.
func (c *Checker) Remove(now time.Time) (State, error) {
	err := os.Remove(filepath.Join(c.dir, FileName))
	if err != nil && !os.IsNotExist(err) {
		return c.State(), err
	}
	return c.Check(now), nil
}

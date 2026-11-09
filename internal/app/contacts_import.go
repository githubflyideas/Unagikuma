package app

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/githubflyideas/unagikuma/internal/directory"
	"github.com/githubflyideas/unagikuma/internal/store"
)

var handleClean = regexp.MustCompile(`[^a-z0-9._-]+`)

// ImportContacts adds a person for every channel address the directory
// does not know yet, named after the address: the quick way to bring an
// existing setup under the directory, then rename and merge by hand. Each
// one is reached through the channels that already name them.
func (a *App) ImportContacts() (created int, errs []string) {
	st := a.store
	taken := map[string]bool{}
	for _, p := range a.Directory().People() {
		taken[p.Handle] = true
	}
	unique := func(h string) string {
		h = strings.Trim(handleClean.ReplaceAllString(strings.ToLower(h), "-"), "-._")
		if h == "" || !store.ValidHandle(h) {
			h = "contact"
		}
		base, n := h, 2
		for taken[h] {
			h = base + "-" + strconv.Itoa(n)
			n++
		}
		taken[h] = true
		return h
	}
	unknown := a.UnknownAddresses()
	cfg := a.EffectiveConfig()
	via := func(kind, addr string) []string {
		var out []string
		if cfg == nil {
			return out
		}
		for _, ch := range cfg.Channels {
			key, k, ok := directory.AddressKey(ch.Kind)
			if !ok || k != kind {
				continue
			}
			for _, item := range directory.SplitList(ch.Opts[key]) {
				if kind == store.ContactPhone && store.SamePhone(item, addr) || kind == store.ContactEmail && store.NormalizeEmail(item) == addr {
					out = append(out, ch.Name)
					break
				}
			}
		}
		return out
	}
	for _, raw := range unknown[store.ContactEmail] {
		addr := store.NormalizeEmail(raw)
		name := strings.TrimSpace(strings.Trim(strings.SplitN(raw, "<", 2)[0], `" `))
		local, _, _ := strings.Cut(addr, "@")
		if name == "" || strings.Contains(name, "@") {
			name = local
		}
		if _, err := st.SavePerson(store.Person{Handle: unique(local), Name: name, Emails: []string{addr}, Notify: via(store.ContactEmail, addr)}); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		created++
	}
	for _, raw := range unknown[store.ContactPhone] {
		ph := store.NormalizePhone(raw)
		tail := ph
		if len(tail) > 4 {
			tail = tail[len(tail)-4:]
		}
		if _, err := st.SavePerson(store.Person{Handle: unique("phone-" + tail), Name: ph, Phones: []string{ph}, Notify: via(store.ContactPhone, ph)}); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		created++
	}
	return created, errs
}

// importContactsOnce runs once, on the first start of a release where
// e-mail and SMS go only to people in the directory: the addresses the
// channels already use are added, so nothing that was delivered before
// stops. Later addresses are offered for the directory when a channel is
// saved.
func (a *App) importContactsOnce() {
	const key = "contacts_whitelist_import"
	if v, _ := a.store.Setting(key); v != "" {
		return
	}
	created, errs := a.ImportContacts()
	if created > 0 || len(errs) > 0 {
		_ = a.store.Audit(auditEntry("contacts.import_on_upgrade", "system", map[string]any{"created": created, "errors": strings.Join(errs, "; ")}))
		a.log.Info("channel addresses added to the contact directory", "created", created, "errors", len(errs))
	}
	_ = a.store.SetSetting(key, "1")
	if created > 0 {
		a.applyConfig(a.cfg.current().Config)
	}
}

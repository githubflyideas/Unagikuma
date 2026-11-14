package app

import (
	"context"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/directory"
	"github.com/githubflyideas/unagikuma/internal/i18n"
	"github.com/githubflyideas/unagikuma/internal/notify"
	"github.com/githubflyideas/unagikuma/internal/store"
	"github.com/githubflyideas/unagikuma/internal/tunables"
)

// Directory returns the contact directory the channels are checked against.
func (a *App) Directory() *directory.Directory {
	if d := a.dir.Load(); d != nil {
		return d
	}
	return directory.New(nil, nil, 0)
}

func (a *App) loadDirectory(ts tunables.Set) {
	people, err := a.store.People()
	if err != nil {
		a.log.Warn("contact directory", "err", err)
	}
	bounces, err := a.store.Bounces()
	if err != nil {
		a.log.Warn("bounces", "err", err)
	}
	a.dir.Store(directory.New(people, bounces, ts.Int("contacts.bounce-threshold")))
}

// DirectoryChanged reloads the directory and re-applies the channels, so
// someone marked as having left stops receiving at once.
func (a *App) DirectoryChanged() {
	a.reapply() // reloads the directory
	a.emit("directory", nil)
}

// send delivers through the notifier and counts the recipients refused
// as nonexistent; once one crosses the threshold the channels are
// re-applied without it.
func (a *App) send(ctx context.Context, ch notify.Channel, m *notify.Message) notify.Attempt {
	if m.Site == "" {
		m.Site = a.site()
	}
	a.noteLeftOut(ch, m)
	at := a.sender.Send(ctx, ch, m)
	a.noteGateway(ch, at)
	if len(at.Bounces) == 0 {
		return at
	}
	_, kind, ok := directory.AddressKey(ch.Kind)
	if !ok {
		return at
	}
	for _, b := range at.Bounces {
		if err := a.store.RecordBounce(kind, b.Address, b.Reason); err != nil {
			a.log.Warn("recording a bounce", "address", b.Address, "err", err)
		}
		a.log.Warn("recipient refused as nonexistent", "channel", ch.Name, "address", b.Address, "reason", b.Reason)
	}
	// The engine is mid-delivery here: re-apply from another goroutine,
	// once for a burst of bounces.
	if a.dirReload.CompareAndSwap(false, true) {
		go func() {
			defer a.dirReload.Store(false)
			a.DirectoryChanged()
		}()
	}
	return at
}

// ChannelIssues lists the directory problems of every channel that sends
// to people, by channel name.
func (a *App) ChannelIssues() map[string][]directory.Issue {
	out := map[string][]directory.Issue{}
	c := a.applied.Load()
	if c == nil {
		return out
	}
	d := a.Directory()
	for _, ch := range c.Channels {
		key, kind, ok := directory.AddressKey(ch.Kind)
		if !ok || strings.Contains(ch.Opts[key], "${") {
			continue
		}
		if _, issues := d.Resolve(kind, ch.Opts[key]); len(issues) > 0 {
			out[ch.Name] = issues
		}
	}
	return out
}

// UnknownAddresses lists, per kind, the channel addresses the directory
// does not know (what "import from channels" would add).
func (a *App) UnknownAddresses() map[string][]string {
	out := map[string][]string{}
	c := a.applied.Load()
	if c == nil {
		return out
	}
	d := a.Directory()
	seen := map[string]bool{}
	for _, ch := range c.Channels {
		key, kind, ok := directory.AddressKey(ch.Kind)
		if !ok || strings.Contains(ch.Opts[key], "${") {
			continue
		}
		for _, v := range d.Unknown(kind, ch.Opts[key]) {
			k := kind + "|" + strings.ToLower(v)
			if kind == store.ContactPhone {
				k = kind + "|" + store.NormalizePhone(v)
			}
			if !seen[k] {
				seen[k] = true
				out[kind] = append(out[kind], v)
			}
		}
	}
	return out
}

// noteLeftOut logs the recipients a channel leaves out (not active
// contacts, or bouncing) each time it sends, and, with
// contacts.notify-admins, tells the administrators of the monitor's team,
// at most once a day per channel and team.
func (a *App) noteLeftOut(ch notify.Channel, m *notify.Message) {
	lp := a.leftOut.Load()
	if lp == nil || len((*lp)[ch.Name]) == 0 || m.DryRun {
		return
	}
	issues := (*lp)[ch.Name]
	var list []string
	for _, i := range issues {
		list = append(list, i.Address+" ("+i.Code+")")
	}
	a.log.Warn("recipients left out", "channel", ch.Name, "monitor", m.Monitor, "recipients", strings.Join(list, ", "))
	cfg := a.applied.Load()
	if cfg == nil || !cfg.Tunables.Bool("contacts.notify-admins") {
		return
	}
	team := config.AdminTeam
	if mon := cfg.FindMonitor(m.Monitor); mon != nil {
		team = mon.TeamOf()
	}
	tp := a.teamAdmins.Load()
	if tp == nil || len((*tp)[team]) == 0 {
		return
	}
	key := ch.Name + "|" + team
	now := a.now()
	a.toldMu.Lock()
	if a.told == nil {
		a.told = map[string]time.Time{}
	}
	if last, ok := a.told[key]; ok && now.Sub(last) < 24*time.Hour {
		a.toldMu.Unlock()
		return
	}
	a.told[key] = now
	a.toldMu.Unlock()
	lang := a.messageLang()
	msg := &notify.Message{Kind: "SYSTEM", Monitor: m.Monitor, Site: m.Site, Brand: m.Brand,
		Title: i18n.T(lang, "sys.left_out_title", "channel", ch.Name),
		Body:  i18n.T(lang, "sys.left_out_body", "channel", ch.Name, "recipients", strings.Join(list, ", "))}
	for _, c := range (*tp)[team] {
		go a.sender.Send(context.Background(), c, msg)
	}
}

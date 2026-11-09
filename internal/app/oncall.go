package app

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/directory"
	"github.com/githubflyideas/unagikuma/internal/oncall"
	"github.com/githubflyideas/unagikuma/internal/store"
)

// currentOverrides are the stand-ins not over yet, for the engine.
func (a *App) currentOverrides() []oncall.Override {
	os, err := a.store.Overrides(a.now())
	if err != nil {
		a.log.Warn("on-call stand-ins", "err", err)
	}
	return os
}

// rotations returns the on-call rotations of cfg by name.
func rotations(cfg *config.Config) map[string]*oncall.Rotation {
	out := map[string]*oncall.Rotation{}
	for _, c := range cfg.Channels {
		if c.Kind != "group" {
			continue
		}
		if r, err := oncall.Parse(c.Opts, c.Members); err == nil && r != nil {
			out[c.Name] = r
		}
	}
	return out
}

// ErrOnCall is a stand-in that cannot be arranged; the message says why.
var ErrOnCall = errors.New("on-call")

// AddOverride arranges a stand-in and applies it at once.
func (a *App) AddOverride(o oncall.Override) (oncall.Override, error) {
	r := rotations(a.Config().Config)[o.Rotation]
	switch {
	case r == nil:
		return o, fmt.Errorf("%w: %s is not an on-call rotation", ErrOnCall, o.Rotation)
	case !slices.Contains(r.Members, o.Member):
		return o, fmt.Errorf("%w: %s is not a member of %s", ErrOnCall, o.Member, o.Rotation)
	case !o.Until.After(o.From):
		return o, fmt.Errorf("%w: the end must be after the start", ErrOnCall)
	case !o.Until.After(a.now()):
		return o, fmt.Errorf("%w: the stand-in would already be over", ErrOnCall)
	}
	o.CreatedAt = a.now()
	o, err := a.store.AddOverride(o)
	if err != nil {
		return o, err
	}
	a.reapply()
	a.emit("oncall", nil)
	return o, nil
}

// DeleteOverride cancels a stand-in.
func (a *App) DeleteOverride(id int64) (bool, error) {
	ok, err := a.store.DeleteOverride(id)
	if err == nil && ok {
		a.reapply()
		a.emit("oncall", nil)
	}
	return ok, err
}

// OnCallMember is a member of a rotation and the people it reaches.
type OnCallMember struct {
	Channel string   `json:"channel"`
	Kind    string   `json:"kind"`
	People  []string `json:"people"`
}

// OnCallShift is a shift with the stand-ins that cover part of it.
type OnCallShift struct {
	oncall.Shift
	StandIns []oncall.Override `json:"stand_ins"`
}

// OnCallRotation is what the on-call page shows of a rotation.
type OnCallRotation struct {
	Name      string            `json:"name"`
	Rotate    string            `json:"rotate"`
	Handoff   string            `json:"handoff"`
	Start     string            `json:"start,omitempty"`
	TZ        string            `json:"tz"` // the time zone it hands off in
	Members   []OnCallMember    `json:"members"`
	Now       string            `json:"now"`       // who is notified first now
	Scheduled oncall.Shift      `json:"scheduled"` // the shift running now
	StandIn   *oncall.Override  `json:"stand_in,omitempty"`
	Shifts    []OnCallShift     `json:"shifts"`
	Overrides []oncall.Override `json:"overrides"`
	Policies  []string          `json:"policies"` // policies that page this rotation
}

// OnCall describes every rotation: who is on call, the next shifts and
// the stand-ins arranged.
func (a *App) OnCall(shifts int) ([]OnCallRotation, error) {
	cfg := a.Config().Config
	now, loc := a.now(), a.opts.Location
	os, err := a.store.Overrides(now)
	if err != nil {
		return nil, err
	}
	dir := a.Directory()
	out := []OnCallRotation{}
	for name, r := range rotations(cfg) {
		ro := OnCallRotation{Name: name, Rotate: r.Src, Handoff: r.Handoff, Start: cfg.FindChannel(name).Opts[oncall.OptStart], TZ: rotationZone(r, loc),
			Scheduled: r.At(now, loc), Overrides: []oncall.Override{}, Policies: policiesUsing(cfg, name)}
		ro.Now = ro.Scheduled.Member
		if o := oncall.Active(name, os, now); o != nil {
			ro.StandIn, ro.Now = o, o.Member
		}
		for _, m := range r.Members {
			ro.Members = append(ro.Members, memberOf(cfg, dir, m))
		}
		for _, o := range os {
			if o.Rotation == name {
				ro.Overrides = append(ro.Overrides, o)
			}
		}
		for _, s := range r.Next(now, shifts, loc) {
			sh := OnCallShift{Shift: s, StandIns: []oncall.Override{}}
			for _, o := range ro.Overrides {
				if o.From.Before(s.Until) && o.Until.After(s.From) {
					sh.StandIns = append(sh.StandIns, o)
				}
			}
			ro.Shifts = append(ro.Shifts, sh)
		}
		out = append(out, ro)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// memberOf names the people a member channel reaches: those of the
// contact directory its recipients belong to.
func memberOf(cfg *config.Config, dir *directory.Directory, name string) OnCallMember {
	m := OnCallMember{Channel: name, People: []string{}}
	c := cfg.FindChannel(name)
	if c == nil {
		return m
	}
	m.Kind = c.Kind
	key, kind, ok := directory.AddressKey(c.Kind)
	if !ok {
		return m
	}
	for _, v := range directory.SplitList(c.Opts[key]) {
		var p *store.Person
		if h, isHandle := strings.CutPrefix(v, "@"); isHandle {
			p = dir.ByHandle(h)
		} else {
			p = dir.Owner(kind, v)
		}
		if p != nil && !slices.Contains(m.People, p.Name) {
			m.People = append(m.People, p.Name)
		}
	}
	return m
}

// policiesUsing lists the policies whose chain or escalation includes ch.
func policiesUsing(cfg *config.Config, ch string) []string {
	out := []string{}
	for _, p := range cfg.Policies {
		uses := slices.Contains(p.Chain, ch)
		for _, e := range p.Escalate {
			uses = uses || slices.Contains(e.Channels, ch)
		}
		if uses {
			out = append(out, p.Name)
		}
	}
	sort.Strings(out)
	return out
}

// ResponseStats are response times over a set of incidents.
type ResponseStats struct {
	Incidents int     `json:"incidents"`
	Acked     int     `json:"acked"`
	Resolved  int     `json:"resolved"`
	MTTA      float64 `json:"mtta_s"` // mean time to the first acknowledgement
	MTTR      float64 `json:"mttr_s"` // mean time to resolution
	ackSum    float64
	resSum    float64
}

func (s *ResponseStats) add(it store.IncidentTimes) {
	s.Incidents++
	if !it.FirstAck.IsZero() {
		s.Acked++
		s.ackSum += it.FirstAck.Sub(it.Opened).Seconds()
		s.MTTA = s.ackSum / float64(s.Acked)
	}
	if !it.Resolved.IsZero() {
		s.Resolved++
		s.resSum += it.Resolved.Sub(it.Opened).Seconds()
		s.MTTR = s.resSum / float64(s.Resolved)
	}
}

// MemberStats are the response times of the incidents that opened while
// a member was on call.
type MemberStats struct {
	Rotation string `json:"rotation"`
	Member   string `json:"member"`
	ResponseStats
}

// OnCallStatsResult covers a window of time.
type OnCallStatsResult struct {
	From    time.Time     `json:"from"`
	To      time.Time     `json:"to"`
	All     ResponseStats `json:"all"`
	Members []MemberStats `json:"members"`
}

// OnCallStats measures MTTA and MTTR over the incidents opened in the last
// window, overall and by who was on call when each opened (incidents of
// policies that page a rotation).
func (a *App) OnCallStats(window time.Duration) (OnCallStatsResult, error) {
	to := a.now()
	res := OnCallStatsResult{From: to.Add(-window), To: to, Members: []MemberStats{}}
	its, err := a.store.IncidentTimesBetween(res.From, to)
	if err != nil {
		return res, err
	}
	// Stand-ins over during the window still count.
	os, err := a.store.Overrides(res.From)
	if err != nil {
		return res, err
	}
	cfg := a.Config().Config
	rots := rotations(cfg)
	byPolicy := map[string][]string{}
	for name := range rots {
		for _, p := range policiesUsing(cfg, name) {
			byPolicy[p] = append(byPolicy[p], name)
		}
	}
	stats := map[[2]string]*MemberStats{}
	for name, r := range rots {
		for _, m := range r.Members {
			stats[[2]string{name, m}] = &MemberStats{Rotation: name, Member: m}
		}
	}
	// A written default policy became the teams' channels (team_migration.go):
	// its older incidents count under their monitor's routing now.
	def := cfg.FindPolicy("default")
	hasDefault := def != nil && !def.Implicit
	for _, it := range its {
		res.All.add(it)
		pol := it.Policy
		if pol == "default" && !hasDefault {
			if m := cfg.FindMonitor(it.Monitor); m != nil {
				pol = m.Policy
			}
		}
		for _, rot := range byPolicy[pol] {
			// Whoever took the problem notice; for older incidents, who
			// was on call when it opened.
			who := it.Responder(rot)
			if who == "" {
				who = rots[rot].At(it.Opened, a.opts.Location).Member
				if o := oncall.Active(rot, os, it.Opened); o != nil {
					who = o.Member
				}
			}
			if s := stats[[2]string{rot, who}]; s != nil {
				s.add(it)
			}
		}
	}
	for _, s := range stats {
		res.Members = append(res.Members, *s)
	}
	sort.Slice(res.Members, func(i, j int) bool {
		if res.Members[i].Rotation != res.Members[j].Rotation {
			return res.Members[i].Rotation < res.Members[j].Rotation
		}
		return slices.Index(rots[res.Members[i].Rotation].Members, res.Members[i].Member) < slices.Index(rots[res.Members[j].Rotation].Members, res.Members[j].Member)
	})
	return res, nil
}

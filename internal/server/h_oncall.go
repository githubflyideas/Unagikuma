package server

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/githubflyideas/unagikuma/internal/app"
	"github.com/githubflyideas/unagikuma/internal/oncall"
)

// oncallView is the on-call page: every rotation with who is on call and
// the next shifts, and the response times of the stats window.
func (s *Server) oncallView(w http.ResponseWriter, r *http.Request) (any, error) {
	ts := s.app.EffectiveConfig().Tunables
	rots, err := s.app.OnCall(ts.Int("oncall.shifts-shown"))
	if err != nil {
		return nil, err
	}
	stats, err := s.app.OnCallStats(ts.Duration("oncall.stats-window"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"rotations": rots, "stats": stats}, nil
}

// oncallCalendar lays out who is on call between from and to (RFC 3339;
// default: the next 28 days).
func (s *Server) oncallCalendar(w http.ResponseWriter, r *http.Request) (any, error) {
	from, to := s.now(), time.Time{}
	for _, p := range []struct {
		key string
		t   *time.Time
	}{{"from", &from}, {"to", &to}} {
		if v := r.URL.Query().Get(p.key); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return nil, Errorf(http.StatusBadRequest, CodeInvalid, "%s must be a time like 2026-11-01T00:00:00+08:00", p.key)
			}
			*p.t = t
		}
	}
	if to.IsZero() {
		to = from.Add(28 * 24 * time.Hour)
	}
	rots, err := s.app.OnCallCalendar(from, to)
	if errors.Is(err, app.ErrOnCall) {
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "%v", err)
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"from": from, "to": to, "rotations": rots}, nil
}

// oncallSwap swaps the shift running at a with the one running at b.
func (s *Server) oncallSwap(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Rotation string    `json:"rotation"`
		A        time.Time `json:"a"`
		B        time.Time `json:"b"`
		Note     string    `json:"note"`
	}
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	if req.A.IsZero() || req.B.IsZero() {
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "a and b are required: a time inside each shift")
	}
	os, err := s.app.SwapShifts(req.Rotation, req.A, req.B, authOf(r).user, req.Note)
	if errors.Is(err, app.ErrOnCall) {
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "%v", err)
	}
	if err != nil {
		return nil, err
	}
	auditOf(r).target = req.Rotation + ": " + os[1].Member + " ⇄ " + os[0].Member
	return map[string]any{"stand_ins": os}, nil
}

func (s *Server) oncallOverrideCreate(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Rotation string    `json:"rotation"`
		Member   string    `json:"member"`
		From     time.Time `json:"from"`
		Until    time.Time `json:"until"`
		Note     string    `json:"note"`
	}
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	if req.From.IsZero() {
		req.From = s.now()
	}
	o, err := s.app.AddOverride(oncall.Override{Rotation: req.Rotation, Member: req.Member, From: req.From, Until: req.Until,
		Note: req.Note, By: authOf(r).user})
	if errors.Is(err, app.ErrOnCall) {
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "%v", err)
	}
	if err != nil {
		return nil, err
	}
	auditOf(r).target = o.Rotation + ": " + o.Member
	return o, nil
}

func (s *Server) oncallOverrideDelete(w http.ResponseWriter, r *http.Request) (any, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return nil, notFound("stand-in")
	}
	ok, err := s.app.DeleteOverride(id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, notFound("stand-in")
	}
	return map[string]any{"deleted": id}, nil
}

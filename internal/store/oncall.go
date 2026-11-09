package store

import (
	"encoding/json"
	"time"

	"github.com/githubflyideas/unagikuma/internal/engine"

	"github.com/githubflyideas/unagikuma/internal/oncall"
)

// AddOverride records a stand-in and returns it with its id.
func (s *Store) AddOverride(o oncall.Override) (oncall.Override, error) {
	res, err := s.core.Exec(`INSERT INTO oncall_overrides(rotation, member, from_at, until_at, by, note, created_at) VALUES(?,?,?,?,?,?,?)`,
		o.Rotation, o.Member, o.From.Unix(), o.Until.Unix(), o.By, o.Note, o.CreatedAt.Unix())
	if err != nil {
		return o, err
	}
	o.ID, err = res.LastInsertId()
	return o, err
}

// DeleteOverride removes a stand-in; false when there was none.
func (s *Store) DeleteOverride(id int64) (bool, error) {
	res, err := s.core.Exec(`DELETE FROM oncall_overrides WHERE id=?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Overrides returns the stand-ins that end after since, by start.
func (s *Store) Overrides(since time.Time) ([]oncall.Override, error) {
	rows, err := s.core.Query(`SELECT id, rotation, member, from_at, until_at, by, note, created_at FROM oncall_overrides
		WHERE until_at>? ORDER BY from_at, id`, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []oncall.Override{}
	for rows.Next() {
		var o oncall.Override
		var from, until, created int64
		if err := rows.Scan(&o.ID, &o.Rotation, &o.Member, &from, &until, &o.By, &o.Note, &created); err != nil {
			return nil, err
		}
		o.From, o.Until, o.CreatedAt = time.Unix(from, 0), time.Unix(until, 0), time.Unix(created, 0)
		out = append(out, o)
	}
	return out, rows.Err()
}

// IncidentTimes is when an incident opened, was first acknowledged and
// was resolved (zero when not yet).
type IncidentTimes struct {
	ID       int64
	Monitor  string
	Policy   string
	Opened   time.Time
	FirstAck time.Time
	Resolved time.Time
	// Responders are who took its problem notices, by rotation
	// ("rotation=member,..."; empty for incidents from before this was kept).
	Responders string
}

// Responder is the member of rotation who took the incident's problem
// notices, "" when unknown.
func (it IncidentTimes) Responder(rotation string) string {
	st := engine.IncidentState{Responders: it.Responders}
	return st.Responder(rotation)
}

// IncidentTimesBetween returns the incidents of monitors opened in
// [from, to), drills and this host's own outages aside. Incident times
// are kept in milliseconds, acknowledgements in seconds.
func (s *Store) IncidentTimesBetween(from, to time.Time) ([]IncidentTimes, error) {
	rows, err := s.core.Query(`SELECT i.id, i.monitor, COALESCE(i.policy,''), i.opened_at, COALESCE(i.resolved_at,0), COALESCE(i.state,''),
		COALESCE((SELECT MIN(a.at) FROM acks a WHERE a.incident_id=i.id AND a.action='ack'),0)
		FROM incidents i WHERE i.opened_at>=? AND i.opened_at<? AND i.drill=0 AND COALESCE(i.system,'')=''
		ORDER BY i.opened_at`, from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IncidentTimes
	at := func(v int64, conv func(int64) time.Time) time.Time {
		if v <= 0 {
			return time.Time{}
		}
		return conv(v)
	}
	secs := func(v int64) time.Time { return time.Unix(v, 0) }
	for rows.Next() {
		var it IncidentTimes
		var opened, resolved, ack int64
		var state string
		if err := rows.Scan(&it.ID, &it.Monitor, &it.Policy, &opened, &resolved, &state, &ack); err != nil {
			return nil, err
		}
		var st struct {
			Responders string `json:"responders"`
		}
		if json.Unmarshal([]byte(state), &st) == nil {
			it.Responders = st.Responders
		}
		it.Opened, it.Resolved, it.FirstAck = time.UnixMilli(opened), at(resolved, time.UnixMilli), at(ack, secs)
		out = append(out, it)
	}
	return out, rows.Err()
}

package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/engine"
)

// Decisions returns the ledger of one incident: the steps of its alerts,
// plus the monitor's own entries (held, taken back, released) between
// from and to, oldest first.
func (s *Store) Decisions(incident int64, monitor string, from, to time.Time) ([]engine.Decision, error) {
	s.Flush()
	var site, epoch string
	_ = s.core.QueryRow(`SELECT site,source_epoch FROM incidents WHERE id=?`, incident).Scan(&site, &epoch)
	q := `SELECT data FROM decisions WHERE site=? AND (incident_id=?`
	args := []any{site, incident}
	if monitor != "" {
		q += ` OR (incident_id=0 AND monitor=? AND at>=? AND at<=? AND source_epoch=?)`
		args = append(args, monitor, ms(from), ms(to), epoch)
	}
	rows, err := s.core.Query(q+`) ORDER BY at, id LIMIT 5000`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []engine.Decision{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return out, err
		}
		var d engine.Decision
		if json.Unmarshal([]byte(data), &d) == nil {
			out = append(out, d)
		}
	}
	return out, rows.Err()
}

// DeliveriesOf returns the notification log of one incident, oldest first.
func (s *Store) DeliveriesOf(incident int64) ([]DeliveryRow, error) {
	s.Flush()
	rows, err := s.core.Query(`SELECT id,intent_id,kind,seq,at,attempts,ok,fallback,drill,delayed_s FROM deliveries
		WHERE incident_id=? ORDER BY at, id LIMIT 1000`, incident)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DeliveryRow{}
	for rows.Next() {
		d := DeliveryRow{IncidentID: incident}
		var at int64
		var att, intent sql.NullString
		if err := rows.Scan(&d.ID, &intent, &d.Kind, &d.Seq, &at, &att, &d.OK, &d.Fallback, &d.Drill, &d.DelayedS); err != nil {
			return out, err
		}
		d.IntentID, d.At = intent.String, fromMS(at)
		_ = json.Unmarshal([]byte(att.String), &d.Attempts)
		out = append(out, d)
	}
	return out, rows.Err()
}

// EventsBetween returns events of the given types (all when none) in
// [from, to], oldest first, at most limit.
func (s *Store) EventsBetween(from, to time.Time, types []string, limit int, sites ...string) ([]EventRow, error) {
	s.Flush()
	q := `SELECT id,at,type,severity,monitor,message,data FROM events WHERE at>=? AND at<=? AND site=?`
	args := []any{ms(from), ms(to), selectedSite(sites)}
	if len(sites) > 1 && sites[0] != "" {
		q += " AND source_epoch=?"
		args = append(args, sites[1])
	}
	if len(types) > 0 {
		q += " AND type IN (?" + strings.Repeat(",?", len(types)-1) + ")"
		for _, t := range types {
			args = append(args, t)
		}
	}
	rows, err := s.core.Query(q+fmt.Sprintf(" ORDER BY at, id LIMIT %d", max(1, min(limit, 5000))), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EventRow{}
	for rows.Next() {
		var e EventRow
		var at int64
		var sev, mon, msg, data sql.NullString
		if err := rows.Scan(&e.ID, &at, &e.Type, &sev, &mon, &msg, &data); err != nil {
			return out, err
		}
		e.At, e.Severity, e.Monitor, e.Message = fromMS(at), sev.String, mon.String, msg.String
		if data.Valid {
			_ = json.Unmarshal([]byte(data.String), &e.Data)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// RoundRow is one stored check round.
type RoundRow struct {
	At      time.Time `json:"at"`
	Status  string    `json:"status"`
	Verdict string    `json:"verdict"`
	Hard    bool      `json:"hard,omitzero"`
	Reason  string    `json:"reason,omitempty"`
}

// Rounds returns a monitor's rounds in [from, to], oldest first (only
// while raw history is kept), at most limit.
func (s *Store) Rounds(monitor string, from, to time.Time, limit int, sites ...string) ([]RoundRow, error) {
	s.Flush()
	mid := s.MonitorID(monitor)
	if mid == 0 {
		return nil, nil
	}
	q := `SELECT at,st,flags,reason FROM rounds WHERE mid=? AND at>=? AND at<=? AND site=?`
	args := []any{mid, ms(from), ms(to), selectedSite(sites)}
	if len(sites) > 1 && sites[0] != "" {
		q += " AND source_epoch=?"
		args = append(args, sites[1])
	}
	rows, err := s.hist.Query(q+fmt.Sprintf(" ORDER BY at LIMIT %d", max(1, min(limit, 20000))), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := map[int]string{}
	for k, v := range verdictCodes {
		names[v] = k
	}
	out := []RoundRow{}
	for rows.Next() {
		var at int64
		var st, flags int
		var reason sql.NullString
		if err := rows.Scan(&at, &st, &flags, &reason); err != nil {
			return out, err
		}
		out = append(out, RoundRow{At: fromMS(at), Status: statusNames[st], Verdict: names[(flags>>verdictShift)&3],
			Hard: flags&flagHard != 0, Reason: reason.String})
	}
	return out, rows.Err()
}

package store

import "time"

// IncidentTimes is when an incident opened, was first acknowledged and
// was resolved (zero when not yet).
type IncidentTimes struct {
	ID       int64
	Monitor  string
	Policy   string
	Opened   time.Time
	FirstAck time.Time
	Resolved time.Time
}

// IncidentTimesBetween returns the incidents of monitors opened in
// [from, to), drills and this host's own outages aside. Incident times
// are kept in milliseconds, acknowledgements in seconds.
func (s *Store) IncidentTimesBetween(from, to time.Time) ([]IncidentTimes, error) {
	rows, err := s.core.Query(`SELECT i.id, i.monitor, COALESCE(i.policy,''), i.opened_at, COALESCE(i.resolved_at,0),
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
		if err := rows.Scan(&it.ID, &it.Monitor, &it.Policy, &opened, &resolved, &ack); err != nil {
			return nil, err
		}
		it.Opened, it.Resolved, it.FirstAck = time.UnixMilli(opened), at(resolved, time.UnixMilli), at(ack, secs)
		out = append(out, it)
	}
	return out, rows.Err()
}

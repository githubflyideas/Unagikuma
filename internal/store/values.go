package store

import (
	"time"

	"github.com/githubflyideas/unagikuma/internal/valuerule"
)

// ValueSamples returns the values monitors' rules judged since a time, by
// monitor, oldest first.
func (s *Store) ValueSamples(since time.Time) (map[string][]valuerule.Sample, error) {
	rows, err := s.core.Query(`SELECT monitor,at,v FROM value_samples WHERE at>=? ORDER BY monitor,at`, ms(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]valuerule.Sample{}
	for rows.Next() {
		var name string
		var at int64
		var v float64
		if err := rows.Scan(&name, &at, &v); err != nil {
			return nil, err
		}
		out[name] = append(out[name], valuerule.Sample{At: fromMS(at), V: v})
	}
	return out, rows.Err()
}

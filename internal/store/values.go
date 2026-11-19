package store

import (
	"time"

	"github.com/githubflyideas/unagikuma/internal/valuerule"
)

// ValueSamples returns the values monitors' rules judged since a time, by
// monitor and the definition that produced them, oldest first.
func (s *Store) ValueSamples(since time.Time) (map[string]map[string][]valuerule.Sample, error) {
	rows, err := s.core.Query(`SELECT monitor,def_hash,at,v FROM value_samples WHERE at>=? ORDER BY monitor,at`, ms(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string][]valuerule.Sample{}
	for rows.Next() {
		var name, hash string
		var at int64
		var v float64
		if err := rows.Scan(&name, &hash, &at, &v); err != nil {
			return nil, err
		}
		if out[name] == nil {
			out[name] = map[string][]valuerule.Sample{}
		}
		out[name][hash] = append(out[name][hash], valuerule.Sample{At: fromMS(at), V: v})
	}
	return out, rows.Err()
}

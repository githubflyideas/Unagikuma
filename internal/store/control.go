package store

import (
	"fmt"
	"strings"
)

// CopyControlFrom copies only authority-owned notification configuration.
// Incidents, ACKs, intents, delivery health, evidence and audit chains stay local.
// Promotion also transfers agent authentication hashes with configuration
// authority; ordinary replica refreshes do not. src is an immutable snapshot.
func (s *Store) CopyControlFrom(src *Store, changes bool) error {
	tables := []string{"people", "contacts", "oncall_overrides"}
	if changes {
		tables = append(tables, "changes", "agent_tokens")
	}
	type snapshot struct {
		name string
		cols []string
		rows [][]any
	}
	var snapshots []snapshot
	for _, name := range tables {
		rows, err := src.core.Query("SELECT * FROM " + name)
		if err != nil {
			return err
		}
		cols, err := rows.Columns()
		if err != nil {
			rows.Close()
			return err
		}
		snap := snapshot{name: name, cols: cols}
		for rows.Next() {
			values := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range values {
				ptrs[i] = &values[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				rows.Close()
				return err
			}
			for i, v := range values {
				if b, ok := v.([]byte); ok {
					values[i] = string(b)
				}
			}
			snap.rows = append(snap.rows, values)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		snapshots = append(snapshots, snap)
	}
	tx, err := s.core.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// contacts references people; neither table references local incidents.
	for i := len(snapshots) - 1; i >= 0; i-- {
		if _, err := tx.Exec("DELETE FROM " + snapshots[i].name); err != nil {
			return err
		}
	}
	for _, snap := range snapshots {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(snap.cols)), ",")
		query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", snap.name, strings.Join(snap.cols, ","), placeholders)
		for _, row := range snap.rows {
			if _, err := tx.Exec(query, row...); err != nil {
				return err
			}
		}
	}
	// Offboarding must also revoke an existing local login; never copy passwords.
	if _, err := tx.Exec("UPDATE users SET disabled=1 WHERE username IN (SELECT username FROM people WHERE left_at>0 AND username!='')"); err != nil {
		return err
	}
	return tx.Commit()
}

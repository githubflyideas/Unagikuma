package app

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/githubflyideas/unagikuma/internal/engine"
	"github.com/githubflyideas/unagikuma/internal/schedule"
	_ "modernc.org/sqlite" // read the master's copied database
)

// replicaCarry contains shared maintenance, not source-local ACKs or silences.
// Silences is retained only to decode old snapshots; carryWindows ignores it.
type replicaCarry struct {
	Silences []carrySilence `json:"silences,omitempty"`
	Changes  []carryChange  `json:"changes,omitempty"`
}

type carrySilence struct {
	Monitor string    `json:"monitor"`
	Until   time.Time `json:"until"`
}

type carryChange struct {
	ID    int64     `json:"id"`
	Title string    `json:"title"`
	Sel   string    `json:"sel"`
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

const replicaCarryFile = "carry.json"

// readCarry reads planned/running changes from an unpacked copy of the
// master's core database.
func readCarry(dir string, now time.Time) (replicaCarry, error) {
	var c replicaCarry
	path := filepath.Join(dir, "core.db")
	if _, err := os.Stat(path); err != nil {
		return c, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return c, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT id,title,sel,start_at,end_at FROM changes WHERE state IN ('planned','running') AND end_at>?`, now.UnixMilli())
	if err != nil {
		return c, nil // an older master without change windows
	}
	defer rows.Close()
	for rows.Next() {
		var ch carryChange
		var s, e int64
		if rows.Scan(&ch.ID, &ch.Title, &ch.Sel, &s, &e) == nil {
			ch.Start, ch.End = time.UnixMilli(s), time.UnixMilli(e)
			c.Changes = append(c.Changes, ch)
		}
	}
	return c, rows.Err()
}

// carryWindows turns shared changes (never another site's silences) into maintenance
// windows for the local engine.
func (a *App) carryWindows(sel func(string) []string) []engine.MaintSpec {
	b, err := os.ReadFile(a.dataPath(replicaDir, replicaCarryFile))
	if err != nil {
		return nil
	}
	var c replicaCarry
	if json.Unmarshal(b, &c) != nil {
		return nil
	}
	var out []engine.MaintSpec
	for _, ch := range c.Changes {
		if mons := sel(ch.Sel); len(mons) > 0 {
			out = append(out, engine.MaintSpec{Name: fmt.Sprintf("change-%d", ch.ID), Title: ch.Title, Monitors: mons,
				Schedule: schedule.Spec{At: ch.Start, Duration: ch.End.Sub(ch.Start)}})
		}
	}
	return out
}

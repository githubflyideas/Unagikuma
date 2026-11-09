// Package observation contains informational, source-owned observations.
package observation

import (
	"fmt"
	"strings"
	"time"
)

type Row struct {
	MonitorID   string    `json:"monitor_id"`
	NodeID      string    `json:"node_id"`
	Status      string    `json:"status"`
	CheckedAt   time.Time `json:"checked_at"`
	StatusSince time.Time `json:"status_since"`
	ReceivedAt  time.Time `json:"received_at"`
	LatencyMS   float64   `json:"latency_ms"`
	Error       string    `json:"error,omitempty"`
	Epoch       string    `json:"epoch"`
	Seq         int64     `json:"seq"`
	IntervalMS  int64     `json:"interval_ms"`
	AgeMS       int64     `json:"age_ms"`
	DefHash     string    `json:"def_hash"`
	Stale       bool      `json:"stale"`
}
type Delta struct {
	NodeID string `json:"node_id"`
	Epoch  string `json:"epoch"`
	Cursor int64  `json:"cursor"`
	More   bool   `json:"more"`
	Rows   []Row  `json:"rows"`
}
type Snapshot struct {
	TakenAt time.Time `json:"taken_at"`
	Rows    []Row     `json:"rows"`
	Gateway string    `json:"gateway"`
	Uplink  string    `json:"uplink"`
}

func (s Snapshot) Summary() string {
	var parts []string
	up := 0
	for _, r := range s.Rows {
		mark := "?"
		if !r.Stale {
			switch r.Status {
			case "up":
				mark = "○"
				up++
			case "down":
				mark = "×"
			}
		} else {
			mark = "~"
		}
		parts = append(parts, r.NodeID+" "+mark)
	}
	return strings.Join(parts, ", ") + fmt.Sprintf(", %d/%d", up, len(s.Rows))
}

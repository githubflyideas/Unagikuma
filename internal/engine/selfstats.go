package engine

import "sync/atomic"

// selfStats counts the engine's own work for /metrics: a monitoring host
// that falls behind shows it here (rounds dropped, rounds starting late)
// before its results go wrong.
type selfStats struct {
	started   atomic.Int64
	dropped   atomic.Int64
	lagMicros atomic.Int64 // sum of how late rounds started after their due time
}

// SelfStats is a snapshot of the engine's counters since start.
type SelfStats struct {
	RoundsStarted int64
	RoundsDropped int64
	// StartLagSeconds is the total delay of round starts after their due
	// time; divided by RoundsStarted it is the average delay.
	StartLagSeconds float64
}

// SelfStats returns the counters; safe from any goroutine.
func (e *Engine) SelfStats() SelfStats {
	return SelfStats{RoundsStarted: e.stats.started.Load(), RoundsDropped: e.stats.dropped.Load(),
		StartLagSeconds: float64(e.stats.lagMicros.Load()) / 1e6}
}

package app

import (
	"sync"
	"time"

	"github.com/githubflyideas/unagikuma/internal/cluster"
)

// rejoinSeen remembers nonces so a CA-signed rejoin request cannot be
// replayed to mint extra join codes within its validity window.
type rejoinSeen struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

// ConsumeRejoinNonce records req.Nonce and returns false if it was already used.
func (a *App) ConsumeRejoinNonce(req cluster.RejoinRequest) bool {
	a.rejoinOnce.Do(func() { a.rejoinSeen.seen = map[string]time.Time{} })
	key := req.Nonce
	if key == "" {
		return false
	}
	now := a.now()
	a.rejoinSeen.mu.Lock()
	defer a.rejoinSeen.mu.Unlock()
	for k, exp := range a.rejoinSeen.seen {
		if now.After(exp) {
			delete(a.rejoinSeen.seen, k)
		}
	}
	if _, ok := a.rejoinSeen.seen[key]; ok {
		return false
	}
	a.rejoinSeen.seen[key] = now.Add(10 * time.Minute)
	return true
}

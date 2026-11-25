package app

import (
	"github.com/githubflyideas/unagikuma/internal/cluster"
	"os"
	"path/filepath"
	"time"
)

// Site identity is independent of the transferable configuration authority CA.
func loadSitePKI(dir string, now time.Time) (*cluster.PKI, error) {
	p, err := cluster.LoadOrCreatePKI(dir, now)
	if err != nil {
		return nil, err
	}
	peerDir := filepath.Join(dir, "site-identity")
	if err := os.MkdirAll(peerDir, 0700); err != nil {
		return nil, err
	}
	p.Peer, err = cluster.LoadOrCreatePKI(peerDir, now)
	return p, err
}

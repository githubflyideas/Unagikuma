package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/githubflyideas/unagikuma/internal/cluster"
)

const peerIdentityFile = "cluster-peer-identities.json"

// Only authenticated agent/master sessions introduce independent peer CAs.
func (a *App) peerIdentities() []cluster.PeerAddr {
	var out []cluster.PeerAddr
	if b, err := os.ReadFile(a.dataPath(peerIdentityFile)); err == nil {
		_ = json.Unmarshal(b, &out)
	}
	if a.master != nil {
		out = append(out, a.master.PeerIdentities()...)
	}
	a.am.mu.Lock()
	ag := a.am.agent
	a.am.mu.Unlock()
	if ag != nil {
		for _, v := range ag.Peers() {
			if v.Fingerprint != "" {
				out = append(out, cluster.PeerAddr{Name: v.Name, Addr: v.Addr, Fingerprint: v.Fingerprint})
			}
		}
	}
	return out
}

func (a *App) peerTrust(addr string) ([]string, string) {
	var pins []string
	name := ""
	if a.pki != nil {
		pins = append(pins, a.pki.Fingerprint(), a.pki.PeerIdentity().Fingerprint())
	}
	a.am.mu.Lock()
	if m := a.am.member; m != nil {
		pins = append(pins, m.Fingerprint)
	}
	a.am.mu.Unlock()
	for _, p := range a.peerIdentities() {
		if strings.TrimPrefix(p.Addr, "https://") == addr && p.Fingerprint != "" {
			pins = append(pins, p.Fingerprint)
			name = p.Name
		}
	}
	return pins, name
}

func (a *App) persistPeerIdentities() {
	// New authenticated announcements replace an address's former identity.
	byAddr := map[string]cluster.PeerAddr{}
	for _, p := range a.peerIdentities() {
		if p.Addr != "" && p.Fingerprint != "" {
			byAddr[p.Addr] = p
		}
	}
	var peers []cluster.PeerAddr
	for _, p := range byAddr {
		peers = append(peers, p)
	}
	if len(peers) == 0 {
		return
	}
	b, err := json.Marshal(peers)
	if err == nil {
		_ = writePeerAtomic(a.dataPath(peerIdentityFile), b)
	}
}

func writePeerAtomic(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".peer-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

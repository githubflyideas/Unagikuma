package cluster

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestFailoverAndTerms(t *testing.T) {
	p, err := LoadOrCreatePKI(t.TempDir(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var superseded atomic.Uint64
	old := newEnvWith(t, p, func(o *MasterOptions) {
		o.Term = func() uint64 { return 1 }
		o.Superseded = func(term uint64, _ string) { superseded.Store(term) }
	})
	standby := newEnvWith(t, p, func(o *MasterOptions) {
		o.Term = func() uint64 { return 2 }
		o.Standby = func(n string) bool { return n == "bj" }
		o.WriteState = func(w http.ResponseWriter) error {
			w.Header().Set(StateKeyHeader, "k3y")
			_, err := w.Write([]byte("STATE"))
			return err
		}
	})
	old.master.SetConfigs(map[string]*AgentConfig{"bj": {Serial: 1, Hash: "a", Masters: []string{standby.url}}})

	var terms, promoted atomic.Uint64
	var masters atomic.Value
	a, _ := old.agent(func(o *AgentOptions) {
		o.OnTerm = func(t uint64) { terms.Store(t) }
		o.OnMasters = func(l []string) { masters.Store(l) }
		o.OnPromote = func(t uint64) { promoted.Store(t) }
	})
	waitFor(t, "connected to the old master", func() bool { return a.Status().Connected && a.Term() == 1 })
	waitFor(t, "standby address learnt", func() bool {
		l, _ := masters.Load().([]string)
		return len(l) == 2 && l[1] == standby.url
	})

	// The master goes away: the agent moves on to the standby, whose
	// term is higher.
	old.hs.Close()
	old.master.Close()
	waitFor(t, "failed over", func() bool { return a.Status().Connected && a.Server() == standby.url && terms.Load() == 2 })

	// Standby state and promotion.
	var buf bytes.Buffer
	key, term, err := a.FetchState(t.Context(), &buf)
	if err != nil || key != "k3y" || term != 2 || buf.String() != "STATE" {
		t.Fatalf("state: %q %d %q %v", key, term, buf.String(), err)
	}
	if !standby.master.Promote("bj", 3) {
		t.Fatal("promote not sent")
	}
	waitFor(t, "promote received", func() bool { return promoted.Load() == 3 })

	// A replaced master that comes back is refused by agents that saw a
	// newer one, and learns it was replaced.
	back := newEnvWith(t, p, func(o *MasterOptions) {
		o.Term = func() uint64 { return 1 }
		o.Superseded = func(term uint64, _ string) { superseded.Store(term) }
	})
	b, _ := back.agent(func(o *AgentOptions) {
		o.Term = 2
		o.Servers = []string{standby.url}
	})
	waitFor(t, "old master told it was replaced", func() bool { return superseded.Load() == 2 })
	waitFor(t, "agent on the newer master", func() bool { return b.Status().Connected && b.Server() == standby.url })

	// Only standbys may fetch the state.
	if _, _, err := b.FetchState(t.Context(), &buf); err != nil {
		t.Fatal(err) // bj is a standby on that master
	}
}

func TestRetireNotice(t *testing.T) {
	p, _ := LoadOrCreatePKI(t.TempDir(), time.Now())
	other, _ := LoadOrCreatePKI(t.TempDir(), time.Now())
	n, err := p.RetireNotice(3, time.Now())
	if err != nil || p.CheckRetire(n) != nil {
		t.Fatalf("own notice: %v", err)
	}
	if other.CheckRetire(n) == nil {
		t.Fatal("a notice from another cluster was accepted")
	}
	n.Term = 4
	if p.CheckRetire(n) == nil {
		t.Fatal("an altered notice was accepted")
	}
	// Delivery over the pinned TLS.
	var got RetireNotice
	c := newEnvWith(t, p, nil)
	mux := c.hs.Handler.(*http.ServeMux)
	mux.HandleFunc("POST "+RetirePath, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
	})
	n, _ = p.RetireNotice(5, time.Now())
	if err := SendRetire(t.Context(), c.url, p.Fingerprint(), n); err != nil || got.Term != 5 {
		t.Fatalf("send: %v %+v", err, got)
	}
	if err := SendRetire(t.Context(), c.url, other.Fingerprint(), n); err == nil {
		t.Fatal("sent to a server of another cluster")
	}
}

func TestRetiredMasterRedirectsAuthenticatedFormerNode(t *testing.T) {
	p, err := LoadOrCreatePKI(t.TempDir(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	successor := newEnvWith(t, p, func(o *MasterOptions) { o.Term = func() uint64 { return 2 } })
	old := newEnvWith(t, p, func(o *MasterOptions) {
		o.Retired = func() bool { return true }
		o.Known = func(string) bool { return false }
		o.Successor = func() string { return successor.url }
	})
	a, _ := old.agent(nil)
	waitFor(t, "authenticated former node redirected despite local config removal", func() bool { return a.Status().Connected && a.Server() == successor.url })
}

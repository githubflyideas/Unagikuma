package server

import (
	"context"
	"io"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/githubflyideas/unagikuma/internal/probe"
)

// Suggesting an internet gateway (localnet.exit-target): the address
// pinged to show whether this host gets out to the internet. The best one
// is near: a public resolver of the host's own country answers faster and
// more reliably than one across an ocean, and some are blocked in places.
//
// What can go wrong is kept out by construction:
//   - only a fixed list of well-known public resolvers is measured, never
//     an address from the request, so the endpoint cannot be used to scan
//     or reach anything else;
//   - the checks are the product's own ping, or a TCP connection to port
//     53 where ping is filtered: no shell, no external program, no raw
//     socket;
//   - one run at a time, at most one every 20 seconds (a second request
//     gets the last result), each check bounded by a short timeout;
//   - the public address, read from one fixed HTTPS service, is only shown
//     and only accepted when it parses as an IP address;
//   - nothing is saved: the administrator picks one, and it is written as
//     the tunable it already is, with its usual validation.
//
// A traceroute would need raw sockets, so root or extra capabilities: the
// round-trip times of the candidates measure the same thing well enough.

type exitCandidate struct {
	Addr   string `json:"addr"`
	Label  string `json:"label"`
	Region string `json:"region"` // where it is local; "" = anycast everywhere
}

var exitCandidates = []exitCandidate{
	{"114.114.114.114", "114DNS", "CN"}, {"223.5.5.5", "AliDNS", "CN"}, {"119.29.29.29", "DNSPod", "CN"}, {"180.76.76.76", "Baidu DNS", "CN"},
	{"1.1.1.1", "Cloudflare", ""}, {"8.8.8.8", "Google", ""}, {"9.9.9.9", "Quad9", ""}, {"208.67.222.222", "OpenDNS", ""},
	{"77.88.8.8", "Yandex", "RU"}, {"168.126.63.1", "KT", "KR"}, {"101.101.101.101", "TWNIC", "TW"}, {"203.80.96.10", "HKBN", "HK"},
}

type exitMeasure struct {
	exitCandidate
	OK     bool    `json:"ok"`
	RTTms  float64 `json:"rtt_ms"`
	Loss   float64 `json:"loss"`
	Detail string  `json:"detail,omitempty"`
	Via    string  `json:"via"` // icmp, or tcp/53 when ping got no answer
}

var (
	exitProbe   = probe.Run // replaced in tests
	exitPublic  = "https://ifconfig.me/ip"
	exitMu      sync.Mutex
	exitLastAt  time.Time
	exitLastOut map[string]any
)

const exitMinGap = 20 * time.Second

func (s *Server) exitSuggest(w http.ResponseWriter, r *http.Request) (any, error) {
	exitMu.Lock()
	defer exitMu.Unlock()
	if exitLastOut != nil && time.Since(exitLastAt) < exitMinGap {
		return exitLastOut, nil
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	res := make([]exitMeasure, len(exitCandidates))
	var wg sync.WaitGroup
	for i, c := range exitCandidates {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pr := exitProbe(ctx, probe.Spec{Name: "exit-suggest", Type: "PING", Target: c.Addr, Timeout: 4 * time.Second,
				Opts: map[string]string{"count": "4", "interval": "250ms"}})
			via := "icmp"
			if pr.Vote != probe.VoteOK {
				// Ping is filtered on many networks: every public resolver
				// also answers DNS over TCP, so a connection to port 53
				// measures the same path.
				pr = exitProbe(ctx, probe.Spec{Name: "exit-suggest", Type: "TCP", Target: c.Addr + ":53", Timeout: 3 * time.Second})
				via = "tcp/53"
			}
			m := exitMeasure{exitCandidate: c, OK: pr.Vote == probe.VoteOK, Loss: 1, Via: via}
			if v, ok := pr.Metrics["ping.loss"]; ok {
				m.Loss = v
			} else if m.OK {
				m.Loss = 0 // TCP fallback: it answered
			}
			if m.OK {
				m.RTTms = float64(pr.Latency) / float64(time.Millisecond)
			} else {
				m.Detail = pr.Detail
			}
			res[i] = m
		}()
	}
	var public string
	wg.Add(1)
	go func() { defer wg.Done(); public = publicAddr(ctx) }()
	wg.Wait()
	// Answering first, then less loss, then the shortest round trip.
	sort.SliceStable(res, func(i, j int) bool {
		a, b := res[i], res[j]
		if a.OK != b.OK {
			return a.OK
		}
		if a.Loss != b.Loss {
			return a.Loss < b.Loss
		}
		return a.RTTms < b.RTTms
	})
	best := []string{} // a list, also when nothing answered
	for _, m := range res {
		if m.OK && len(best) < 2 {
			best = append(best, m.Addr)
		}
	}
	out := map[string]any{"candidates": res, "suggested": best, "public_addr": public, "at": time.Now().UTC()}
	exitLastAt, exitLastOut = time.Now(), out
	return out, nil
}

// publicAddr is this host's address as the internet sees it, or "".
func publicAddr(ctx context.Context) string {
	if exitPublic == "" {
		return ""
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, exitPublic, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "curl/8") // the service answers plain text to command-line clients
	cl := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := cl.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	a, err := netip.ParseAddr(strings.TrimSpace(string(b)))
	if err != nil || resp.StatusCode != http.StatusOK {
		return ""
	}
	return a.String()
}

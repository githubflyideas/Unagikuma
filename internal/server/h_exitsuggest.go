package server

import (
	"context"
	"io"
	"net/http"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/githubflyideas/unagikuma/internal/probe"
)

// Choosing the internet gateway (localnet.exit-target): the address pinged
// to show whether this host gets out to the internet. A near one is best.
// Two ways to find it, both started by an administrator:
//
//   - by country: the public resolvers of a country or region from a fixed
//     list, measured from this host;
//   - automatic: this host's public address and country (two fixed HTTPS
//     services, as `curl ifconfig.me` would), the route towards the
//     internet traced in Go (as traceroute would), and a ping of what it
//     finds. It proposes the fastest resolver of this host's country and
//     the first public router on the way out (the provider's edge).
//
// What can go wrong is kept out by construction:
//   - only addresses from the fixed list, or found on this host's own route,
//     are measured, never an address from the request: the endpoint cannot
//     be used to probe anything else;
//   - everything is the product's own Go code: no shell, no traceroute,
//     ping or curl program;
//   - the route is traced only where the process may open a raw ICMP
//     socket (root or CAP_NET_RAW); elsewhere that step is skipped and
//     said so;
//   - one run at a time, at most one every 20 seconds per kind of run,
//     every step bounded by a short timeout;
//   - what the HTTPS services answer is used only when it parses as an IP
//     address or a two-letter country code;
//   - nothing is saved: the administrator picks one and saves it as the
//     tunable it already is.

type exitCountry struct {
	Code  string   `json:"code"`
	Addrs []string `json:"addrs"`
	Label string   `json:"label"`
}

// exitCountries are well-known public resolvers by where they are local.
var exitCountries = []exitCountry{
	{"CN", []string{"114.114.114.114", "223.5.5.5", "119.29.29.29"}, "114DNS · AliDNS · DNSPod"},
	{"HK", []string{"203.80.96.10"}, "HKBN"},
	{"TW", []string{"101.101.101.101", "168.95.1.1"}, "Quad101 · HiNet"},
	{"KR", []string{"168.126.63.1", "164.124.101.2"}, "KT · LG U+"},
	{"RU", []string{"77.88.8.8", "77.88.8.1"}, "Yandex"},
	{"US", []string{"8.8.8.8", "208.67.222.222"}, "Google · OpenDNS"},
	{"DE", []string{"84.200.69.80", "84.200.70.40"}, "DNS.WATCH"},
	{"CH", []string{"9.9.9.9", "149.112.112.112"}, "Quad9"},
	{"FR", []string{"80.67.169.12", "80.67.169.40"}, "FDN"},
	{"GB", []string{"1.1.1.1", "8.8.8.8"}, "Cloudflare · Google (anycast)"},
	{"JP", []string{"1.1.1.1", "8.8.8.8"}, "Cloudflare · Google (anycast)"},
	{"ANY", []string{"1.1.1.1", "1.0.0.1", "94.140.14.14"}, "Cloudflare · AdGuard (anycast)"},
}

type exitMeasure struct {
	Addr   string  `json:"addr"`
	Label  string  `json:"label"`
	Source string  `json:"source"` // country code, or "route" for a router on the way out
	OK     bool    `json:"ok"`
	RTTms  float64 `json:"rtt_ms"`
	Loss   float64 `json:"loss"`
	Via    string  `json:"via"` // icmp, or tcp/53 when ping got no answer
}

var (
	exitProbe   = probe.Run   // replaced in tests
	exitTrace   = probe.Trace // replaced in tests
	exitPublic  = "https://ifconfig.me/ip"
	exitWhere   = "https://ifconfig.co/country-iso"
	exitMu      sync.Mutex
	exitLastAt  = map[string]time.Time{}
	exitLastOut = map[string]map[string]any{}
	countryCode = regexp.MustCompile(`^[A-Z]{2}$`)
)

const exitMinGap = 20 * time.Second

func (s *Server) exitCountries(w http.ResponseWriter, r *http.Request) (any, error) {
	return exitCountries, nil
}

func (s *Server) exitSuggest(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Mode    string `json:"mode"` // country | auto
		Country string `json:"country"`
	}
	if r.ContentLength != 0 {
		if err := decodeJSON(r, 4096, &req); err != nil { // two short fields
			return nil, err
		}
	}
	key := "auto"
	var country *exitCountry
	if req.Mode == "country" {
		for i := range exitCountries {
			if exitCountries[i].Code == req.Country {
				country = &exitCountries[i]
			}
		}
		if country == nil {
			return nil, Errorf(http.StatusBadRequest, CodeInvalid, "choose one of the listed countries")
		}
		key = "country:" + country.Code
	}
	exitMu.Lock()
	defer exitMu.Unlock()
	if out := exitLastOut[key]; out != nil && time.Since(exitLastAt[key]) < exitMinGap {
		return out, nil
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	var out map[string]any
	if country != nil {
		res := measureAll(ctx, targetsOf(*country))
		out = map[string]any{"mode": "country", "country": country.Code, "candidates": res, "suggested": best(res, 2)}
	} else {
		out = autoSuggest(ctx)
	}
	out["at"] = time.Now().UTC()
	exitLastAt[key], exitLastOut[key] = time.Now(), out
	return out, nil
}

func targetsOf(c exitCountry) []exitMeasure {
	var out []exitMeasure
	for _, a := range c.Addrs {
		out = append(out, exitMeasure{Addr: a, Label: c.Label, Source: c.Code})
	}
	return out
}

// autoSuggest finds this host's country and its way out, and proposes the
// fastest resolver of that country and the first public router.
func autoSuggest(ctx context.Context) map[string]any {
	var pub, cc string
	var hops []probe.Hop
	var traceErr error
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); pub = fetchText(ctx, exitPublic, parseIP) }()
	go func() { defer wg.Done(); cc = fetchText(ctx, exitWhere, parseCountry) }()
	go func() {
		defer wg.Done()
		hops, traceErr = exitTrace(ctx, netip.MustParseAddr("1.1.1.1"), 12, 900*time.Millisecond)
	}()
	wg.Wait()
	var cands []exitMeasure
	home := "ANY"
	for _, c := range exitCountries {
		if c.Code == cc {
			home = cc
			cands = append(cands, targetsOf(c)...)
		}
	}
	if home == "ANY" {
		cands = append(cands, targetsOf(exitCountries[len(exitCountries)-1])...)
	}
	// The first public routers on the way out: the provider's edge, as near
	// as an internet address gets.
	edge := 0
	for _, h := range hops {
		a, err := netip.ParseAddr(h.Addr)
		if err != nil || !publicAddr4(a) || edge == 2 {
			continue
		}
		cands = append(cands, exitMeasure{Addr: a.String(), Label: "hop " + strconv.Itoa(h.TTL), Source: "route"})
		edge++
	}
	res := measureAll(ctx, cands)
	// One from each source: the country's best resolver and the edge.
	sug := []string{}
	for _, src := range []string{home, "route"} {
		for _, m := range res {
			if m.OK && m.Source == src {
				sug = append(sug, m.Addr)
				break
			}
		}
	}
	if len(sug) < 2 {
		for _, a := range best(res, 2) {
			if len(sug) < 2 && !contains(sug, a) {
				sug = append(sug, a)
			}
		}
	}
	out := map[string]any{"mode": "auto", "public_addr": pub, "country": cc, "candidates": res, "suggested": sug, "hops": hops}
	if hops == nil {
		out["hops"] = []probe.Hop{}
	}
	if traceErr != nil {
		out["trace_error"] = traceErr.Error()
	}
	return out
}

// measureAll pings every candidate (TCP port 53 where ping gets no answer)
// and sorts them: answering first, then less loss, then shorter.
func measureAll(ctx context.Context, cands []exitMeasure) []exitMeasure {
	res := append([]exitMeasure(nil), cands...)
	var wg sync.WaitGroup
	for i := range res {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m := &res[i]
			pr := exitProbe(ctx, probe.Spec{Name: "exit-suggest", Type: "PING", Target: m.Addr, Timeout: 4 * time.Second,
				Opts: map[string]string{"count": "4", "interval": "250ms"}})
			m.Via = "icmp"
			if pr.Vote != probe.VoteOK && m.Source != "route" {
				// Ping is filtered on many networks: every public resolver
				// also answers DNS over TCP.
				pr = exitProbe(ctx, probe.Spec{Name: "exit-suggest", Type: "TCP", Target: m.Addr + ":53", Timeout: 3 * time.Second})
				m.Via = "tcp/53"
			}
			m.OK, m.Loss = pr.Vote == probe.VoteOK, 1
			if v, ok := pr.Metrics["ping.loss"]; ok {
				m.Loss = v
			} else if m.OK {
				m.Loss = 0
			}
			if m.OK {
				m.RTTms = float64(pr.Latency) / float64(time.Millisecond)
			}
		}()
	}
	wg.Wait()
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
	return res
}

func best(res []exitMeasure, n int) []string {
	out := []string{} // a list, also when nothing answered
	for _, m := range res {
		if m.OK && len(out) < n && !contains(out, m.Addr) {
			out = append(out, m.Addr)
		}
	}
	return out
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// publicAddr4 reports a public IPv4 address: not private, shared (CGNAT),
// loopback, link-local or reserved.
func publicAddr4(a netip.Addr) bool {
	if !a.Is4() || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsMulticast() || a.IsUnspecified() {
		return false
	}
	for _, p := range []string{"100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4"} {
		if netip.MustParsePrefix(p).Contains(a) {
			return false
		}
	}
	return true
}

func parseIP(s string) string {
	a, err := netip.ParseAddr(s)
	if err != nil {
		return ""
	}
	return a.String()
}

func parseCountry(s string) string {
	if !countryCode.MatchString(s) {
		return ""
	}
	return s
}

// fetchText reads a short plain-text answer of a fixed service and keeps
// it only when check accepts it.
func fetchText(ctx context.Context, url string, check func(string) string) string {
	if url == "" {
		return ""
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "curl/8") // these services answer plain text to command-line clients
	cl := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := cl.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	return check(strings.TrimSpace(string(b)))
}

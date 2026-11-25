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

// exitCity is a city whose nearby public resolvers can be measured. The
// list names cities only; Country is used to match automatic detection and
// is never shown.
type exitCity struct {
	ID      string   `json:"id"`
	Country string   `json:"-"`
	Addrs   []string `json:"addrs"`
	Label   string   `json:"label"`
	Anycast bool     `json:"anycast,omitempty"` // served from the nearest site of a global service
}

// anycastDNS answer from the nearest of their many sites.
var anycastDNS = []string{"1.1.1.1", "8.8.8.8", "9.9.9.9"}

const anycastLabel = "Cloudflare · Google · Quad9"

// exitCities are well-known public resolvers by the city they are run
// from, or anycast where a city has none of its own that answers anyone.
var exitCities = []exitCity{
	{"beijing", "CN", []string{"202.106.0.20", "219.141.136.10"}, "China Unicom · China Telecom", false},
	{"shanghai", "CN", []string{"202.96.209.133", "210.22.70.3"}, "China Telecom · China Unicom", false},
	{"guangzhou", "CN", []string{"202.96.128.86", "210.21.196.6"}, "China Telecom · China Unicom", false},
	{"shenzhen", "CN", []string{"119.29.29.29", "202.96.134.33"}, "DNSPod · China Telecom", false},
	{"hangzhou", "CN", []string{"223.5.5.5", "223.6.6.6"}, "AliDNS", false},
	{"nanjing", "CN", []string{"114.114.114.114", "114.114.115.115"}, "114DNS", false},
	{"chengdu", "CN", []string{"61.139.2.69", "119.6.6.6"}, "China Telecom · China Unicom", false},
	{"hongkong", "HK", []string{"203.80.96.10", "1.1.1.1"}, "HKBN · Cloudflare", false},
	{"taipei", "TW", []string{"168.95.1.1", "101.101.101.101"}, "HiNet · Quad101", false},
	{"seoul", "KR", []string{"168.126.63.1", "164.124.101.2", "210.220.163.82"}, "KT · LG U+ · SK", false},
	{"tokyo", "JP", anycastDNS, anycastLabel, true},
	{"singapore", "SG", anycastDNS, anycastLabel, true},
	{"jakarta", "ID", anycastDNS, anycastLabel, true},
	{"hochiminh", "VN", anycastDNS, anycastLabel, true},
	{"mumbai", "IN", anycastDNS, anycastLabel, true},
	{"dhaka", "BD", anycastDNS, anycastLabel, true},
	{"karachi", "PK", anycastDNS, anycastLabel, true},
	{"dubai", "AE", anycastDNS, anycastLabel, true},
	{"riyadh", "SA", anycastDNS, anycastLabel, true},
	{"cairo", "EG", anycastDNS, anycastLabel, true},
	{"moscow", "RU", []string{"77.88.8.8", "77.88.8.1"}, "Yandex", false},
	{"frankfurt", "DE", []string{"84.200.69.80", "84.200.70.40"}, "DNS.WATCH", false},
	{"zurich", "CH", []string{"9.9.9.9", "149.112.112.112"}, "Quad9", false},
	{"paris", "FR", []string{"80.67.169.12", "80.67.169.40"}, "FDN", false},
	{"london", "GB", anycastDNS, anycastLabel, true},
	{"madrid", "ES", anycastDNS, anycastLabel, true},
	{"lisbon", "PT", anycastDNS, anycastLabel, true},
	{"newyork", "US", []string{"8.8.8.8", "208.67.222.222"}, "Google · OpenDNS", false},
	{"losangeles", "US", []string{"8.8.8.8", "208.67.222.222"}, "Google · OpenDNS", false},
	{"mexicocity", "MX", anycastDNS, anycastLabel, true},
	{"saopaulo", "BR", anycastDNS, anycastLabel, true},
	{"buenosaires", "AR", anycastDNS, anycastLabel, true},
	{"sydney", "AU", anycastDNS, anycastLabel, true},
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

func (s *Server) exitCities(w http.ResponseWriter, r *http.Request) (any, error) {
	return exitCities, nil
}

func (s *Server) exitSuggest(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Mode string `json:"mode"` // city | auto
		City string `json:"city"`
	}
	if r.ContentLength != 0 {
		if err := decodeJSON(r, 4096, &req); err != nil { // two short fields
			return nil, err
		}
	}
	key := "auto"
	var city *exitCity
	if req.Mode == "city" {
		for i := range exitCities {
			if exitCities[i].ID == req.City {
				city = &exitCities[i]
			}
		}
		if city == nil {
			return nil, Errorf(http.StatusBadRequest, CodeInvalid, "choose one of the listed cities")
		}
		key = "city:" + city.ID
	}
	exitMu.Lock()
	defer exitMu.Unlock()
	if out := exitLastOut[key]; out != nil && time.Since(exitLastAt[key]) < exitMinGap {
		return out, nil
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	var out map[string]any
	if city != nil {
		res := measureAll(ctx, targetsOf(*city))
		out = map[string]any{"mode": "city", "city": city.ID, "candidates": res, "suggested": best(res, 2)}
	} else {
		out = autoSuggest(ctx)
	}
	out["at"] = time.Now().UTC()
	exitLastAt[key], exitLastOut[key] = time.Now(), out
	return out, nil
}

func targetsOf(c exitCity) []exitMeasure {
	var out []exitMeasure
	for _, a := range c.Addrs {
		out = append(out, exitMeasure{Addr: a, Label: c.Label, Source: c.ID})
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
	// The resolvers of the cities in this host's country, each address
	// once; anycast ones where the country has none listed.
	var cands []exitMeasure
	seen := map[string]bool{}
	for _, c := range exitCities {
		if c.Country != cc {
			continue
		}
		for _, m := range targetsOf(c) {
			if !seen[m.Addr] {
				seen[m.Addr] = true
				cands = append(cands, m)
			}
		}
	}
	if len(cands) == 0 {
		for _, a := range anycastDNS {
			cands = append(cands, exitMeasure{Addr: a, Label: anycastLabel, Source: "anycast"})
		}
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
	// One of each kind: the best nearby resolver and the edge.
	sug := []string{}
	for _, route := range []bool{false, true} {
		for _, m := range res {
			if m.OK && (m.Source == "route") == route {
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

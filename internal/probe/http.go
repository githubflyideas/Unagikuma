package probe

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// httpBodyCap bounds the body read for assertions: a hostile or runaway
// endpoint must not drive an unbounded allocation.
const httpBodyCap = 1 << 20

var httpOpts = map[string]bool{
	"method": true, "body": true, "expect": true, "keyword": true, "keyword-regex": true, "absent": true,
	"json-path": true, "json-expect": true, "json-expect-num": true, "no-follow": true, "insecure": true,
	"host": true, "basic": true, "bearer": true, "proxy": true, "cert-days": true, "cert-critical-days": true,
	"user-agent": true,
}

func validateHTTP(target string, o opts) error {
	if err := rejectUnknown(o, httpOpts, "header."); err != nil {
		return err
	}
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("target must be an http:// or https:// URL")
	}
	if _, err := parseStatus(o.str("expect", "")); err != nil {
		return err
	}
	if _, err := regexpOpt(o, "keyword-regex"); err != nil {
		return err
	}
	for _, k := range []string{"no-follow", "insecure"} {
		if _, err := o.boolOpt(k, false); err != nil {
			return err
		}
	}
	if o.has("json-expect-num") {
		if _, err := parseNumExpect(o.str("json-expect-num", "")); err != nil {
			return err
		}
	}
	if (o.has("json-expect") || o.has("json-expect-num")) && !o.has("json-path") {
		return errors.New("json-expect needs json-path")
	}
	if o.has("json-path") {
		if _, err := parseJSONPath(o.str("json-path", "")); err != nil {
			return err
		}
	}
	if b := o.str("basic", ""); b != "" && !strings.Contains(b, ":") {
		return errors.New(`basic must be "user:password"`)
	}
	if p := o.str("proxy", ""); p != "" {
		if pu, err := url.Parse(p); err != nil || pu.Host == "" {
			return errors.New("proxy must be a URL like http://proxy:3128")
		}
	}
	for _, k := range []string{"cert-days", "cert-critical-days"} {
		if _, err := o.intOpt(k, 0, 0, 3650); err != nil {
			return err
		}
	}
	return nil
}

func runHTTP(ctx context.Context, s Spec) Result {
	o := opts(s.Opts)
	u, _ := url.Parse(s.Target)
	method := strings.ToUpper(o.str("method", http.MethodGet))
	var body io.Reader
	if b := o.str("body", ""); b != "" {
		body = strings.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.Target, body)
	if err != nil {
		return abstain(ReasonConfig, "cannot build the request: %v", err)
	}
	for k, v := range o {
		if name, ok := strings.CutPrefix(k, "header."); ok {
			req.Header.Set(name, v)
		}
	}
	req.Header.Set("User-Agent", o.str("user-agent", "unagikuma"))
	if h := o.str("host", ""); h != "" {
		req.Host = h
	}
	if b := o.str("basic", ""); b != "" {
		user, pass, _ := strings.Cut(b, ":")
		req.SetBasicAuth(user, pass)
	}
	if t := o.str("bearer", ""); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}

	// The transport may dial from its own goroutines (redirects); peer is
	// read after the request returns.
	var peerMu sync.Mutex
	var peer string
	setPeer := func(v string) { peerMu.Lock(); peer = v; peerMu.Unlock() }
	getPeer := func() string { peerMu.Lock(); defer peerMu.Unlock(); return peer }
	timeout := effTimeout(s)
	dialer := &net.Dialer{Timeout: timeout}
	tr := &http.Transport{
		DisableKeepAlives:     true, // every probe opens a fresh connection
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: o.on("insecure"), //nolint:gosec // opt-in per monitor
			ServerName:         serverName(o.str("host", ""), u.Hostname()),
		},
		DialContext: func(dctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ip, err := resolve(dctx, host, o)
			if err != nil {
				return nil, err
			}
			c, err := dialer.DialContext(dctx, "tcp", net.JoinHostPort(ip.String(), port))
			if c != nil {
				setPeer(c.RemoteAddr().String())
			} else {
				setPeer(net.JoinHostPort(ip.String(), port))
			}
			return c, err
		},
	}
	// The environment's proxy is never used implicitly: a monitor checks
	// the path it names.
	if p := o.str("proxy", ""); p != "" {
		if pu, err := url.Parse(p); err == nil {
			tr.Proxy = http.ProxyURL(pu)
		}
	}
	client := &http.Client{Transport: tr, Timeout: timeout}
	if o.on("no-follow") {
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	} else {
		client.CheckRedirect = func(r *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("more than 10 redirects")
			}
			return nil
		}
	}
	start := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(start)
	if err != nil {
		r := httpError(err, o)
		if p := getPeer(); p != "" {
			r.Evidence = &Evidence{Peer: p}
		} else if r.Reason == ReasonTimeout && o.str("proxy", "") == "" {
			// Out of time before the name had an address: the resolver
			// never answered (the request itself never started).
			if _, perr := netip.ParseAddr(strings.Trim(u.Hostname(), "[]")); perr != nil {
				r = fail(ReasonDNSResolve, "cannot resolve %s: no answer within the timeout", u.Hostname())
			}
		}
		return r
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, httpBodyCap))

	r := ok("HTTP %d", resp.StatusCode)
	r.Latency = latency
	r.Metrics = map[string]float64{"http.status": float64(resp.StatusCode), "latency_ms": ms(latency)}
	r.Evidence = newEvidence(getPeer(), requestEvidence(req), responseEvidence(resp, data))
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		if res, done := certDays(r, o, resp.TLS.PeerCertificates[0], start); done {
			return res
		}
		r = certAnnotate(r, o, resp.TLS.PeerCertificates[0], start)
	}
	sm, _ := parseStatus(o.str("expect", ""))
	if !sm.match(resp.StatusCode) {
		return failKeep(r, ReasonHTTPStatus, "HTTP %d, expected %s", resp.StatusCode, sm.expr)
	}
	if kw := o.str("keyword", ""); kw != "" && !strings.Contains(string(data), kw) {
		return failKeep(r, ReasonAssertFailed, "keyword %q not found in the response", kw)
	}
	if kw := o.str("absent", ""); kw != "" && strings.Contains(string(data), kw) {
		return failKeep(r, ReasonAssertFailed, "unwanted text %q found in the response", kw)
	}
	if re, _ := regexpOpt(o, "keyword-regex"); re != nil && !re.Match(data) {
		return failKeep(r, ReasonAssertFailed, "response does not match %s", re)
	}
	if p := o.str("json-path", ""); p != "" {
		if msg, good := checkJSON(data, p, o); !good {
			return failKeep(r, ReasonAssertFailed, "%s", msg)
		}
	}
	return r
}

func serverName(hostOpt, urlHost string) string {
	if hostOpt != "" {
		if h, _, err := net.SplitHostPort(hostOpt); err == nil {
			return h
		}
		return hostOpt
	}
	return urlHost
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// failKeep turns a result into a failure while keeping evidence and metrics.
func failKeep(r Result, reason, format string, args ...any) Result {
	r.Vote, r.Reason, r.Detail = VoteFail, reason, oneLine(fmt.Sprintf(format, args...))
	return r
}

func httpError(err error, o opts) Result {
	var ce *tls.CertificateVerificationError
	var ue x509.UnknownAuthorityError
	var he x509.HostnameError
	var ie x509.CertificateInvalidError
	switch {
	case errors.As(err, &ue):
		return fail(ReasonTLS, "certificate rejected: not issued by a trusted authority (self-signed?)")
	case errors.As(err, &he):
		return fail(ReasonTLS, "certificate rejected: issued for another name (%s)", certNames(he.Certificate))
	case errors.As(err, &ie):
		switch ie.Reason {
		case x509.Expired:
			if c := ie.Cert; c != nil && time.Now().Before(c.NotBefore) {
				return fail(ReasonTLS, "certificate rejected: not valid yet")
			}
			return fail(ReasonTLS, "certificate rejected: expired")
		default:
			return fail(ReasonTLS, "certificate rejected: not valid for this use")
		}
	case errors.As(err, &ce):
		return fail(ReasonTLS, "certificate rejected")
	case strings.Contains(err.Error(), "tls:"), strings.Contains(err.Error(), "remote error"):
		return fail(ReasonTLS, "TLS handshake failed (no common protocol or the server refused)")
	case strings.Contains(err.Error(), "redirects"):
		return fail(ReasonProtoError, "too many redirects (more than 10)")
	}
	return classify(err, false, o)
}

// certNames lists the names a certificate is for, briefly.
func certNames(c *x509.Certificate) string {
	if c == nil {
		return "?"
	}
	names := c.DNSNames
	if len(names) == 0 && c.Subject.CommonName != "" {
		names = []string{c.Subject.CommonName}
	}
	for _, ip := range c.IPAddresses {
		names = append(names, ip.String())
	}
	if len(names) > 3 {
		names = append(names[:3], "…")
	}
	if len(names) == 0 {
		return "?"
	}
	return strings.Join(names, ", ")
}

// certDays fails the check when the certificate is inside the critical
// window; certAnnotate marks it degraded inside the warning window.
func certDays(r Result, o opts, leaf *x509.Certificate, now time.Time) (Result, bool) {
	days := leaf.NotAfter.Sub(now).Hours() / 24
	r.Metrics["cert.days"] = days
	if days < 0 {
		// Only reachable with insecure=yes (verification would refuse it):
		// skipping verification must not hide an expired certificate.
		return failKeep(r, ReasonTLS, "certificate expired %.0f days ago", -days), true
	}
	if crit, _ := o.intOpt("cert-critical-days", 0, 0, 3650); crit > 0 && days < float64(crit) {
		return failKeep(r, ReasonTLS, "certificate expires in %.0f days (critical below %d)", days, crit), true
	}
	return r, false
}

func certAnnotate(r Result, o opts, leaf *x509.Certificate, now time.Time) Result {
	days := leaf.NotAfter.Sub(now).Hours() / 24
	r.Metrics["cert.days"] = days
	if warn, _ := o.intOpt("cert-days", 0, 0, 3650); warn > 0 && days < float64(warn) {
		r.Degraded = true
		r.Detail = oneLine(fmt.Sprintf("%s; certificate expires in %.0f days", r.Detail, days))
	}
	return r
}

// requestEvidence records the request without credentials.
func requestEvidence(req *http.Request) []byte {
	var b strings.Builder
	u := *req.URL
	u.User = nil
	fmt.Fprintf(&b, "%s %s\r\n", req.Method, u.RequestURI())
	host := req.Host
	if host == "" {
		host = u.Host
	}
	fmt.Fprintf(&b, "Host: %s\r\n", host)
	for name, vals := range req.Header {
		if isSecretHeader(name) {
			fmt.Fprintf(&b, "%s: ***\r\n", name)
			continue
		}
		for _, v := range vals {
			fmt.Fprintf(&b, "%s: %s\r\n", name, v)
		}
	}
	return []byte(b.String())
}

func isSecretHeader(name string) bool {
	n := strings.ToLower(name)
	return n == "authorization" || n == "cookie" || n == "proxy-authorization" || strings.Contains(n, "token") || strings.Contains(n, "key") || strings.Contains(n, "secret")
}

func responseEvidence(resp *http.Response, body []byte) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\r\n", resp.Proto, resp.Status)
	for name, vals := range resp.Header {
		if strings.EqualFold(name, "Set-Cookie") {
			fmt.Fprintf(&b, "%s: ***\r\n", name)
			continue
		}
		for _, v := range vals {
			fmt.Fprintf(&b, "%s: %s\r\n", name, v)
		}
	}
	b.WriteString("\r\n")
	b.Write(body)
	return []byte(b.String())
}

// statusMatcher matches status codes like "200,301-399".
type statusMatcher struct {
	expr   string
	ranges [][2]int
}

func parseStatus(s string) (statusMatcher, error) {
	if s == "" {
		s = "200-399"
	}
	m := statusMatcher{expr: s}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		lo, hi, isRange := strings.Cut(part, "-")
		a, err1 := strconv.Atoi(lo)
		b := a
		var err2 error
		if isRange {
			b, err2 = strconv.Atoi(hi)
		}
		if err1 != nil || err2 != nil || a < 100 || b > 599 || a > b {
			return m, fmt.Errorf("expect: invalid status %q (use 200 or 200-299)", part)
		}
		m.ranges = append(m.ranges, [2]int{a, b})
	}
	return m, nil
}

func (m statusMatcher) match(code int) bool {
	for _, r := range m.ranges {
		if code >= r[0] && code <= r[1] {
			return true
		}
	}
	return false
}

func regexpOpt(o opts, k string) (*regexp.Regexp, error) {
	v := o.str(k, "")
	if v == "" {
		return nil, nil
	}
	re, err := regexp.Compile(v)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", k, err)
	}
	return re, nil
}

// numExpect is a numeric comparison such as ">100".
type numExpect struct {
	op  string
	val float64
}

func parseNumExpect(s string) (numExpect, error) {
	s = strings.TrimSpace(s)
	for _, op := range []string{">=", "<=", "!=", "==", ">", "<", "="} {
		if rest, ok := strings.CutPrefix(s, op); ok {
			f, err := strconv.ParseFloat(strings.TrimSpace(rest), 64)
			if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
				return numExpect{}, fmt.Errorf("invalid number in %q", s)
			}
			if op == "=" {
				op = "=="
			}
			return numExpect{op, f}, nil
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return numExpect{}, fmt.Errorf("want a comparison like >100, got %q", s)
	}
	return numExpect{"==", f}, nil
}

func (n numExpect) match(x float64) bool {
	switch n.op {
	case ">":
		return x > n.val
	case "<":
		return x < n.val
	case ">=":
		return x >= n.val
	case "<=":
		return x <= n.val
	case "!=":
		return x != n.val
	}
	return x == n.val
}

func (n numExpect) String() string { return n.op + strconv.FormatFloat(n.val, 'f', -1, 64) }

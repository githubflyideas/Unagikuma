package probe

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/keywords"
)

// Run executes one check. It never panics and returns within the spec's
// timeout plus a small slack.
// CheckTypes are the check types this build runs. Nodes announce them
// when they connect, so a master never hands a node a check it lacks.
var CheckTypes = []string{"HTTP", "TCP", "PING", "UDP", "EXEC", "DOCKER", "DOMAIN"}

func Run(ctx context.Context, s Spec) (r Result) {
	defer func() {
		if v := recover(); v != nil {
			r = abstain(ReasonInternal, "probe crashed: %v", v)
		}
		if r.Depth == "" {
			r.Depth = DepthOf(s)
		}
		scrub(&r, s.Secrets)
	}()
	if err := Validate(s); err != nil {
		return abstain(ReasonConfig, "%v", err)
	}
	ctx, cancel := context.WithTimeout(ctx, effTimeout(s))
	defer cancel()
	switch strings.ToUpper(s.Type) {
	case "HTTP":
		return runHTTP(ctx, s)
	case "TCP":
		return runTCP(ctx, s)
	case "PING":
		return runPing(ctx, s)
	case "UDP":
		return runUDP(ctx, s)
	case "EXEC":
		return runExec(ctx, s)
	case "DOCKER":
		return runDocker(ctx, s)
	case "DOMAIN":
		return runDomain(ctx, s)
	}
	return abstain(ReasonConfig, "unknown check type %q", s.Type)
}

// Validate checks a spec's type-specific options statically.
func Validate(s Spec) error {
	o := opts(s.Opts)
	switch strings.ToUpper(s.Type) {
	case "HTTP":
		return validateHTTP(s.Target, o)
	case "TCP":
		return validateTCP(s.Target, o)
	case "PING":
		return validatePing(o)
	case "UDP":
		return validateUDP(s, o)
	case "EXEC":
		return validateExec(s.Target, o)
	case "DOCKER":
		return validateDocker(s.Target, o)
	case "DOMAIN":
		return validateDomain(s.Target, o)
	}
	return fmt.Errorf("unknown check type %q", s.Type)
}

// DepthOf is the honest detection depth this build delivers for a spec.
func DepthOf(s Spec) keywords.Depth {
	o := opts(s.Opts)
	switch strings.ToUpper(s.Type) {
	case "HTTP":
		return keywords.L3
	case "TCP":
		if o.has("expect") || o.has("expect-hex") || o.has("expect-regex") || o.on("tls") || o.has("proto") {
			return keywords.L3
		}
		return keywords.L2
	case "DOCKER":
		return keywords.L3
	case "PING":
		return keywords.L2
	case "EXEC":
		return keywords.L3
	case "UDP":
		plan, err := planUDP(s)
		if err != nil {
			return keywords.L1
		}
		return plan.depth
	}
	return keywords.L1
}

// built lists the UDP templates implemented in this build.
var built = map[string]bool{
	"echo": true, "dns": true, "ntp": true, "snmp": true, "snmptrap": true, "time": true,
	"tftp": true, "sunrpc": true, "syslog": true, "l1": true, "raw": true,
}

// Built reports whether a template is implemented in this build; keywords
// whose template is not fall back to the L1 delivery check.
func Built(template string) bool { return built[template] }

// effTimeout is the probe timeout (default 5s).
func effTimeout(s Spec) time.Duration {
	if s.Timeout > 0 {
		return s.Timeout
	}
	return 5 * time.Second
}

func result(v Vote, reason, format string, args ...any) Result {
	return Result{Vote: v, Reason: reason, Detail: oneLine(fmt.Sprintf(format, args...))}
}

func ok(format string, args ...any) Result { return result(VoteOK, ReasonOK, format, args...) }
func fail(reason, format string, args ...any) Result {
	return result(VoteFail, reason, format, args...)
}
func abstain(reason, format string, args ...any) Result {
	return result(VoteAbstain, reason, format, args...)
}

// oneLine keeps details short and single-line for lists and messages.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 300 {
		s = s[:297] + "..."
	}
	return s
}

// opts reads probe options.
type opts map[string]string

func (o opts) has(k string) bool { _, ok := o[k]; return ok }

func (o opts) str(k, def string) string {
	if v, ok := o[k]; ok {
		return v
	}
	return def
}

func (o opts) on(k string) bool {
	b, _ := o.boolOpt(k, false)
	return b
}

func (o opts) boolOpt(k string, def bool) (bool, error) {
	v, ok := o[k]
	if !ok {
		return def, nil
	}
	switch strings.ToLower(v) {
	case "yes", "true", "on", "1":
		return true, nil
	case "no", "false", "off", "0":
		return false, nil
	}
	return def, fmt.Errorf("%s: want yes or no, got %q", k, v)
}

func (o opts) intOpt(k string, def, lo, hi int) (int, error) {
	v, ok := o[k]
	if !ok {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi {
		return def, fmt.Errorf("%s: want an integer between %d and %d", k, lo, hi)
	}
	return n, nil
}

func (o opts) durOpt(k string, def time.Duration) (time.Duration, error) {
	v, ok := o[k]
	if !ok {
		return def, nil
	}
	d, err := parseDuration(v)
	if err != nil || d < 0 {
		return def, fmt.Errorf("%s: invalid duration %q", k, v)
	}
	return d, nil
}

// percent parses "20%" or "0.2" into a ratio.
func (o opts) percent(k string, def float64) (float64, error) {
	v, ok := o[k]
	if !ok {
		return def, nil
	}
	s := strings.TrimSpace(v)
	scale := 1.0
	if strings.HasSuffix(s, "%") {
		s, scale = strings.TrimSuffix(s, "%"), 100
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || !(f >= 0 && f/scale <= 1) { // also refuses NaN and Inf
		return def, fmt.Errorf("%s: want a percentage like 20%%", k)
	}
	return f / scale, nil
}

// parseDuration accepts Go durations, a d unit and bare seconds.
func parseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if n, err := strconv.Atoi(s); err == nil {
		return time.Duration(n) * time.Second, nil
	}
	if i := strings.Index(s, "d"); i > 0 {
		days, err := strconv.Atoi(s[:i])
		if err != nil {
			return 0, err
		}
		rest := time.Duration(0)
		if s[i+1:] != "" {
			if rest, err = time.ParseDuration(s[i+1:]); err != nil {
				return 0, err
			}
		}
		return time.Duration(days)*24*time.Hour + rest, nil
	}
	return time.ParseDuration(s)
}

// commonOpts are accepted by every type.
var commonOpts = map[string]bool{"family": true, "allow-metadata": true, "on-filtered": true, "authorized": true}

// rejectUnknown reports options the type does not know. prefixes allow
// families like header.<name>.
func rejectUnknown(o opts, allowed map[string]bool, prefixes ...string) error {
	for k := range o {
		if allowed[k] || commonOpts[k] {
			continue
		}
		known := false
		for _, p := range prefixes {
			if strings.HasPrefix(k, p) && len(k) > len(p) {
				known = true
			}
		}
		if !known {
			return fmt.Errorf("unknown option %s", k)
		}
	}
	if f := o.str("family", ""); f != "" && f != "4" && f != "6" {
		return fmt.Errorf("family must be 4 or 6")
	}
	if v := o.str("on-filtered", "fail"); v != "fail" && v != "abstain" {
		return fmt.Errorf("on-filtered must be fail or abstain")
	}
	for _, k := range []string{"allow-metadata", "authorized"} {
		if _, err := o.boolOpt(k, false); err != nil {
			return err
		}
	}
	return nil
}

// splitHostPort accepts "host:port" and "[v6]:port".
func splitHostPort(target string) (string, int, error) {
	h, p, err := net.SplitHostPort(target)
	if err != nil {
		return "", 0, fmt.Errorf("target must be host:port")
	}
	n, err := strconv.Atoi(p)
	if err != nil || n < 1 || n > 65535 {
		return "", 0, fmt.Errorf("invalid port %q", p)
	}
	return h, n, nil
}

// evidenceCap is how much of each direction is kept for the packet viewer.
const evidenceCap = 512

func newEvidence(peer string, req, resp []byte) *Evidence {
	cut := func(b []byte) []byte {
		if len(b) > evidenceCap {
			b = b[:evidenceCap]
		}
		return append([]byte(nil), b...)
	}
	return &Evidence{Peer: peer, Request: cut(req), Response: cut(resp)}
}

// scrub replaces every secret value in what a result records.
func scrub(r *Result, secrets []string) {
	var olds []string
	for _, v := range secrets {
		if len(v) >= 3 { // shorter values would mangle unrelated text
			olds = append(olds, v, "***")
			if q := url.QueryEscape(v); q != v {
				olds = append(olds, q, "***")
			}
			if p := url.PathEscape(v); p != v {
				olds = append(olds, p, "***")
			}
		}
	}
	if len(olds) == 0 {
		return
	}
	rep := strings.NewReplacer(olds...)
	r.Detail = rep.Replace(r.Detail)
	if e := r.Evidence; e != nil {
		e.Peer = rep.Replace(e.Peer)
		e.Request = []byte(rep.Replace(string(e.Request)))
		e.Response = []byte(rep.Replace(string(e.Response)))
		for i := range e.Decoded {
			e.Decoded[i].Value = rep.Replace(e.Decoded[i].Value)
		}
	}
}

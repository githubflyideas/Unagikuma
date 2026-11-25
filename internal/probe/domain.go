package probe

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// DOMAIN checks when a domain's registration expires, from the registry's
// RDAP service (found through the IANA bootstrap file), or over WHOIS for
// registries without RDAP. A domain inside critical-days fails, inside
// days it is degraded. When no registry answers, the check abstains: a
// lookup problem says nothing about the domain.

var domainOpts = map[string]bool{"days": true, "critical-days": true, "rdap": true, "whois": true}

// Where registries are found; replaceable in tests.
var (
	rdapBootstrapURL = "https://data.iana.org/rdap/dns.json"
	ianaWhois        = "whois.iana.org:43"
)

var domainRe = regexp.MustCompile(`^(?i)[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

func validateDomain(target string, o opts) error {
	if err := rejectUnknown(o, domainOpts); err != nil {
		return err
	}
	if !domainRe.MatchString(strings.TrimSuffix(target, ".")) {
		return fmt.Errorf("target must be a domain name such as example.com, not %q", target)
	}
	warn, err := o.intOpt("days", 30, 0, 3650)
	if err != nil {
		return err
	}
	crit, err := o.intOpt("critical-days", 7, 0, 3650)
	if err != nil {
		return err
	}
	if crit > warn && warn > 0 {
		return errors.New("critical-days must not be more than days")
	}
	return nil
}

func runDomain(ctx context.Context, s Spec) Result {
	o := opts(s.Opts)
	name := strings.ToLower(strings.TrimSuffix(s.Target, "."))
	exp, source, err := domainExpiry(ctx, name, o)
	if err != nil {
		r := abstain(ReasonRegistry, "%v", err)
		r.Evidence = &Evidence{Peer: source}
		return r
	}
	days := time.Until(exp).Hours() / 24
	warn, _ := o.intOpt("days", 30, 0, 3650)
	crit, _ := o.intOpt("critical-days", 7, 0, 3650)
	r := ok("registration expires %s (in %.0f days)", exp.UTC().Format("2006-01-02"), days)
	switch {
	case days < 0:
		r = fail(ReasonExpiring, "registration expired %s (%.0f days ago)", exp.UTC().Format("2006-01-02"), -days)
	case crit > 0 && days < float64(crit):
		r = fail(ReasonExpiring, "registration expires %s, in %.0f days (critical below %d)", exp.UTC().Format("2006-01-02"), days, crit)
	case warn > 0 && days < float64(warn):
		r.Degraded = true
		r.Detail = oneLine(fmt.Sprintf("registration expires %s, in %.0f days (warning below %d)", exp.UTC().Format("2006-01-02"), days, warn))
	}
	r.Metrics = map[string]float64{"domain.days": days}
	r.Evidence = &Evidence{Peer: source}
	return r
}

// domainExpiry asks RDAP first, then WHOIS.
func domainExpiry(ctx context.Context, name string, o opts) (time.Time, string, error) {
	var errs []string
	if !o.has("whois") {
		bases := []string{o.str("rdap", "")}
		if bases[0] == "" {
			var err error
			if bases, err = rdapServers(ctx, name); err != nil {
				errs = append(errs, err.Error())
			}
		}
		for _, base := range bases {
			exp, err := rdapExpiry(ctx, base, name)
			if err == nil {
				return exp, base, nil
			}
			errs = append(errs, err.Error())
		}
	}
	if o.has("rdap") {
		return time.Time{}, o.str("rdap", ""), errors.New(strings.Join(errs, "; "))
	}
	exp, server, err := whoisExpiry(ctx, name, o.str("whois", ""))
	if err == nil {
		return exp, "whois://" + server, nil
	}
	errs = append(errs, err.Error())
	return time.Time{}, server, errors.New(strings.Join(errs, "; "))
}

var rdapClient = &http.Client{Timeout: 20 * time.Second}

// The bootstrap file changes rarely; it is fetched at most once a day.
var bootstrap struct {
	sync.Mutex
	at       time.Time
	services map[string][]string // TLD → RDAP base URLs
}

func rdapServers(ctx context.Context, name string) ([]string, error) {
	bootstrap.Lock()
	defer bootstrap.Unlock()
	if bootstrap.services == nil || time.Since(bootstrap.at) > 24*time.Hour {
		var doc struct {
			Services [][][]string `json:"services"`
		}
		if err := getJSON(ctx, rdapBootstrapURL, &doc); err != nil {
			if bootstrap.services == nil {
				return nil, fmt.Errorf("RDAP bootstrap: %v", err)
			}
		} else {
			m := map[string][]string{}
			for _, svc := range doc.Services {
				if len(svc) != 2 {
					continue
				}
				for _, tld := range svc[0] {
					m[strings.ToLower(tld)] = svc[1]
				}
			}
			bootstrap.services, bootstrap.at = m, time.Now()
		}
	}
	labels := strings.Split(name, ".")
	for i := range labels {
		if s, ok := bootstrap.services[strings.Join(labels[i:], ".")]; ok {
			return s, nil
		}
	}
	return nil, fmt.Errorf("no RDAP service for .%s", labels[len(labels)-1])
}

func getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/rdap+json, application/json")
	req.Header.Set("User-Agent", "unagikuma")
	resp, err := rdapClient.Do(req)
	if err != nil {
		return rootErr(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errors.New("the registry does not know this domain")
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
}

func rdapExpiry(ctx context.Context, base, name string) (time.Time, error) {
	var doc struct {
		Events []struct {
			Action string `json:"eventAction"`
			Date   string `json:"eventDate"`
		} `json:"events"`
	}
	if err := getJSON(ctx, strings.TrimRight(base, "/")+"/domain/"+name, &doc); err != nil {
		return time.Time{}, fmt.Errorf("RDAP %s: %v", base, err)
	}
	for _, e := range doc.Events {
		if strings.EqualFold(e.Action, "expiration") {
			if t, ok := parseExpiry(e.Date); ok {
				return t, nil
			}
		}
	}
	return time.Time{}, fmt.Errorf("RDAP %s: no expiration date in the reply", base)
}

var (
	whoisReferRe  = regexp.MustCompile(`(?im)^\s*(?:refer|whois):\s*(\S+)`)
	whoisExpiryRe = regexp.MustCompile(`(?im)^\s*(?:registry expiry date|registrar registration expiration date|expiration date|expiry date|expiration time|expires on|expire date|paid-till|expires)\s*:\s*(.+?)\s*$`)
)

// whoisExpiry asks IANA which server holds the TLD, then that server.
func whoisExpiry(ctx context.Context, name, server string) (time.Time, string, error) {
	if server == "" {
		tld := name[strings.LastIndex(name, ".")+1:]
		reply, err := whoisQuery(ctx, ianaWhois, tld)
		if err != nil {
			return time.Time{}, ianaWhois, fmt.Errorf("WHOIS %s: %v", ianaWhois, err)
		}
		m := whoisReferRe.FindStringSubmatch(reply)
		if m == nil {
			return time.Time{}, ianaWhois, fmt.Errorf("no WHOIS server for .%s", tld)
		}
		server = m[1]
	}
	if _, _, err := net.SplitHostPort(server); err != nil {
		server = net.JoinHostPort(server, "43")
	}
	reply, err := whoisQuery(ctx, server, name)
	if err != nil {
		return time.Time{}, server, fmt.Errorf("WHOIS %s: %v", server, err)
	}
	for _, m := range whoisExpiryRe.FindAllStringSubmatch(reply, -1) {
		if t, ok := parseExpiry(m[1]); ok {
			return t, server, nil
		}
	}
	return time.Time{}, server, fmt.Errorf("WHOIS %s: no expiry date in the reply", server)
}

func whoisQuery(ctx context.Context, server, q string) (string, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", server)
	if err != nil {
		return "", rootErr(err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	if _, err := io.WriteString(conn, q+"\r\n"); err != nil {
		return "", err
	}
	var b strings.Builder
	sc := bufio.NewScanner(io.LimitReader(conn, 1<<20))
	for sc.Scan() {
		b.WriteString(sc.Text() + "\n")
	}
	return b.String(), nil
}

var expiryLayouts = []string{time.RFC3339, "2006-01-02T15:04:05Z", "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02 15:04:05 MST",
	"2006-01-02", "02-Jan-2006", "2006.01.02", "2006/01/02", "02.01.2006", "January 2 2006", "2006-01-02 15:04:05-07"}

func parseExpiry(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, l := range expiryLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, true
		}
		if len(s) > len(l) {
			if t, err := time.Parse(l, s[:len(l)]); err == nil {
				return t, true
			}
		}
	}
	return time.Time{}, false
}

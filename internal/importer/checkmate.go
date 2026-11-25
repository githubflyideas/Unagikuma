package importer

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/githubflyideas/unagikuma/internal/config"
)

// Checkmate reads monitors.json from bluewave-labs/Checkmate's settings export.
// Format observations and the exact upstream revision are in testdata/ADAPTERS.md.
func Checkmate(data []byte, existing *config.Config, o Options) (*Result, error) {
	if err := boundedJSON(data); err != nil {
		return nil, errors.New("Checkmate export: " + err.Error())
	}
	var rows []json.RawMessage
	if json.Unmarshal(data, &rows) != nil || rows == nil {
		return nil, errors.New("Checkmate export must be a JSON array (Settings export monitors.json)")
	}
	if len(rows) > 1000 {
		return nil, errors.New("maximum 1000 monitors")
	}
	var mons []adapterMonitor
	for _, raw := range rows {
		m, err := checkmateMonitor(raw)
		if err != nil {
			return nil, err
		}
		mons = append(mons, m)
	}
	r := adapterResult("Checkmate", mons, existing, o)
	r.Info = append(r.Info, "Source IDs, users, teams, tenants, status/history and metadata are not imported. HTTP transport retries/cookies differ; verify behavior before production use.")
	return r, nil
}

// These fields are observations or foreign identifiers, never destination
// authority.
var checkmateIgnored = strings.Fields("id _id userId teamId tenantId status statusWindow uptimePercentage cpuAlertThreshold cpuAlertCounter memoryAlertThreshold memoryAlertCounter diskAlertThreshold diskAlertCounter tempAlertThreshold tempAlertCounter selectedDisks gameId grpcServiceName strategy geoCheckLocations geoCheckInterval dockerLogsEnabled dockerAlertOnStopped dockerAlertOnUnhealthy dockerTlsCa dockerTlsCert dockerTlsKeySet dnsServer dnsRecordType recentChecks createdAt updatedAt lastEvaluatedAt __v")

var checkmateAccepted = strings.Fields("name description type url method interval isActive group ignoreTlsErrors proxyMode proxyId useAdvancedMatching jsonPath expectedValue matchMethod customUpCodes secret statusWindowSize statusWindowThreshold geoCheckEnabled notifications tags port")

type checkmateRow struct {
	Name, Description, Type, URL, Method, Group, Secret, ProxyMode, ProxyID, JSONPath, ExpectedValue, MatchMethod string
	Interval                                                                                                      int64
	Port                                                                                                          int
	IsActive                                                                                                      *bool
	IgnoreTLSErrors, UseAdvancedMatching, GeoCheckEnabled                                                         bool
	StatusWindowSize, StatusWindowThreshold                                                                       int
	CustomUpCodes                                                                                                 []int
	Notifications, Tags                                                                                           []string
}

func checkmateMonitor(raw json.RawMessage) (adapterMonitor, error) {
	var e checkmateRow
	if json.Unmarshal(raw, &e) != nil {
		return adapterMonitor{}, errors.New("invalid Checkmate monitor field types")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return adapterMonitor{}, errors.New("Checkmate monitors must be objects")
	}
	m := adapterMonitor{sourceType: e.Type, name: e.Name, group: e.Group, target: e.URL, method: e.Method, interval: strconv.FormatInt(e.Interval/1000, 10) + "s", timeout: "30s", expect: "200-299", bearer: e.Secret, insecure: e.IgnoreTLSErrors, disabled: e.IsActive != nil && !*e.IsActive}
	for key := range fields {
		if !contains(checkmateAccepted, key) && !contains(checkmateIgnored, key) {
			m.block("unsupported Checkmate monitor field")
		}
	}
	badCode := false
	for _, code := range e.CustomUpCodes {
		if code < 100 || code > 599 {
			badCode = true
		} else {
			m.expect += "," + strconv.Itoa(code)
		}
	}
	// The monitor type decides what is checked: a port monitor's
	// target is its url's host and its port (the port used to sit on
	// the ignore list, so the monitor was refused for a reason that
	// had nothing to do with it).
	switch e.Type {
	case "http":
		m.typ = "HTTP"
	case "port":
		m.typ = "TCP"
		m.target = checkmateHostPort(e.URL, e.Port)
	case "ping":
		m.typ = "PING"
		m.target = checkmateHost(e.URL)
	default:
		m.block("unsupported Checkmate monitor type (only HTTP, port and ping are supported)")
	}
	// Every rule that keeps the monitor out adds its own reason; the
	// report shows them all, not just the last one that applied.
	if e.Interval <= 0 || e.Interval%1000 != 0 {
		m.block("Checkmate interval must be positive whole seconds expressed in milliseconds")
	}
	if e.IsActive == nil {
		m.block("missing isActive flag")
	}
	switch {
	case e.ProxyMode == "inherit" && e.ProxyID == "":
		// Checkmate's factory default: the monitor inherits the
		// instance's proxy settings, which the export does not carry.
		// Importing it without a proxy is the honest reading; say so.
		m.note("It inherited Checkmate's proxy settings, which the export does not carry; it is imported without a proxy.", "Set proxy= on the record if it must go through one.")
	case e.ProxyMode != "none" || e.ProxyID != "":
		m.block("its proxy cannot be reconstructed from the monitor export; proxyMode must be none (or inherit, which is imported without a proxy)")
	}
	// Checkmate's status window marks a monitor down when threshold%
	// of the last size checks failed. The closest equivalent here is
	// attempts: the number of failed checks in a row that marks it
	// down, attempts = ceil(size × threshold / 100) — the smallest
	// run of failures that fills the window's failing share. For the
	// factory default (5, 60) that is 3; for (1, 100) it is 1, which
	// is exact. A window also forgives a failure once it slides out,
	// which attempts does not: the mapping is approximate, and the
	// report says so whenever the window holds more than one check.
	switch size, threshold := e.StatusWindowSize, e.StatusWindowThreshold; {
	case size < 1 || threshold < 1 || threshold > 100:
		m.block(fmt.Sprintf("its status window (size %d, threshold %d) is not a window: size must be at least 1 and threshold 1..100", size, threshold))
	default:
		attempts := (size*threshold + 99) / 100
		m.attempts = strconv.Itoa(max(attempts, 1))
		if size > 1 {
			m.note(fmt.Sprintf("Checkmate marked it down when %d%% of its last %d checks failed (its status window); here it is down after %d failed checks in a row (attempts=%d), the closest equivalent.", threshold, size, max(attempts, 1), max(attempts, 1)), "")
		}
	}
	if e.UseAdvancedMatching || e.JSONPath != "" || e.ExpectedValue != "" || e.MatchMethod != "" {
		m.block("advanced matching has no verified equivalent")
	}
	if e.GeoCheckEnabled {
		m.block("geographic checks are not supported")
	}
	if len(e.Notifications) > 0 || len(e.Tags) > 0 {
		m.block("notification/tag IDs cannot be resolved from monitor export")
	}
	if badCode {
		m.block("invalid custom HTTP status")
	}
	return m, nil
}

// checkmateHost is the host of a Checkmate target: the URL's host when
// it has one, else the value itself up to any path.
func checkmateHost(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	s := strings.TrimPrefix(raw, "//")
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	return s
}

// checkmateHostPort builds a port monitor's host:port target.
func checkmateHostPort(raw string, port int) string {
	host := checkmateHost(raw)
	if port <= 0 {
		return host
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

package importer

import (
	"encoding/json"
	"errors"
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
var checkmateIgnored = strings.Fields("port id _id userId teamId tenantId status statusWindow uptimePercentage cpuAlertThreshold cpuAlertCounter memoryAlertThreshold memoryAlertCounter diskAlertThreshold diskAlertCounter tempAlertThreshold tempAlertCounter selectedDisks gameId grpcServiceName strategy geoCheckLocations geoCheckInterval dockerLogsEnabled dockerAlertOnStopped dockerAlertOnUnhealthy dockerTlsCa dockerTlsCert dockerTlsKeySet dnsServer dnsRecordType recentChecks createdAt updatedAt lastEvaluatedAt __v")

var checkmateAccepted = strings.Fields("name description type url method interval isActive group ignoreTlsErrors proxyMode proxyId useAdvancedMatching jsonPath expectedValue matchMethod customUpCodes secret statusWindowSize statusWindowThreshold geoCheckEnabled notifications tags")

type checkmateRow struct {
	Name, Description, Type, URL, Method, Group, Secret, ProxyMode, ProxyID, JSONPath, ExpectedValue, MatchMethod string
	Interval                                                                                                      int64
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
			m.why = "unsupported Checkmate monitor field"
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
	// The last rule that applies gives the reason.
	for _, rule := range []struct {
		applies bool
		why     string
	}{
		{e.Interval <= 0 || e.Interval%1000 != 0, "Checkmate interval must be positive whole seconds expressed in milliseconds"},
		{e.IsActive == nil, "missing isActive flag"},
		{e.ProxyMode != "none" || e.ProxyID != "", "inherited/custom proxy cannot be reconstructed from monitor export; proxyMode must be none"},
		{e.StatusWindowSize != 1 || e.StatusWindowThreshold < 1 || e.StatusWindowThreshold > 100, "rolling status windows are not equivalent; only size 1 and threshold 1..100 are supported"},
		{e.UseAdvancedMatching || e.JSONPath != "" || e.ExpectedValue != "" || e.MatchMethod != "", "advanced matching has no verified equivalent"},
		{e.GeoCheckEnabled, "geographic checks are not supported"},
		{len(e.Notifications) > 0 || len(e.Tags) > 0, "notification/tag IDs cannot be resolved from monitor export"},
		{badCode, "invalid custom HTTP status"},
		{e.Type != "http", "unsupported Checkmate monitor type (only HTTP is supported)"},
	} {
		if rule.applies {
			m.why = rule.why
		}
	}
	return m, nil
}

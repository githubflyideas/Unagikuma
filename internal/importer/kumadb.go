package importer

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite" // reads kuma.db
)

// KumaUser is an Uptime Kuma account. The bcrypt hash is carried over, so
// the person keeps their password.
type KumaUser struct {
	Username string `json:"username"`
	Hash     string `json:"-"`
	Active   bool   `json:"active"`
}

var sqliteMagic = []byte("SQLite format 3\x00")

// isSQLite reports a SQLite database file (Uptime Kuma's kuma.db).
func isSQLite(data []byte) bool { return bytes.HasPrefix(data, sqliteMagic) }

// readKumaDB reads monitors, notifications and users straight from
// kuma.db, which is what Uptime Kuma 2 keeps (it has no JSON export).
// wal is the database's write-ahead log (kuma.db-wal), when Uptime Kuma
// was still running: recent changes may only be there.
func readKumaDB(data, wal []byte) (*kumaBackup, []KumaUser, error) {
	dir, err := os.MkdirTemp("", "kuma-import-")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "kuma.db")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return nil, nil, err
	}
	dsn := "file:" + path + "?mode=ro&immutable=1"
	if len(wal) > 0 {
		if err := os.WriteFile(path+"-wal", wal, 0o600); err != nil {
			return nil, nil, err
		}
		dsn = "file:" + path // the log is applied on open
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()
	monitors, err := rowsOf(db, "monitor")
	if err != nil {
		return nil, nil, errors.New("this database has no Uptime Kuma monitor table: is it kuma.db, and was it copied completely?")
	}
	notifs, _ := rowsOf(db, "notification")
	links, _ := rowsOf(db, "monitor_notification")
	tags, _ := rowsOf(db, "tag")
	mtags, _ := rowsOf(db, "monitor_tag")
	users, _ := rowsOf(db, "user")

	bk := &kumaBackup{FromDB: true, Proxies: map[int64]kumaProxy{}}
	bk.Version = versionOf(db)
	proxies, _ := rowsOf(db, "proxy")
	for _, p := range proxies {
		bk.Proxies[p.int("id")] = kumaProxy{Protocol: p.str("protocol"), Host: p.str("host"), Port: int(p.int("port")), Auth: p.bool("auth")}
	}
	maints, _ := rowsOf(db, "maintenance")
	mm, _ := rowsOf(db, "monitor_maintenance")
	for _, r := range maints {
		mt := kumaMaint{ID: r.int("id"), Title: r.str("title"), Description: r.str("description"), Strategy: r.str("strategy"), Active: r.bool("active"),
			StartDate: r.str("start_date"), EndDate: r.str("end_date"), StartTime: r.str("start_time"), EndTime: r.str("end_time"),
			Weekdays: r.str("weekdays"), DaysOfMonth: r.str("days_of_month"), Cron: r.str("cron"), Zone: r.str("timezone"),
			IntervalDay: int(r.int("interval_day")), Duration: int(r.int("duration"))}
		for _, l := range mm {
			if l.int("maintenance_id") == mt.ID {
				mt.Monitors = append(mt.Monitors, l.int("monitor_id"))
			}
		}
		bk.Maintenances = append(bk.Maintenances, mt)
	}
	pages, _ := rowsOf(db, "status_page")
	groups, _ := rowsOf(db, "group")
	mg, _ := rowsOf(db, "monitor_group")
	cnames, _ := rowsOf(db, "status_page_cname")
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].int("weight") < groups[j].int("weight") })
	sort.SliceStable(mg, func(i, j int) bool { return mg[i].int("weight") < mg[j].int("weight") })
	for _, p := range pages {
		sp := kumaStatusPage{Slug: p.str("slug"), Title: p.str("title"), Description: p.str("description"), Password: p.str("password") != ""}
		for _, c := range cnames {
			if c.int("status_page_id") == p.int("id") && c.str("domain") != "" {
				sp.Domains = append(sp.Domains, c.str("domain"))
			}
		}
		for _, g := range groups {
			if g.int("status_page_id") != p.int("id") {
				continue
			}
			pg := kumaPageGroup{Name: g.str("name")}
			for _, l := range mg {
				if l.int("group_id") == g.int("id") {
					pg.Monitors = append(pg.Monitors, l.int("monitor_id"))
				}
			}
			sp.Groups = append(sp.Groups, pg)
		}
		bk.StatusPages = append(bk.StatusPages, sp)
	}
	if inc, _ := rowsOf(db, "incident"); inc != nil {
		bk.Incidents = len(inc)
	}
	if keys, _ := rowsOf(db, "api_key"); keys != nil {
		bk.APIKeys = len(keys)
	}
	for _, n := range notifs {
		bk.NotificationList = append(bk.NotificationList, kumaNotification{ID: n.int("id"), Name: n.str("name"), Config: n.str("config"), Active: flag(n.bool("active")), IsDefault: flag(n.bool("is_default"))})
	}
	tagName := map[int64]string{}
	for _, t := range tags {
		tagName[t.int("id")] = t.str("name")
	}
	notifOf := map[int64]map[string]bool{}
	for _, l := range links {
		m := l.int("monitor_id")
		if notifOf[m] == nil {
			notifOf[m] = map[string]bool{}
		}
		notifOf[m][strconv.FormatInt(l.int("notification_id"), 10)] = true
	}
	tagsOf := map[int64][]kumaTag{}
	for _, t := range mtags {
		m := t.int("monitor_id")
		tagsOf[m] = append(tagsOf[m], kumaTag{Name: tagName[t.int("tag_id")], Value: t.str("value")})
	}
	for _, r := range monitors {
		id := r.int("id")
		m := kumaMonitor{
			ID: id, Name: r.str("name"), Description: r.str("description"), Type: r.str("type"), URL: r.str("url"),
			Method: r.str("method"), Body: r.str("body"), Headers: r.str("headers"), Hostname: r.str("hostname"),
			Port: int(r.int("port")), Interval: int(r.int("interval")), RetryInterval: int(r.int("retry_interval")),
			MaxRetries: int(r.int("maxretries")), Timeout: r.float("timeout"), Active: flag(r.bool("active")),
			Keyword: r.str("keyword"), InvertKeyword: flag(r.bool("invert_keyword")), JSONPath: r.str("json_path"),
			ExpectedValue: r.str("expected_value"), IgnoreTLS: flag(r.bool("ignore_tls")), UpsideDown: flag(r.bool("upside_down")),
			DNSResolveType: r.str("dns_resolve_type"), DNSResolveServer: r.str("dns_resolve_server"),
			BasicUser: r.str("basic_auth_user"), BasicPass: r.str("basic_auth_pass"),
			DBConn: r.str("database_connection_string"), DockerContainer: r.str("docker_container"),
			GRPCURL: r.str("grpc_url"), GRPCService: r.str("grpc_service_name"), GRPCTLS: flag(r.bool("grpc_enable_tls")),
			ExpiryNotify: flag(r.bool("expiry_notification")), Notifications: notifOf[id], Tags: tagsOf[id],
			AuthMethod: r.str("auth_method"), Bearer: r.str("bearer_token"), JSONOp: r.str("json_path_operator"), PushToken: r.str("push_token"),
			SNMPVersion: r.str("snmp_version"), SNMPOID: r.str("snmp_oid"), RadiusPassword: r.str("radius_password"),
		}
		if v, ok := r["proxy_id"]; ok && v != nil {
			p := r.int("proxy_id")
			m.ProxyID = &p
		}
		if v, ok := r["maxredirects"]; ok && v != nil {
			n := int(r.int("maxredirects"))
			m.MaxRedirects = &n
		}
		if v, ok := r["docker_host"]; ok && v != nil {
			h := r.int("docker_host")
			m.DockerHost = &h
		}
		if v, ok := r["parent"]; ok && v != nil {
			p := r.int("parent")
			m.Parent = &p
		}
		if s := r.str("accepted_statuscodes_json"); s != "" {
			_ = json.Unmarshal([]byte(s), &m.AcceptedStatus)
		}
		bk.MonitorList = append(bk.MonitorList, m)
	}
	var out []KumaUser
	for _, u := range users {
		if name := u.str("username"); name != "" {
			out = append(out, KumaUser{Username: name, Hash: u.str("password"), Active: u.bool("active")})
		}
	}
	if len(bk.MonitorList) == 0 && len(out) == 0 {
		return nil, nil, errors.New("the Uptime Kuma database has no monitors and no users")
	}
	return bk, out, nil
}

// versionOf guesses the Uptime Kuma version from its schema: 2.x keeps
// the applied migrations in knex_migrations.
func versionOf(db *sql.DB) string {
	if rows, err := rowsOf(db, "knex_migrations"); err == nil && len(rows) > 0 {
		return "2.x"
	}
	return "1.x"
}

// row is one database row by column name; column sets differ between
// Uptime Kuma versions, so absent columns read as zero.
type row map[string]any

func rowsOf(db *sql.DB, table string) ([]row, error) {
	rows, err := db.Query(`SELECT * FROM "` + table + `"`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []row
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		r := row{}
		for i, c := range cols {
			r[strings.ToLower(c)] = vals[i]
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (r row) str(k string) string {
	switch v := r[k].(type) {
	case string:
		return v
	case []byte:
		return string(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return ""
}

func (r row) int(k string) int64 {
	switch v := r[k].(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	case string:
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	}
	return 0
}

func (r row) float(k string) float64 {
	switch v := r[k].(type) {
	case int64:
		return float64(v)
	case float64:
		return v
	case string:
		f, _ := strconv.ParseFloat(v, 64)
		return f
	}
	return 0
}

func (r row) bool(k string) bool {
	switch v := r[k].(type) {
	case bool:
		return v
	case int64:
		return v != 0
	case string:
		return v == "1" || v == "true"
	}
	return false
}

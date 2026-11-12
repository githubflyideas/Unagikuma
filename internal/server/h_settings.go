package server

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/backup"
	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/directory"
	"github.com/githubflyideas/unagikuma/internal/i18n"
	"github.com/githubflyideas/unagikuma/internal/keywords"
	"github.com/githubflyideas/unagikuma/internal/notify"
	"github.com/githubflyideas/unagikuma/internal/probe"
	"github.com/githubflyideas/unagikuma/internal/store"
	"github.com/githubflyideas/unagikuma/internal/tunables"
)

// ---- channels and policies ----

func (s *Server) channelKinds(w http.ResponseWriter, r *http.Request) (any, error) {
	return notify.Kinds(), nil
}

func isSecretKey(k string) bool {
	switch k {
	case "token", "secret", "password", "sendkey", "device-key", "user-key", "routing-key", "api-key", "community",
		"access-key-secret", "secret-key", "auth-token":
		return true
	}
	return false
}

func (s *Server) channels(w http.ResponseWriter, r *http.Request) (any, error) {
	cfg := s.app.EffectiveConfig()
	type chJSON struct {
		Name    string            `json:"name"`
		Kind    string            `json:"kind"`
		Opts    map[string]string `json:"opts"`
		Retries int               `json:"retries"`
		UsedBy  []string          `json:"used_by"` // policies written in the file
		Teams   []string          `json:"teams"`   // teams notified through it
		System  bool              `json:"system"`  // a system channel (system=yes)
		ETag    string            `json:"etag"`
		// Issues are recipients the contact directory flags (red dot).
		Issues []directory.Issue `json:"issues,omitempty"`
	}
	issues := s.app.ChannelIssues()
	out := []chJSON{}
	for _, c := range cfg.Channels {
		opts := map[string]string{}
		for k, v := range c.Opts {
			if (isSecretKey(k) || (k == "url" && c.Kind != "webhook")) && !strings.Contains(v, "${") {
				v = "***"
			}
			opts[k] = v
		}
		if c.Period != "" {
			opts["period"] = c.Period
		}
		if len(c.Members) > 0 {
			opts["members"] = strings.Join(c.Members, ",")
		}
		if c.System {
			opts["system"] = "yes"
		}
		if c.NoReport {
			opts["report"] = "no"
		}
		used := []string{}
		for _, p := range cfg.Policies {
			for _, n := range p.Chain {
				if n == c.Name && !p.Implicit {
					used = append(used, p.Name)
				}
			}
		}
		teams := []string{}
		for _, t := range cfg.AllTeams() {
			if slices.Contains(t.Channels, c.Name) {
				teams = append(teams, t.Name)
			}
		}
		out = append(out, chJSON{c.Name, c.Kind, opts, c.Retries, used, teams, c.System, s.recordETags()[c.Name], issues[c.Name]})
	}
	return out, nil
}

// channelSave creates or replaces a CHANNEL record. Credential fields are

var telegramToken = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`)

// stored as secrets and referenced, so plaintext never reaches the file.
func (s *Server) channelSave(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Name    string            `json:"name"`
		Kind    string            `json:"kind"`
		Opts    map[string]string `json:"opts"`
		Message string            `json:"message"`
	}
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	if !config.ValidName(req.Name) {
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "invalid name")
	}
	if req.Opts == nil {
		req.Opts = map[string]string{}
	}
	kind := req.Kind
	if p, ok := strings.CutPrefix(kind, "webhook:"); ok {
		kind = "webhook"
		req.Opts["preset"] = p
	}
	existing := s.app.EffectiveConfig().FindChannel(req.Name)
	keys := make([]string, 0, len(req.Opts))
	for k := range req.Opts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	// A bot token typed here is checked before it is stored: a wrong one
	// would otherwise only show when an alert fails to go out.
	if tok := req.Opts["token"]; kind == "telegram" && tok != "" && tok != "***" && !strings.Contains(tok, "${") && !telegramToken.MatchString(tok) {
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "this is not a Telegram bot token: it looks like 123456789:AAE… (numbers, a colon, then letters and digits), as BotFather gives it")
	}
	// A record is one line: the line breaks of a message template are kept
	// as the two characters \n, which the channel turns back when sending.
	if tpl, ok := req.Opts["template"]; ok {
		req.Opts["template"] = strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(tpl), "\r\n", "\n"), "\n", `\n`)
	}
	// A template typed here is tried at once: a file may carry a wrong one
	// (it loads with a warning), a form does not save it.
	if probs := notify.TemplateProblems(kind, req.Opts); len(probs) > 0 {
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "%s", strings.Join(probs, "; "))
	}
	var opts []config.Opt
	for _, k := range keys {
		v := req.Opts[k]
		switch {
		case v == "***" && existing != nil:
			v = existing.Opts[k] // unchanged masked value
		case v != "" && !strings.Contains(v, "${") && (isSecretKey(k) || (k == "url" && kind != "webhook")):
			name := "ch_" + req.Name + "_" + strings.ReplaceAll(k, "-", "_")
			if err := s.app.SecretStore().Put(name, v, []string{kind}); err != nil {
				return nil, err
			}
			v = "${secret:" + name + "}"
		}
		if v != "" {
			opts = append(opts, config.Opt{Key: k, Value: v})
		}
	}
	spec := config.RecordSpec{Name: req.Name, Type: "CHANNEL", Target: kind, Opts: opts}
	st, err := s.editConfig(r, req.Message, ifMatch(r, spec.Name, func(f *config.File) error { return f.UpsertRecord(spec) }))
	if err != nil {
		return nil, s.configError(err)
	}
	return s.savedRecord(st, spec.Name), nil
}

func (s *Server) channelDelete(w http.ResponseWriter, r *http.Request) (any, error) {
	name := r.PathValue("name")
	if s.app.EffectiveConfig().FindChannel(name) == nil {
		return nil, notFound("channel")
	}
	st, err := s.editConfig(r, "delete channel "+name, ifMatch(r, name, func(f *config.File) error { return f.DeleteRecord(name) }))
	if err != nil {
		return nil, s.configError(err)
	}
	return s.saved(st), nil
}

func (s *Server) channelTest(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Name string            `json:"name"`
		Kind string            `json:"kind"`
		Opts map[string]string `json:"opts"`
		// Mode selects recipient handling:
		//   "" / "alert" (default): same active-contact whitelist as real alerts
		//   "connection": transport-only probe; does not claim real recipients are reachable
		Mode string `json:"mode"`
	}
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	if req.Opts == nil {
		req.Opts = map[string]string{}
	}
	ch := notify.Channel{Name: req.Name, Kind: req.Kind, Opts: req.Opts}
	if p, ok := strings.CutPrefix(ch.Kind, "webhook:"); ok {
		ch.Kind = "webhook"
		ch.Opts["preset"] = p
	}
	if c := s.app.EffectiveConfig().FindChannel(req.Name); c != nil && req.Kind == "" {
		ch.Kind, ch.Opts = c.Kind, map[string]string{}
		for k, v := range c.Opts {
			ch.Opts[k] = v
		}
	}
	resolve := s.app.Resolver()
	for k, v := range ch.Opts {
		rv, err := resolve(v)
		if err != nil {
			return nil, Errorf(http.StatusBadRequest, CodeInvalid, "%v", err)
		}
		ch.Opts[k] = rv
	}

	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = "alert"
	}
	diag := map[string]any{"mode": mode}
	if key, kind, ok := directory.AddressKey(ch.Kind); ok {
		raw := ch.Opts[key]
		send, issues := s.app.Directory().Resolve(kind, raw)
		filtered := 0
		if raw != "" {
			// Approximate input count via Resolve issues + kept.
			filtered = len(directory.SplitList(raw)) - len(send)
			if filtered < 0 {
				filtered = 0
			}
		}
		diag["input"] = raw
		diag["kept"] = len(send)
		diag["filtered"] = filtered
		// Issues are about addresses; that nobody is left is about the
		// whole list, as in /people/lookup.
		about := []directory.Issue{}
		none := false
		for _, is := range issues {
			if is.Code == directory.IssueNoRecipients {
				none = true
				continue
			}
			about = append(about, is)
		}
		diag["issues"], diag["no_recipients"] = about, none
		switch mode {
		case "connection":
			diag["note"] = "connection_only"
			// Leave opts unchanged: connection mode does not claim whitelist delivery.
		default:
			ch.Opts[key] = strings.Join(send, ",")
			if len(send) == 0 {
				msg := "nobody to send to: no recipient is an active contact (see Alerting › Contacts)"
				if raw == "" {
					msg = "nobody to send to: channel has no recipients configured"
				}
				return map[string]any{
					"ok": false, "error": msg, "channel": ch.Name,
					"status": 0, "diagnostic": diag,
				}, nil
			}
		}
	} else if mode == "connection" {
		diag["note"] = "connection_only"
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.app.EffectiveConfig().Tunables.Duration("notify.channel-timeout"))
	defer cancel()
	att := s.app.Engine().TestChannel(ctx, ch)
	out := map[string]any{
		"ok": att.OK, "error": att.Error, "channel": att.Channel,
		"status": att.Status, "response": att.Response, "latency_ms": float64(att.Latency) / float64(time.Millisecond),
		"diagnostic": diag,
	}
	if mode == "connection" && att.OK {
		out["warning"] = "connection_only_success"
	}
	return out, nil
}

func (s *Server) policies(w http.ResponseWriter, r *http.Request) (any, error) {
	type polJSON struct {
		Name          string   `json:"name"`
		Chain         []string `json:"chain"`
		Mode          string   `json:"mode"`
		RepeatS       int      `json:"repeat_s"`
		Max           int      `json:"max"`
		DelayS        int      `json:"delay_s"`
		ReopenWindowS int      `json:"reopen_window_s"`
		Recovery      bool     `json:"recovery"`
		Degraded      bool     `json:"notify_degraded"`
		AckExpireS    int      `json:"ack_expire_s"`
		Escalate      string   `json:"escalate"`
		Network       string   `json:"network"`
		Implicit      bool     `json:"implicit"`
		UsedByCount   int      `json:"used_by_count"`
		ETag          string   `json:"etag,omitempty"` // empty for implicit policies
		// IssueChannels are channels of the chain (or of its groups) whose
		// recipients the contact directory flags: the policy may not reach
		// who it should.
		IssueChannels []string `json:"issue_channels,omitempty"`
	}
	issues := s.app.ChannelIssues()
	cfg := s.app.EffectiveConfig()
	out := []polJSON{}
	for _, p := range cfg.Policies {
		if p.Team != "" || p.Mute {
			continue // made from a team's channels, or mute=yes
		}
		n := 0
		for _, m := range cfg.Monitors {
			if m.Policy == p.Name {
				n++
			}
		}
		chain := p.Chain
		if chain == nil {
			chain = []string{}
		}
		out = append(out, polJSON{p.Name, chain, p.Mode, int(p.Repeat.Seconds()), p.Max, int(p.Delay.Seconds()), int(p.ReopenWindow.Seconds()),
			p.Recovery, p.NotifyDegraded, int(p.AckExpire.Seconds()), config.EscalateText(p.Escalate), strings.Join(p.Network, ","), p.Implicit, n, s.recordETags()[p.Name], chainIssues(cfg, p.Chain, issues)})
	}
	return out, nil
}

func (s *Server) policySave(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Name    string            `json:"name"`
		Chain   []string          `json:"chain"`
		Opts    map[string]string `json:"opts"`
		Message string            `json:"message"`
	}
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	if !config.ValidName(req.Name) || len(req.Chain) == 0 {
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "a policy needs a name and at least one channel")
	}
	opts := []config.Opt{{Key: "chain", Value: strings.Join(req.Chain, ",")}}
	keys := make([]string, 0, len(req.Opts))
	for k := range req.Opts {
		if k != "chain" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		if v := req.Opts[k]; v != "" {
			opts = append(opts, config.Opt{Key: k, Value: v})
		}
	}
	spec := config.RecordSpec{Name: req.Name, Type: "POLICY", Opts: opts}
	st, err := s.editConfig(r, req.Message, ifMatch(r, spec.Name, func(f *config.File) error { return f.UpsertRecord(spec) }))
	if err != nil {
		return nil, s.configError(err)
	}
	return s.savedRecord(st, spec.Name), nil
}

func (s *Server) policyTest(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		FailFirst int `json:"fail_first"`
	}
	_ = decodeJSON(r, s.maxBody(), &req)
	name := r.PathValue("name")
	if s.app.EffectiveConfig().FindPolicy(name) == nil {
		return nil, notFound("policy")
	}
	id, err := s.app.Engine().TestPolicy(name, req.FailFirst, authOf(r).user)
	if err != nil {
		return nil, err
	}
	return map[string]any{"intent_id": id}, nil
}

func (s *Server) policyPredict(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Monitor string `json:"monitor"`
		Minutes int    `json:"minutes"`
	}
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	if req.Minutes <= 0 || req.Minutes > 7*24*60 {
		req.Minutes = 60
	}
	steps, err := s.app.Engine().Predict(req.Monitor, time.Duration(req.Minutes)*time.Minute)
	if err != nil {
		return nil, notFound("monitor")
	}
	type stepJSON struct {
		AtS     int    `json:"at_s"`
		Kind    string `json:"kind"`
		Seq     int    `json:"seq"`
		Max     int    `json:"max"`
		Channel string `json:"channel"`
	}
	out := []stepJSON{}
	for _, st := range steps {
		out = append(out, stepJSON{int(st.At.Seconds()), string(st.Kind), st.Seq, st.Max, st.Channel})
	}
	return map[string]any{"timeline": out}, nil
}

// ---- tunables, keywords, system, settings ----

func (s *Server) tunablesGet(w http.ResponseWriter, r *http.Request) (any, error) {
	ts := s.app.EffectiveConfig().Tunables
	over := ts.Overrides()
	type tj struct {
		Key        string `json:"key"`
		Kind       string `json:"kind"`
		Default    string `json:"default"`
		Value      string `json:"value"`
		Overridden bool   `json:"overridden"`
		Min        string `json:"min,omitempty"`
		Max        string `json:"max,omitempty"`
		Desc       string `json:"desc"`
		DescZH     string `json:"desc_zh"`
		Override   string `json:"override,omitempty"`
	}
	out := []tj{}
	for _, d := range tunables.Defs() {
		_, o := over[d.Key]
		v := ts.Raw(d.Key)
		if d.Key == "retention.audit" {
			v = days(s.app.Store().AuditRetention(ts.Duration(d.Key))) // the one in effect
		}
		out = append(out, tj{d.Key, string(d.Kind), d.Default, v, o, d.Min, d.Max, d.Desc, d.DescZH, d.Override})
	}
	return out, nil
}

// days writes a duration as tunables do: whole days as "730d".
func days(d time.Duration) string {
	if d%(24*time.Hour) == 0 {
		return strconv.Itoa(int(d/(24*time.Hour))) + "d"
	}
	return d.String()
}

func (s *Server) tunablesPut(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Set     map[string]string `json:"set"`
		Unset   []string          `json:"unset"`
		Message string            `json:"message"`
	}
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	for k, v := range req.Set {
		if _, err := tunables.New(map[string]string{k: v}); err != nil {
			return nil, Errorf(http.StatusBadRequest, CodeInvalid, "%v", err)
		}
	}
	for _, k := range append(slices.Collect(maps.Keys(req.Set)), req.Unset...) {
		if k == "retention.audit" {
			// Shortening it would clear the log from the web.
			return nil, Errorf(http.StatusForbidden, CodeForbidden, "the audit retention is set when starting the server (--audit-retention)")
		}
	}
	st, err := s.editConfig(r, req.Message, func(f *config.File) error { return f.SetTunables(req.Set, req.Unset) })
	if err != nil {
		return nil, s.configError(err)
	}
	return s.saved(st), nil
}

func (s *Server) keywordsList(w http.ResponseWriter, r *http.Request) (any, error) {
	d, _ := keywords.ParseDialect(r.URL.Query().Get("dialect"))
	type kw struct {
		Keyword    string   `json:"keyword"`
		Aliases    []string `json:"aliases,omitempty"`
		Port       int      `json:"port"`
		Service    string   `json:"service"`
		ServiceZH  string   `json:"service_zh"`
		Depth      string   `json:"depth"`
		Effective  string   `json:"effective_depth"`
		Sources    []string `json:"sources"`
		Template   string   `json:"template,omitempty"`
		Stateful   bool     `json:"stateful,omitempty"`
		Reflective bool     `json:"reflective,omitempty"`
		Privileged bool     `json:"privileged,omitempty"`
		Suggest    string   `json:"suggest,omitempty"`
		Note       string   `json:"note,omitempty"`
		InDialect  bool     `json:"in_dialect"`
	}
	out := []kw{}
	for _, e := range keywords.All() {
		port := e.Port
		if res, err := keywords.Resolve(e.Keyword, d); err == nil {
			port = res.Port
		}
		out = append(out, kw{e.Keyword, e.Aliases, port, e.Service, e.ServiceZH, string(e.Depth), string(e.EffectiveDepth(probe.Built(e.Template))),
			e.Sources, e.Template, e.Stateful, e.Reflective, e.Privileged, e.Suggest, e.Note, e.InDialect(d)})
	}
	return out, nil
}

func (s *Server) system(w http.ResponseWriter, r *http.Request) (any, error) {
	st := s.app.Store().Stats()
	return map[string]any{
		"version": s.app.Version(), "instance": s.app.Instance(), "config_path": s.app.ConfigPath(),
		"uptime_s": int(s.now().Sub(s.app.Started()).Seconds()), "platform": runtime.GOOS + "/" + runtime.GOARCH,
		"go": runtime.Version(), "storage": st, "sse_clients": s.hub.Clients(),
		"setup_token_pending": s.SetupToken() != "",
		// ui_plaintext is true when this request itself arrived without TLS.
		// Agents still use sniffed TLS on the same port; this warns about the UI.
		"ui_plaintext": r.TLS == nil,
		"listen":       s.app.ListenAddr(),
	}, nil
}

var settingKeys = []string{"heartbeat_url", "message_lang", "base_url", "instance_name"}

func (s *Server) settingsGet(w http.ResponseWriter, r *http.Request) (any, error) {
	out := map[string]string{}
	for _, k := range settingKeys {
		out[k] = s.app.Setting(k)
	}
	return out, nil
}

func (s *Server) settingsPut(w http.ResponseWriter, r *http.Request) (any, error) {
	var req map[string]string
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	for k, v := range req {
		if k == "message_lang" {
			v = i18n.Normalize(v)
		}
		if err := s.app.SetSetting(k, v); err != nil {
			return nil, Errorf(http.StatusBadRequest, CodeInvalid, "%v", err)
		}
	}
	return s.settingsGet(w, r)
}

func (s *Server) heartbeatTest(w http.ResponseWriter, r *http.Request) (any, error) {
	return s.app.BeatNow(r.Context()), nil
}

func (s *Server) tokensList(w http.ResponseWriter, r *http.Request) (any, error) {
	return s.app.Store().Tokens()
}

func (s *Server) tokenCreate(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Name   string   `json:"name"`
		Scopes []string `json:"scopes"`
	}
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	for _, sc := range req.Scopes {
		switch sc {
		case "read", "ack", "write", "admin":
		default:
			return nil, Errorf(http.StatusBadRequest, CodeInvalid, "unknown scope %q", sc)
		}
	}
	if req.Name == "" || len(req.Scopes) == 0 {
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "name and scopes are required")
	}
	plain, err := s.app.Store().CreateToken(authOf(r).userID, req.Name, req.Scopes)
	if err != nil {
		return nil, err
	}
	auditOf(r).note("scopes", req.Scopes).target = req.Name
	return map[string]any{"token": plain}, nil
}

func (s *Server) tokenDelete(w http.ResponseWriter, r *http.Request) (any, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return nil, notFound("token")
	}
	return nil, s.app.Store().DeleteToken(id)
}

func (s *Server) secretsList(w http.ResponseWriter, r *http.Request) (any, error) {
	return s.app.SecretStore().List()
}

func (s *Server) secretPut(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Value  string   `json:"value"`
		Scopes []string `json:"scopes"`
	}
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	name := r.PathValue("name")
	if !config.ValidName(name) || req.Value == "" {
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "invalid name or empty value")
	}
	if err := s.app.SecretStore().Put(name, req.Value, req.Scopes); err != nil {
		return nil, err
	}
	// Monitors and channels referencing it pick up the new value.
	s.app.Reapply()
	return nil, nil
}

// secretDelete removes a secret, unless the configuration still refers to
// it: the file would stop validating and every later edit would be
// refused until the secret was put back.
func (s *Server) secretDelete(w http.ResponseWriter, r *http.Request) (any, error) {
	name := r.PathValue("name")
	if users := s.secretUsers(name); len(users) > 0 {
		e := Errorf(http.StatusConflict, CodeConflict, "secret %s is still used by %s: change or delete %s first", name, strings.Join(users, ", "),
			map[bool]string{true: "it", false: "them"}[len(users) == 1])
		e.Extra = map[string]any{"used_by": users}
		return nil, e
	}
	return nil, s.app.SecretStore().Delete(name)
}

// secretUsers names the records of the shared and the site's own
// configuration that refer to a secret.
func (s *Server) secretUsers(name string) []string {
	ref := "${secret:" + name + "}"
	seen := map[string]bool{}
	var out []string
	scan := func(path, text string) {
		f, err := config.Parse(path, []byte(text))
		if err != nil || f == nil {
			if strings.Contains(text, ref) {
				out = append(out, path)
			}
			return
		}
		for _, rec := range f.Records() {
			used := strings.Contains(rec.Target, ref)
			for _, o := range rec.Opts {
				used = used || strings.Contains(o.Value, ref)
			}
			if used && !seen[rec.Name] {
				seen[rec.Name] = true
				out = append(out, rec.Name)
			}
		}
	}
	scan("monitors.conf", s.app.Config().Text)
	scan("site-local.conf", s.app.SiteLocal())
	return out
}

// backup streams an archive. GET gives the databases and monitors.conf;
// POST with a passphrase also includes the secret key and the cluster CA,
// encrypted.
func (s *Server) backup(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Passphrase string `json:"passphrase"`
		History    bool   `json:"history"`
	}
	if r.Method == http.MethodPost {
		if err := decodeJSON(r, s.maxBody(), &req); err != nil {
			return nil, err
		}
		if len(req.Passphrase) < 8 {
			return nil, Errorf(http.StatusBadRequest, CodeInvalid, "the passphrase must have at least 8 characters")
		}
	} else {
		req.History = r.URL.Query().Get("history") == "1"
	}
	ext := "tar.gz"
	if req.Passphrase != "" {
		ext = "ukbackup"
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="unagikuma-%s.%s"`, s.now().Format("20060102-150405"), ext))
	_, err := backup.Write(r.Context(), w, s.app.Store().BackupDB, backup.Options{
		DataDir: s.app.DataDir(), ConfigPath: s.app.ConfigPath(), History: req.History, Passphrase: req.Passphrase,
		Instance: s.app.Instance(), Version: s.app.Version(), Now: s.now(),
	})
	if err != nil {
		s.log.Error("backup failed", "err", err)
	}
	actor, ai := "", authOf(r)
	if ai != nil {
		actor = ai.user
	}
	_ = s.app.Store().Audit(store.AuditEntry{Actor: actor, Action: "backup.download", Addr: s.ip(r),
		Detail: withClient(map[string]any{"encrypted": req.Passphrase != "", "history": req.History, "ok": err == nil}, s.client(r, ai))})
	return nil, errHandled
}

// errHandled tells handle() the response was already written.
var errHandled = &APIError{Status: 0, Code: "handled"}

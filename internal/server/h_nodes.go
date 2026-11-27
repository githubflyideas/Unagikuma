package server

import (
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/cluster"
	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/engine"
	"github.com/githubflyideas/unagikuma/internal/store"
)

// nodeView is one probe node with its connection state and token.
type nodeView struct {
	Name        string              `json:"name"`
	Title       string              `json:"title,omitempty"`
	Region      string              `json:"region,omitempty"`
	Probe       bool                `json:"probe"`
	Policy      string              `json:"policy"`
	TimeoutS    int                 `json:"timeout_s"`
	Anchors     map[string][]string `json:"anchors"`
	AnchorsText string              `json:"anchors_text"` // as written in monitors.conf
	Self        bool                `json:"self,omitempty"`
	Standby     string              `json:"standby,omitempty"` // address it takes over at
	ETag        string              `json:"etag,omitempty"`
	Unsupported []string            `json:"unsupported,omitempty"` // monitors it is too old to check

	Online   bool           `json:"online"`
	Since    *time.Time     `json:"since,omitempty"`
	LastSeen *time.Time     `json:"last_seen,omitempty"`
	Serial   uint32         `json:"serial,omitempty"`
	Synced   bool           `json:"synced"`
	RTTMS    float64        `json:"rtt_ms,omitempty"`
	SkewMS   float64        `json:"skew_ms"`
	ClockOK  bool           `json:"clock_ok"`
	Version  string         `json:"version,omitempty"`
	Platform string         `json:"platform,omitempty"`
	Addr     string         `json:"addr,omitempty"`
	Health   *engine.Health `json:"-"`
	// HealthView is the node's own view of its network, as the API shows it.
	HealthView *healthJSON `json:"health,omitempty"`
	// The local peer-path verdict, the node's own view and its
	// notification uplink.
	Verdict string                `json:"verdict,omitempty"`
	Reach   map[string]bool       `json:"reach,omitempty"`
	Gateway []engine.GatewayState `json:"gateway,omitempty"`

	Token *store.AgentToken `json:"token"`
}

func (s *Server) nodeViews() []nodeView {
	cfg := s.app.EffectiveConfig()
	status := map[string]engine.NodeStatus{}
	for _, st := range s.app.Engine().Nodes() {
		status[st.Node] = st
	}
	tokens := map[string]store.AgentToken{}
	if list, err := s.app.Store().AgentTokens(); err == nil {
		for _, t := range list {
			tokens[t.Node] = t
		}
	}
	maxSkew := cfg.Tunables.Duration("cluster.clock-skew")
	out := []nodeView{}
	self := nodeView{Name: cfg.MName, Probe: true, Self: true, Online: true, Synced: true, ClockOK: true, Policy: "system",
		Anchors: map[string][]string{}, Serial: cfg.Serial}
	for _, n := range cfg.Nodes {
		v := nodeView{Name: n.Name, Title: n.Description, Region: n.Region, Probe: n.Probe, Policy: n.Policy,
			TimeoutS: int(n.NodeTimeout.Seconds()), Anchors: map[string][]string{"lan": orEmptyOf(n.AnchorsLAN), "wan": orEmptyOf(n.AnchorsWAN)}, AnchorsText: n.Opts["anchors"],
			Standby: n.Standby, ETag: s.recordETags()[n.Name]}
		if n.Name == cfg.MName {
			self.Title, self.Region, self.Probe, self.Anchors, self.AnchorsText, self.Policy, self.ETag = v.Title, v.Region, v.Probe, v.Anchors, v.AnchorsText, v.Policy, v.ETag
			continue
		}
		if st, ok := status[n.Name]; ok {
			v.Online, v.Serial, v.Version, v.Platform, v.Addr, v.Health, v.HealthView = st.Online, st.Serial, st.Version, st.Platform, st.Addr, st.Health, healthOf(st.Health)
			v.Unsupported = st.Unsupported
			v.Verdict, v.Reach, v.Gateway = st.Verdict, st.Reach, st.Gateway
			v.Since, v.LastSeen = tp(st.Since), tp(st.LastSeen)
			v.RTTMS, v.SkewMS = float64(st.RTT)/1e6, float64(st.Skew)/1e6
			v.Synced = st.Online && st.Serial == cfg.Serial
			v.ClockOK = absDuration(st.Skew) <= maxSkew
		}
		if t, ok := tokens[n.Name]; ok {
			v.Token = &t
		}
		out = append(out, v)
	}
	if m := s.app.Agent().Member; m != nil {
		// On a node of a cluster the own configuration's name is not this
		// node's: name the row as the cluster knows it.
		self.Name, self.Title = m.Node, s.app.ReplicaStatus().Site
		if self.Title == m.Node {
			self.Title = ""
		}
	}
	self.Gateway, self.Verdict = s.app.GatewayStates(), engine.NodeOK
	_, self.Reach = s.app.ReachView()
	return append([]nodeView{self}, out...)
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func (s *Server) nodesList(w http.ResponseWriter, r *http.Request) (any, error) {
	views := s.nodeViews()
	rep := s.app.ReplicaStatus()
	layout := "single"
	switch {
	case rep.Replica:
		layout = "replica"
	case len(views) > 1:
		layout = "cluster"
	}
	monitors := len(s.app.EffectiveConfig().Monitors)
	if rep.Replica && rep.Monitors > monitors {
		monitors = rep.Monitors
	}
	return map[string]any{"layout": layout, "nodes": views, "fingerprint": s.app.PKI().Fingerprint(), "server": agentServerURL(r),
		"agent": s.app.Agent(), "instance": s.app.Instance(), "monitors": monitors,
		"replica": rep, "peers": orEmptyOf(s.app.Peers()), "local_nodes": s.localNodes(), "gateway": s.app.GatewayStates()}, nil
}

var hostRe = regexp.MustCompile(`^(\[[0-9A-Fa-f:.]+\]|[A-Za-z0-9.-]+)(:[0-9]{1,5})?$`)

// agentServerURL is the master address for install commands: the host
// the administrator reached the UI on. Agents need the master's own port,
// not a reverse proxy in front of it, so the UI lets them edit it.
func agentServerURL(r *http.Request) string {
	host := r.Host
	if !hostRe.MatchString(host) {
		return "https://MASTER:8077"
	}
	if m := hostRe.FindStringSubmatch(host); m[2] == "" {
		host += ":8077"
	}
	return "https://" + host
}

func (s *Server) issueToken(r *http.Request, node string) (map[string]any, error) {
	tok, err := s.app.Store().CreateAgentToken(node)
	if err != nil {
		return nil, err
	}
	// A session made with the previous token ends now.
	s.app.Cluster().Disconnect(node, "the token of node "+node+" was replaced")
	inst := cluster.InstallCommands(agentServerURL(r), node, tok, s.app.PKI().Fingerprint())
	return map[string]any{"node": node, "token": tok, "install": inst}, nil
}

func (s *Server) nodeAdd(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Name    string `json:"name"`
		Title   string `json:"title"`
		Region  string `json:"region"`
		Message string `json:"message"`
	}
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	cfg := s.app.EffectiveConfig()
	switch {
	case !config.ValidName(req.Name):
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "invalid node name %q", req.Name)
	case req.Name == cfg.MName || cfg.HasName(req.Name):
		return nil, Errorf(http.StatusConflict, CodeConflict, "%s already exists", req.Name)
	}
	if err := s.app.AllowAdd("nodes"); err != nil {
		return nil, quotaError(err)
	}
	spec := config.RecordSpec{Name: req.Name, Type: "NODE", Target: req.Title}
	if req.Region != "" {
		spec.Opts = append(spec.Opts, config.Opt{Key: "region", Value: req.Region})
	}
	msg := req.Message
	if msg == "" {
		msg = "add node " + req.Name
	}
	st, err := s.editConfig(r, msg, func(f *config.File) error { return f.UpsertRecord(spec) })
	if err != nil {
		return nil, s.configError(err)
	}
	out, err := s.issueToken(r, req.Name)
	if err != nil {
		return nil, err
	}
	for k, v := range s.saved(st) {
		out[k] = v
	}
	return out, nil
}

func (s *Server) nodeToken(w http.ResponseWriter, r *http.Request) (any, error) {
	name := r.PathValue("name")
	cfg := s.app.EffectiveConfig()
	if cfg.FindNode(name) == nil || name == cfg.MName {
		return nil, notFound("node")
	}
	return s.issueToken(r, name)
}

func (s *Server) nodeRevoke(w http.ResponseWriter, r *http.Request) (any, error) {
	name := r.PathValue("name")
	if err := s.app.Store().DeleteAgentToken(name); err != nil {
		return nil, err
	}
	s.app.Cluster().Disconnect(name, "the token of node "+name+" was revoked")
	return map[string]any{"ok": true}, nil
}

func (s *Server) nodeDelete(w http.ResponseWriter, r *http.Request) (any, error) {
	name := r.PathValue("name")
	cfg := s.app.EffectiveConfig()
	if cfg.FindNode(name) == nil {
		return nil, notFound("node")
	}
	st, err := s.editConfig(r, "delete node "+name, ifMatch(r, name, func(f *config.File) error { return f.DeleteRecord(name) }))
	if err != nil {
		return nil, s.configError(err)
	}
	if err := s.app.Store().DeleteAgentToken(name); err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	s.app.Cluster().Disconnect(name, "node "+name+" was removed")
	return s.saved(st), nil
}

// nodeUpdate edits a node's NODE record: description, region, whether it
// probes, the policy of its offline alerts, its anchors.
func (s *Server) nodeUpdate(w http.ResponseWriter, r *http.Request) (any, error) {
	name := r.PathValue("name")
	var req struct {
		Title   string `json:"title"`
		Region  string `json:"region"`
		Probe   *bool  `json:"probe"`
		Policy  string `json:"policy"`
		Anchors string `json:"anchors"`
		Timeout string `json:"node_timeout"`
		Standby string `json:"standby"`
		Message string `json:"message"`
	}
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	cur := s.app.Config()
	if req.Standby != "" && name == cur.Config.MName {
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "the master itself cannot be its standby")
	}
	if cur.Config.FindNode(name) == nil && name != cur.Config.MName {
		return nil, notFound("node")
	}
	spec := config.RecordSpec{Name: name, Type: "NODE", Target: req.Title}
	// Keep options this form does not edit (local-detect, ...).
	if rec, ok := cur.File.Record(name); ok {
		for _, o := range rec.Opts {
			switch o.Key {
			case "region", "probe", "policy", "anchors", "node-timeout", "standby":
			default:
				spec.Opts = append(spec.Opts, config.Opt{Key: o.Key, Value: o.Value})
			}
		}
	}
	for _, kv := range [][2]string{{"region", req.Region}, {"policy", req.Policy}, {"anchors", req.Anchors}, {"node-timeout", req.Timeout}, {"standby", strings.TrimRight(strings.TrimSpace(req.Standby), "/")}} {
		if kv[1] != "" {
			spec.Opts = append(spec.Opts, config.Opt{Key: kv[0], Value: kv[1]})
		}
	}
	if req.Probe != nil && !*req.Probe {
		spec.Opts = append(spec.Opts, config.Opt{Key: "probe", Value: "no"})
	}
	msg := req.Message
	if msg == "" {
		msg = "edit node " + name
	}
	st, err := s.editConfig(r, msg, ifMatch(r, spec.Name, func(f *config.File) error { return f.UpsertRecord(spec) }))
	if err != nil {
		return nil, s.configError(err)
	}
	return s.savedRecord(st, spec.Name), nil
}

// nodeHandover makes a standby node the master; this server steps down.
func (s *Server) nodeHandover(w http.ResponseWriter, r *http.Request) (any, error) {
	name := r.PathValue("name")
	term, err := s.app.HandOver(name, authOf(r).user)
	if err != nil {
		return nil, Errorf(http.StatusConflict, CodeConflict, "%v", err)
	}
	auditOf(r).note("term", term)
	return map[string]any{"node": name, "term": term}, nil
}

// localNodeJSON is one node in a replica's own view, with durations in
// milliseconds like every other API field.
type localNodeJSON struct {
	Node     string    `json:"node"`
	Online   bool      `json:"online"`
	Since    time.Time `json:"since"`
	LastSeen time.Time `json:"last_seen"`
	RTTMS    float64   `json:"rtt_ms"`
	SkewMS   float64   `json:"skew_ms"`
	Serial   uint32    `json:"serial"`
	Version  string    `json:"version,omitempty"`
	Verdict  string    `json:"verdict,omitempty"`
}

// localNodes is a replica's majority view of the other nodes while it
// checks on its own (empty otherwise).
func (s *Server) localNodes() []localNodeJSON {
	out := []localNodeJSON{}
	if !s.app.ReplicaStatus().Local {
		return out
	}
	for _, n := range s.app.Engine().Nodes() {
		out = append(out, localNodeJSON{Node: n.Node, Online: n.Online, Since: n.Since, LastSeen: n.LastSeen,
			RTTMS: float64(n.RTT) / 1e6, SkewMS: float64(n.Skew) / 1e6, Serial: n.Serial, Version: n.Version, Verdict: n.Verdict})
	}
	return out
}

// orEmptyOf turns a nil list into an empty one: lists in the API are
// never null.
func orEmptyOf[T any](l []T) []T {
	if l == nil {
		return []T{}
	}
	return l
}

// healthJSON is engine.Health for the API: round-trip times in
// milliseconds, times null when unknown. engine.Health itself keeps its
// shape, as nodes of other releases send it.
type healthJSON struct {
	Uplink         string     `json:"uplink"`
	UplinkSince    *time.Time `json:"uplink_since"`
	Suspected      bool       `json:"suspected"`
	Gateway        string     `json:"gateway"`
	GatewayOK      bool       `json:"gateway_ok"`
	GatewayRTTMS   float64    `json:"gateway_rtt_ms"`
	GatewayNote    string     `json:"gateway_note"`
	GatewayState   string     `json:"gateway_state"`
	WANDomains     int        `json:"wan_domains"`
	WANAnchors     int        `json:"wan_anchors"`
	DNSDown        bool       `json:"dns_down"`
	CanJudgeUplink bool       `json:"can_judge_uplink"`
	Exit           string     `json:"exit"`
	ExitOK         bool       `json:"exit_ok"`
	ExitRTTMS      float64    `json:"exit_rtt_ms"`
	ExitAt         *time.Time `json:"exit_at"`
	ExitIdle       bool       `json:"exit_idle"`
}

func healthOf(h *engine.Health) *healthJSON {
	if h == nil {
		return nil
	}
	return &healthJSON{Uplink: h.Uplink, UplinkSince: tp(h.UplinkSince), Suspected: h.Suspected, Gateway: h.Gateway, GatewayOK: h.GatewayOK,
		GatewayRTTMS: float64(h.GatewayRTT) / 1e6, GatewayNote: h.GatewayNote, GatewayState: h.GatewayState, WANDomains: h.WANDomains,
		WANAnchors: h.WANAnchors, DNSDown: h.DNSDown, CanJudgeUplink: h.CanJudgeUplink, Exit: h.Exit, ExitOK: h.ExitOK,
		ExitRTTMS: float64(h.ExitRTT) / 1e6, ExitAt: tp(h.ExitAt), ExitIdle: h.ExitIdle}
}

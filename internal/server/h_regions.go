package server

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/githubflyideas/unagikuma/internal/config"
)

// Regions group the nodes that check a monitor. A region is the region=
// option of NODE records; global, the default, is every node.

type regionJSON struct {
	Name     string   `json:"name"`
	Nodes    []string `json:"nodes"`
	Monitors int      `json:"monitors"`
	Builtin  bool     `json:"builtin,omitempty"`
}

func regionNames(cfg *config.Config) map[string][]string {
	out := map[string][]string{}
	for _, n := range cfg.Nodes {
		if n.Region != "" {
			out[n.Region] = append(out[n.Region], n.Name)
		}
	}
	return out
}

func (s *Server) regions(w http.ResponseWriter, r *http.Request) (any, error) {
	cfg := s.app.EffectiveConfig()
	byName := regionNames(cfg)
	count := map[string]int{}
	for _, m := range cfg.Monitors {
		if seesMonitor(r, m.Name) {
			count[m.Region]++
		}
	}
	all := []string{}
	for _, n := range cfg.Nodes {
		all = append(all, n.Name)
	}
	out := []regionJSON{{Name: config.RegionGlobal, Nodes: all, Monitors: count[config.RegionGlobal], Builtin: true}}
	names := make([]string, 0, len(byName))
	for k := range byName {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		out = append(out, regionJSON{Name: k, Nodes: byName[k], Monitors: count[k]})
	}
	return map[string]any{"items": out}, nil
}

// regionSave puts the given nodes, and only them, in a region.
func (s *Server) regionSave(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Name  string   `json:"name"`
		Nodes []string `json:"nodes"`
	}
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	if !config.ValidName(req.Name) || req.Name == config.RegionGlobal {
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "a region needs a valid name other than global")
	}
	if len(cleanList(req.Nodes)) == 0 {
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "a region needs at least one node")
	}
	cfg := s.app.EffectiveConfig()
	want := map[string]bool{}
	for _, n := range cleanList(req.Nodes) {
		if cfg.FindNode(n) == nil {
			return nil, Errorf(http.StatusBadRequest, CodeInvalid, "no node %s", n)
		}
		want[n] = true
	}
	st, err := s.editConfig(r, "region "+req.Name, func(f *config.File) error {
		for _, n := range cfg.Nodes {
			switch {
			case want[n.Name] && n.Region != req.Name:
				if err := f.SetOpts(n.Name, map[string]string{"region": req.Name}); err != nil {
					return err
				}
			case !want[n.Name] && n.Region == req.Name:
				if err := f.SetOpts(n.Name, map[string]string{"region": ""}); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, s.configError(err)
	}
	return s.saved(st), nil
}

// regionDelete moves the region's monitors to global, then takes the
// region off its nodes.
func (s *Server) regionDelete(w http.ResponseWriter, r *http.Request) (any, error) {
	name := r.PathValue("name")
	cfg := s.app.EffectiveConfig()
	nodes, ok := regionNames(cfg)[name]
	if !ok {
		return nil, notFound("region")
	}
	var moved []*config.Monitor
	for _, m := range cfg.Monitors {
		if m.Region == name {
			moved = append(moved, m)
		}
	}
	msg := fmt.Sprintf("delete region %s (%d monitors to global)", name, len(moved))
	st, err := s.editConfig(r, msg, func(f *config.File) error {
		for _, m := range moved {
			if err := f.SetOpts(m.Name, regionOpts(m, config.RegionGlobal)); err != nil {
				return err
			}
		}
		for _, n := range nodes {
			if err := f.SetOpts(n, map[string]string{"region": ""}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, s.configError(err)
	}
	return s.saved(st), nil
}

// regionOpts are the option changes that move a monitor to a region.
func regionOpts(m *config.Monitor, region string) map[string]string {
	set := map[string]string{"nodes": "", "region": region}
	if region == config.RegionGlobal && m.Area == "" {
		set["region"] = "" // global is the default outside a $SCOPE
	}
	return set
}

// monitorsMove moves monitors to another team and/or region in one
// configuration change (one audit entry).
func (s *Server) monitorsMove(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Names  []string `json:"names"`
		Team   string   `json:"team"`
		Region string   `json:"region"`
	}
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	cfg := s.app.EffectiveConfig()
	if req.Team == "" && req.Region == "" {
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "choose a team or a region")
	}
	if req.Team != "" && cfg.FindTeam(req.Team) == nil {
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "no team %s", req.Team)
	}
	if _, ok := regionNames(cfg)[req.Region]; req.Region != "" && req.Region != config.RegionGlobal && !ok {
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "no region %s", req.Region)
	}
	var list []*config.Monitor
	for _, n := range cleanList(req.Names) {
		m := cfg.FindMonitor(n)
		if m == nil {
			return nil, notFound("monitor " + n)
		}
		list = append(list, m)
	}
	if len(list) == 0 {
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "no monitors")
	}
	var to []string
	if req.Team != "" {
		to = append(to, "team "+req.Team)
	}
	if req.Region != "" {
		to = append(to, "region "+req.Region)
	}
	msg := fmt.Sprintf("move %d monitors to %s", len(list), strings.Join(to, ", "))
	st, err := s.editConfig(r, msg, func(f *config.File) error {
		for _, m := range list {
			set := map[string]string{}
			if req.Region != "" {
				set = regionOpts(m, req.Region)
			}
			if req.Team != "" {
				set["team"] = req.Team
				if req.Team == config.AdminTeam {
					set["team"] = ""
				}
			}
			if err := f.SetOpts(m.Name, set); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, s.configError(err)
	}
	return map[string]any{"serial": st.Serial, "etag": st.ETag(), "moved": len(list)}, nil
}

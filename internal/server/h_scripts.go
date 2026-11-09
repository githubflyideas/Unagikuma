package server

import (
	"net/http"
	"sort"

	"github.com/githubflyideas/unagikuma/internal/probe"
)

// scriptCell is one EXEC check's script on one node.
type scriptCell struct {
	Node  string `json:"node"`
	State string `json:"state"` // ok | missing | not_executable | differs | offline
	Hash  string `json:"hash,omitempty"`
}

type scriptCheck struct {
	Monitor string       `json:"monitor"`
	Plugin  string       `json:"plugin"`
	Area    string       `json:"area,omitempty"`
	Paused  bool         `json:"paused"`
	Problem bool         `json:"problem"` // a node that should run it cannot
	Nodes   []scriptCell `json:"nodes"`
}

type nodeScripts struct {
	Node    string         `json:"node"`
	Local   bool           `json:"local"`
	Online  bool           `json:"online"`
	Scripts []probe.Script `json:"scripts"`
}

// scriptReport compares every EXEC check with the scripts each node that
// runs it reported. Scripts are copied to each node by its administrator;
// this is where a node that lacks one, or has another version, shows.
func (s *Server) scriptReport() ([]scriptCheck, []nodeScripts, int) {
	cfg := s.app.EffectiveConfig()
	self := cfg.MName
	if self == "" {
		self = "local"
	}
	inv := map[string]nodeScripts{self: {Node: self, Local: true, Online: true, Scripts: probe.Scripts()}}
	order := []string{self}
	for _, n := range s.app.Engine().Nodes() {
		if n.Node == self {
			continue
		}
		inv[n.Node] = nodeScripts{Node: n.Node, Online: n.Online, Scripts: n.Scripts}
		order = append(order, n.Node)
	}
	var checks []scriptCheck
	problems := 0
	for _, m := range cfg.Monitors {
		if m.Type != "EXEC" {
			continue
		}
		c := scriptCheck{Monitor: m.Name, Plugin: probe.PluginOf(m.Target, m.Opts), Area: m.Area, Paused: m.Disabled}
		nodes := m.Nodes
		if len(nodes) == 0 {
			nodes = []string{self}
		}
		hashes := map[string]int{}
		for _, n := range nodes {
			ns, known := inv[n]
			if !known {
				ns = inv[self] // a name the engine does not track runs here
			}
			cell := scriptCell{Node: n, State: "missing"}
			if !ns.Online {
				cell.State = "offline"
			}
			for _, sc := range ns.Scripts {
				if sc.Name == c.Plugin {
					cell.State, cell.Hash = "ok", sc.Hash
					if !sc.Exec {
						cell.State = "not_executable"
					}
					hashes[sc.Hash]++
				}
			}
			if cell.State == "missing" || cell.State == "not_executable" {
				c.Problem = true
			}
			c.Nodes = append(c.Nodes, cell)
		}
		if len(hashes) > 1 {
			// Not the version most nodes run.
			best, bestN := "", 0
			for h, n := range hashes {
				if n > bestN || (n == bestN && h < best) {
					best, bestN = h, n
				}
			}
			for i := range c.Nodes {
				if c.Nodes[i].State == "ok" && c.Nodes[i].Hash != best {
					c.Nodes[i].State = "differs"
				}
			}
		}
		if c.Problem && !c.Paused {
			problems++
		}
		checks = append(checks, c)
	}
	sort.Slice(checks, func(i, j int) bool {
		if checks[i].Problem != checks[j].Problem {
			return checks[i].Problem
		}
		return checks[i].Monitor < checks[j].Monitor
	})
	var nodes []nodeScripts
	for _, n := range order {
		ns := inv[n]
		if ns.Scripts == nil {
			ns.Scripts = []probe.Script{}
		}
		nodes = append(nodes, ns)
	}
	return checks, nodes, problems
}

func (s *Server) scripts(w http.ResponseWriter, r *http.Request) (any, error) {
	checks, nodes, problems := s.scriptReport()
	if checks == nil {
		checks = []scriptCheck{}
	}
	return map[string]any{"dirs": probe.PluginDirs(), "checks": checks, "nodes": nodes, "problems": problems}, nil
}

// scriptProblems says which nodes cannot run a monitor's script.
func (s *Server) scriptProblems(name string) []scriptCell {
	checks, _, _ := s.scriptReport()
	for _, c := range checks {
		if c.Monitor != name {
			continue
		}
		var bad []scriptCell
		for _, n := range c.Nodes {
			if n.State == "missing" || n.State == "not_executable" {
				bad = append(bad, n)
			}
		}
		return bad
	}
	return nil
}

// scriptCount is the number of enabled script checks some node cannot
// run, for the warning badge.
func (s *Server) scriptCount() int {
	_, _, n := s.scriptReport()
	return n
}

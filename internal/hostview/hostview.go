// Package hostview groups monitors (services) into hosts and derives a
// host state from them, the way operations consoles present a network:
// a host is UP or DOWN by its ping, and lists the services checking it.
package hostview

import "sort"

// Service is one monitor as seen from its host.
type Service struct {
	Name    string
	Host    string // host key: device= or the probed host
	Address string
	Area    string
	Group   string
	Ping    bool // decides the host state
	Status  string
}

// Host is one device or address with the services checking it.
type Host struct {
	Name     string         `json:"name"`
	Address  string         `json:"address"`
	Status   string         `json:"status"`          // up | down | unreachable | pending | maintenance | paused
	Check    string         `json:"check,omitempty"` // the PING monitor that decides the host state
	Services map[string]int `json:"services"`        // monitor count per status
	Total    int            `json:"total"`
	Worst    string         `json:"worst"` // worst service status
	Area     string         `json:"area,omitempty"`
	Group    string         `json:"group,omitempty"`
	Monitors []string       `json:"monitors"`
}

// Severity orders service states from best to worst.
var Severity = map[string]int{
	"ok": 0, "paused": 1, "pending": 2, "maintenance": 3, "degraded": 4, "unreachable": 5, "unknown": 6, "critical": 7,
}

// Build groups services by host, keeping the first-seen address, area and
// group, and returns the hosts sorted by name.
func Build(svcs []Service) []Host {
	byKey := map[string]*Host{}
	var order []string
	status := map[string]string{}
	for _, s := range svcs {
		st := s.Status
		if st == "" {
			st = "pending"
		}
		status[s.Name] = st
		h := byKey[s.Host]
		if h == nil {
			h = &Host{Name: s.Host, Address: s.Address, Services: map[string]int{}, Area: s.Area, Group: s.Group, Worst: "ok"}
			byKey[s.Host] = h
			order = append(order, s.Host)
		}
		h.Services[st]++
		h.Total++
		h.Monitors = append(h.Monitors, s.Name)
		if Severity[st] > Severity[h.Worst] {
			h.Worst = st
		}
		if h.Area != s.Area {
			h.Area = "" // services in several sections
		}
		if h.Address == "" {
			h.Address = s.Address
		}
		if h.Check == "" && s.Ping {
			h.Check = s.Name
		}
	}
	out := make([]Host, 0, len(order))
	for _, key := range order {
		h := byKey[key]
		h.Status = state(h, status)
		out = append(out, *h)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// state maps a host's ping (or, without one, its services) to
// UP / DOWN / UNREACHABLE.
func state(h *Host, status map[string]string) string {
	st := ""
	if h.Check != "" {
		st = status[h.Check]
	} else {
		// No ping: down only when every service is down.
		switch {
		case h.Services["critical"] == h.Total:
			st = "critical"
		case h.Services["unreachable"]+h.Services["critical"] == h.Total && h.Services["unreachable"] > 0:
			st = "unreachable"
		case h.Services["pending"] == h.Total:
			st = "pending"
		case h.Services["maintenance"] == h.Total:
			st = "maintenance"
		case h.Services["paused"] == h.Total:
			st = "paused"
		default:
			st = "ok"
		}
	}
	switch st {
	case "critical":
		return "down"
	case "ok", "degraded", "unknown":
		return "up"
	case "":
		return "pending"
	}
	return st
}

package notify

import (
	"strings"
	"testing"
)

// Every site sends its own messages; each one names the site.
func TestSiteInMessages(t *testing.T) {
	m := &Message{Kind: "PROBLEM", Monitor: "web", Status: "critical", Reason: "timeout", Lang: "en", Site: "Tokyo DC"}
	title, body := Render(m)
	if !strings.Contains(title, "[Tokyo DC]") || !strings.Contains(body, "Site: Tokyo DC") {
		t.Fatalf("site missing: %q / %q", title, body)
	}
	m = &Message{Kind: "SYSTEM", Title: "x", Body: "y", Lang: "en", Site: "Osaka"}
	if title, body = Render(m); !strings.Contains(title, "[Osaka]") || !strings.Contains(body, "Site: Osaka") {
		t.Fatalf("system site missing: %q / %q", title, body)
	}
	m = &Message{Kind: "PROBLEM", Monitor: "web", Lang: "en"}
	if title, body = Render(m); strings.Count(title, "[") > 1 || strings.Contains(body, "Site:") {
		t.Fatalf("single machine shows a site: %q / %q", title, body)
	}
}

// In a cluster every message names its site, the master's too when it
// kept the product's own name: otherwise its alerts are the only ones that
// cannot be told apart.
func TestDefaultSiteNameIsNamed(t *testing.T) {
	m := &Message{Kind: "SYSTEM", Title: "Peer b unreachable", Body: "y", Lang: "en", Site: "unagikuma"}
	if title, _ := Render(m); !strings.Contains(title, "[unagikuma]") {
		t.Fatalf("title %q", title)
	}
	m = &Message{Kind: "PROBLEM", Monitor: "web", Status: "critical", Reason: "timeout", Lang: "en", Site: "unagikuma"}
	if title, _ := Render(m); !strings.Contains(title, "[unagikuma]") {
		t.Fatalf("title %q", title)
	}
}

// A white-label brand replaces the product name in subjects and bodies.
func TestBrandReplacesProductName(t *testing.T) {
	m := &Message{Kind: "SYSTEM", Title: "Unagikuma restarted", Body: "Unagikuma was down", Lang: "en", Brand: "Acme Monitor"}
	title, body := Render(m)
	if strings.Contains(title+body, "Unagikuma") || !strings.Contains(title, "[Acme Monitor]") || !strings.Contains(body, "Acme Monitor was down") {
		t.Fatalf("%q / %q", title, body)
	}
	m = &Message{Kind: "PROBLEM", Monitor: "web", Status: "critical", Lang: "en", Brand: "Acme"}
	if title, _ := Render(m); strings.Contains(title, "Unagikuma") || !strings.Contains(title, "[Acme]") {
		t.Fatalf("%q", title)
	}
}

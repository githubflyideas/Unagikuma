package server

import (
	"fmt"
	"testing"

	"github.com/githubflyideas/unagikuma/internal/store"
)

const peopleBind = `@ IN SOA unagikuma. admin.localhost. ( 2026102801 1m 15s 5s 2 )
$SET api.login-rate=100/s
mail    IN CHANNEL email host=127.0.0.1 port=25 tls=none from=mon@example.com to="Li Lei <li@example.com>, wang@example.com"
sms     IN CHANNEL aliyun-sms access-key-id=a access-key-secret=b sign-name=c template-code=d phones=13800000002
default IN POLICY chain=mail,sms
`

// The addresses the channels use were added to the directory at the first
// start. A new address outside it is left out and flagged until it is
// imported; someone on leave is left out too, and someone who left also
// loses their login.
func TestContactDirectory(t *testing.T) {
	e := newEnv(t, peopleBind, nil)
	e.setup()
	type ch struct {
		Name   string
		Issues []struct{ Code, Address string }
	}
	issues := func() map[string][]string {
		var list []ch
		e.do("GET", "/api/v1/channels", nil, &list)
		out := map[string][]string{}
		for _, c := range list {
			for _, i := range c.Issues {
				out[c.Name] = append(out[c.Name], i.Code)
			}
		}
		return out
	}
	if got := issues(); len(got) != 0 {
		t.Fatalf("after the first start: %v", got)
	}
	mail := map[string]any{"name": "mail", "kind": "email", "opts": map[string]string{"host": "127.0.0.1", "port": "25", "tls": "none", "from": "mon@example.com",
		"to": "Li Lei <li@example.com>, wang@example.com, new@example.com"}}
	if c := e.do("POST", "/api/v1/channels", mail, nil); c != 200 {
		t.Fatalf("channel: %d", c)
	}
	if got := issues(); len(got["mail"]) != 1 || got["mail"][0] != "unknown_address" {
		t.Fatalf("a new address: %v", got)
	}
	// The policy became the admin team's channels at startup.
	var teams struct {
		Items []struct {
			Name          string
			IssueChannels []string `json:"issue_channels"`
		}
	}
	e.do("GET", "/api/v1/teams", nil, &teams)
	if len(teams.Items) == 0 || teams.Items[0].Name != "admin" || len(teams.Items[0].IssueChannels) != 1 {
		t.Fatalf("team red dot: %+v", teams)
	}
	var imp struct{ Created int }
	if c := e.do("POST", "/api/v1/people/import", map[string]any{}, &imp); c != 200 || imp.Created != 1 {
		t.Fatalf("import: %d %+v", c, imp)
	}
	if got := issues(); len(got) != 0 {
		t.Fatalf("after import: %v", got)
	}
	var dir struct {
		People []struct {
			ID       int64
			Handle   string
			Name     string
			Status   string
			Channels []string
		}
	}
	e.do("GET", "/api/v1/people", nil, &dir)
	var li int64
	for _, p := range dir.People {
		if p.Handle == "li" {
			li = p.ID
			if p.Name != "Li Lei" || len(p.Channels) != 1 || p.Status != "active" {
				t.Fatalf("imported %+v", p)
			}
		}
	}
	// Link Li to a login, then Li leaves.
	if c := e.do("POST", "/api/v1/users", map[string]any{"username": "li", "password": "secret123", "role": "operator"}, nil); c != 200 {
		t.Fatalf("user: %d", c)
	}
	if c := e.do("PUT", fmt.Sprintf("/api/v1/people/%d", li), map[string]any{"handle": "li", "name": "Li Lei", "username": "li", "emails": []string{"li@example.com"}, "notify": []string{"mail"}}, nil); c != 200 {
		t.Fatalf("link: %d", c)
	}
	liWeb := e.loginAs("li", "secret123")
	// On leave: not sent to, but still signs in.
	if c := e.do("POST", fmt.Sprintf("/api/v1/people/%d/status", li), map[string]any{"status": "leave"}, nil); c != 204 && c != 200 {
		t.Fatalf("leave: %d", c)
	}
	if got := issues(); len(got["mail"]) != 1 || got["mail"][0] != "inactive" {
		t.Fatalf("on leave: %v", got)
	}
	if c := liWeb.do("GET", "/api/v1/overview", nil, nil); c != 200 {
		t.Fatalf("session of someone on leave: %d", c)
	}
	if c := e.do("POST", fmt.Sprintf("/api/v1/people/%d/status", li), map[string]any{"status": "left"}, nil); c != 204 && c != 200 {
		t.Fatalf("left: %d", c)
	}
	if got := issues(); len(got["mail"]) != 1 || got["mail"][0] != "left" {
		t.Fatalf("after leaving: %v", got)
	}
	if c := liWeb.do("GET", "/api/v1/overview", nil, nil); c != 401 {
		t.Fatalf("session of someone who left: %d", c)
	}
	if c := e.do("POST", "/api/v1/session", map[string]string{"username": "li", "password": "secret123"}, nil); c == 200 {
		t.Fatal("someone who left can still log in")
	}
	// The channel form's check before saving.
	var lk struct {
		Unknown      []string
		Issues       []struct{ Code, Address string }
		NoRecipients bool `json:"no_recipients"`
	}
	// Li left and the other address is unknown: nobody would be sent to.
	// Every issue names its address; the empty list is a field of its own.
	e.do("POST", "/api/v1/people/lookup", map[string]any{"kind": "email", "list": "li@example.com, other@example.com"}, &lk)
	if len(lk.Unknown) != 1 || lk.Unknown[0] != "other@example.com" || len(lk.Issues) != 2 || !lk.NoRecipients {
		t.Fatalf("lookup: %+v", lk)
	}
	for _, is := range lk.Issues {
		if is.Address == "" {
			t.Fatalf("an issue without an address: %+v", lk.Issues)
		}
	}
}

// A contact can always be reached: at least one valid address, and the
// channels to reach them through, which must suit their addresses.
func TestContactReachable(t *testing.T) {
	e := newEnv(t, peopleBind, nil)
	e.setup()
	save := func(body map[string]any) int { return e.do("POST", "/api/v1/people", body, nil) }
	for name, c := range map[string]struct {
		body map[string]any
		code int
	}{
		"no address":          {map[string]any{"name": "Zhao", "handle": "zhao", "notify": []string{"mail"}}, 400},
		"bad e-mail":          {map[string]any{"name": "Zhao", "handle": "zhao", "emails": []string{"zhao@"}, "notify": []string{"mail"}}, 400},
		"bad phone":           {map[string]any{"name": "Zhao", "handle": "zhao", "phones": []string{"12"}, "notify": []string{"sms"}}, 400},
		"no channel":          {map[string]any{"name": "Zhao", "handle": "zhao", "emails": []string{"zhao@example.com"}}, 400},
		"unknown channel":     {map[string]any{"name": "Zhao", "handle": "zhao", "emails": []string{"zhao@example.com"}, "notify": []string{"pager"}}, 400},
		"sms without a phone": {map[string]any{"name": "Zhao", "handle": "zhao", "emails": []string{"zhao@example.com"}, "notify": []string{"sms"}}, 400},
	} {
		if got := save(c.body); got != c.code {
			t.Errorf("%s: %d, want %d", name, got, c.code)
		}
	}
	if c := save(map[string]any{"name": "Zhao", "handle": "zhao", "emails": []string{"Zhao <zhao@example.com>"}, "phones": []string{"+86 138 0000 0009"}, "notify": []string{"mail", "sms"}}); c != 200 {
		t.Fatalf("valid contact: %d", c)
	}
	// Someone saved before this rule, with no way chosen: flagged.
	if _, err := e.app.Store().SavePerson(store.Person{Handle: "old", Name: "Old", Emails: []string{"old@example.com"}}); err != nil {
		t.Fatal(err)
	}
	e.app.DirectoryChanged()
	var dir struct {
		People []struct {
			Handle string
			Notify []string
			Reach  string
		}
		Personal []struct{ Name, Contact string } `json:"personal_channels"`
	}
	e.do("GET", "/api/v1/people", nil, &dir)
	reach := map[string]string{}
	for _, p := range dir.People {
		reach[p.Handle] = p.Reach
	}
	if reach["zhao"] != "" || reach["old"] != "no_channel" {
		t.Errorf("reach: %v", reach)
	}
	if len(dir.Personal) != 2 {
		t.Errorf("personal channels: %+v", dir.Personal)
	}
	// Importing the addresses the channels use reaches each through them.
	var imp struct{ Created int }
	e.do("POST", "/api/v1/people/import", map[string]any{}, &imp)
	e.do("GET", "/api/v1/people", nil, &dir)
	for _, p := range dir.People {
		if p.Handle == "li" && (len(p.Notify) != 1 || p.Notify[0] != "mail" || p.Reach != "") {
			t.Errorf("imported li: %+v", p)
		}
	}
}

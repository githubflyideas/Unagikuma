package app

import (
	"bufio"
	"context"
	"io"
	"net"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/store"
)

// smtpRecorder accepts every message and records its recipients and
// subject.
type smtpRecorder struct {
	mu   sync.Mutex
	msgs []smtpMsg
}

type smtpMsg struct {
	to      []string
	subject string
}

func (s *smtpRecorder) listen(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	return l.Addr().String()
}

func (s *smtpRecorder) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	w := func(l string) { io.WriteString(c, l+"\r\n") }
	w("220 fake ESMTP")
	var cur smtpMsg
	inData := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		if inData {
			if line == ".\r\n" {
				inData = false
				s.mu.Lock()
				s.msgs = append(s.msgs, cur)
				s.mu.Unlock()
				cur = smtpMsg{}
				w("250 queued")
			} else if h, ok := strings.CutPrefix(line, "Subject: "); ok && cur.subject == "" {
				cur.subject = strings.TrimSpace(h)
			}
			continue
		}
		cmd := strings.TrimSpace(line)
		switch up := strings.ToUpper(cmd); {
		case strings.HasPrefix(up, "RCPT TO:"):
			cur.to = append(cur.to, strings.ToLower(strings.Trim(cmd[8:], "<> ")))
			w("250 ok")
		case strings.HasPrefix(up, "DATA"):
			inData = true
			w("354 go ahead")
		case strings.HasPrefix(up, "QUIT"):
			w("221 bye")
			return
		default:
			w("250 ok")
		}
	}
}

// recipients lists who received any message so far.
func (s *smtpRecorder) recipients() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	set := map[string]bool{}
	for _, m := range s.msgs {
		for _, r := range m.to {
			set[r] = true
		}
	}
	var out []string
	for r := range set {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// When a monitor of the admin team fails, the administrators in the teams
// it affects are told, each at their own address through the channel their
// contact entry names: the teams with monitors that depend on it, or every
// team when none does. Team monitors keep to their teams.
func TestManagersHearOfSharedFailures(t *testing.T) {
	rec := &smtpRecorder{}
	host, port, _ := net.SplitHostPort(rec.listen(t))
	a, fp, _, _ := newApp(t, `$FILE revision=1 interval=1s retry=500ms timeout=300ms attempts=1
$SET engine.first-round-delay=0s suspend.recheck-spread=0s notify.backstop-max=50
mail    IN CHANNEL email host=`+host+` port=`+port+` tls=none from=mon@example.com to=ops@example.com
default IN POLICY chain=mail
web     IN TEAM members=li,zhang
db      IN TEAM members=wang
gw      IN TCP 10.0.0.1:443
dns     IN TCP 10.0.0.53:53
shop    IN TCP 10.0.0.2:443 team=web parent=gw
pg      IN TCP 10.0.0.3:5432 team=db
`)
	for _, p := range []store.Person{
		{Handle: "li", Name: "Li", Username: "li", Emails: []string{"li@example.com"}, Notify: []string{"mail"}},
		{Handle: "wang", Name: "Wang", Username: "wang", Emails: []string{"wang@example.com"}, Notify: []string{"mail"}},
		{Handle: "zhang", Name: "Zhang", Username: "zhang", Emails: []string{"zhang@example.com"}, Notify: []string{"mail"}},
	} {
		if _, err := a.Store().SavePerson(p); err != nil {
			t.Fatal(err)
		}
	}
	for u, role := range map[string]string{"li": store.RoleAdmin, "wang": store.RoleAdmin, "zhang": store.RoleOperator} {
		if _, err := a.Store().CreateUserRole(u, "secret123", role, nil); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	defer func() { cancel(); <-done }()
	a.DirectoryChanged()

	wait := func(what string, want ...string) {
		t.Helper()
		sort.Strings(want)
		deadline := time.Now().Add(15 * time.Second)
		for {
			got := rec.recipients()
			if strings.Join(got, ",") == strings.Join(want, ",") {
				return
			}
			if time.Now().After(deadline) {
				for id := int64(1); id < 6; id++ {
					ds, _ := a.Store().Decisions(id, "", time.Time{}, time.Now())
					for _, d := range ds {
						t.Logf("%d %s %s %s %s %v %s", id, d.IntentID, d.Monitor, d.Kind, d.Step, d.Recipients, d.Note)
					}
				}
				t.Fatalf("%s: recipients %v, want %v", what, got, want)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	// gw: web's shop depends on it, so web's manager hears of it, not db's.
	fp.set("gw", true)
	wait("gw down", "ops@example.com", "li@example.com")
	// dns: nothing depends on it, so every team's manager hears of it.
	fp.set("dns", true)
	wait("dns down", "ops@example.com", "li@example.com", "wang@example.com")
	// A team monitor goes through the usual policy only.
	rec.mu.Lock()
	rec.msgs = nil
	rec.mu.Unlock()
	fp.set("pg", true)
	wait("pg down", "ops@example.com")
	time.Sleep(2 * time.Second)
	if got := rec.recipients(); strings.Join(got, ",") != "ops@example.com" {
		t.Fatalf("a team monitor's failure reached %v", got)
	}
	// The managers who were told of a failure are told of its recovery.
	rec.mu.Lock()
	rec.msgs = nil
	rec.mu.Unlock()
	fp.set("gw", false)
	wait("gw recovered", "ops@example.com", "li@example.com")
}

// A team's monitors are notified through its channels. When they all fail,
// the notice goes to the system channels and the team's administrators are
// told; a team with no channels goes to the system channels, and its
// administrators (every administrator when it has none) are told too.
func TestTeamChannelsFallBackToSystem(t *testing.T) {
	rec := &smtpRecorder{}
	host, port, _ := net.SplitHostPort(rec.listen(t))
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, deadPort, _ := net.SplitHostPort(l.Addr().String())
	l.Close() // nothing answers there
	a, fp, _, _ := newApp(t, `$FILE revision=1 interval=1s retry=500ms timeout=300ms attempts=1
$SET engine.first-round-delay=0s suspend.recheck-spread=0s notify.backstop-max=50
mail    IN CHANNEL email host=`+host+` port=`+port+` tls=none from=mon@example.com to=ops@example.com system=yes
broken  IN CHANNEL email host=127.0.0.1 port=`+deadPort+` tls=none from=mon@example.com to=web@example.com
web     IN TEAM members=li channels=broken
db      IN TEAM members=zhang
shop    IN TCP 10.0.0.2:443 team=web
pg      IN TCP 10.0.0.3:5432 team=db
`)
	if _, err := a.Store().SavePerson(store.Person{Handle: "li", Name: "Li", Username: "li", Emails: []string{"li@example.com"}, Notify: []string{"mail"}}); err != nil {
		t.Fatal(err)
	}
	for u, role := range map[string]string{"li": store.RoleAdmin, "zhang": store.RoleOperator} {
		if _, err := a.Store().CreateUserRole(u, "secret123", role, nil); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	defer func() { cancel(); <-done }()
	a.DirectoryChanged()
	wait := func(what string, want ...string) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for {
			got := rec.recipients()
			if strings.Join(got, ",") == strings.Join(want, ",") {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: recipients %v, want %v", what, got, want)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	fp.set("shop", true)
	wait("web's channel failed", "li@example.com", "ops@example.com")
	rec.mu.Lock()
	rec.msgs = nil
	rec.mu.Unlock()
	fp.set("pg", true)
	wait("db has no channels", "li@example.com", "ops@example.com")
}

// With contacts.notify-admins, a team's administrators hear once that a
// channel left recipients out (here someone on leave).
func TestAdminsHearOfLeftOutRecipients(t *testing.T) {
	rec := &smtpRecorder{}
	host, port, _ := net.SplitHostPort(rec.listen(t))
	a, fp, _, _ := newApp(t, `$FILE revision=1 interval=1s retry=500ms timeout=300ms attempts=1
$SET engine.first-round-delay=0s suspend.recheck-spread=0s notify.backstop-max=50 contacts.notify-admins=on
mail    IN CHANNEL email host=`+host+` port=`+port+` tls=none from=mon@example.com to="ops@example.com, temp@example.com"
web     IN TEAM members=li channels=mail
shop    IN TCP 10.0.0.2:443 team=web
`)
	st := a.Store()
	people, _ := st.People()
	for _, p := range people {
		if p.Handle == "temp" {
			if err := st.SetStatus(p.ID, store.StatusLeave); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := st.SavePerson(store.Person{Handle: "li", Name: "Li", Username: "li", Emails: []string{"li@example.com"}, Notify: []string{"mail"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUserRole("li", "secret123", store.RoleAdmin, nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	defer func() { cancel(); <-done }()
	a.DirectoryChanged()
	fp.set("shop", true)
	deadline := time.Now().Add(20 * time.Second)
	for {
		rec.mu.Lock()
		var toOps, toTemp, told bool
		for _, m := range rec.msgs {
			for _, r := range m.to {
				toOps = toOps || r == "ops@example.com"
				toTemp = toTemp || r == "temp@example.com"
				told = told || r == "li@example.com" && strings.Contains(m.subject, "left_out")
			}
		}
		rec.mu.Unlock()
		if toTemp {
			t.Fatal("someone on leave was sent to")
		}
		if toOps && told {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("ops %v, admins told %v: %+v", toOps, told, rec.msgs)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Each team on notify.daily-report-teams gets the report on its own
// monitors through its channels; a person who switched it off is left
// out, and so is a channel with report=no.
func TestDailyReportPerTeam(t *testing.T) {
	rec := &smtpRecorder{}
	host, port, _ := net.SplitHostPort(rec.listen(t))
	a, _, _, _ := newApp(t, `$FILE revision=1 interval=1s retry=500ms timeout=300ms attempts=1
$SET engine.first-round-delay=0s suspend.recheck-spread=0s notify.daily-report-teams=admin,web
mail    IN CHANNEL email host=`+host+` port=`+port+` tls=none from=mon@example.com to=ops@example.com,boss@example.com system=yes
webmail IN CHANNEL email host=`+host+` port=`+port+` tls=none from=mon@example.com to=web@example.com
quiet   IN CHANNEL email host=`+host+` port=`+port+` tls=none from=mon@example.com to=quiet@example.com report=no
web     IN TEAM members=li channels=webmail,quiet mode=all
shop    IN TCP 10.0.0.2:443 team=web
pg      IN TCP 10.0.0.3:5432
`)
	// The channels' addresses were taken into the directory on upgrade.
	ppl, _ := a.Store().People()
	found := 0
	for _, p := range ppl {
		if slices.Contains(p.Emails, "boss@example.com") {
			found++
			if err := a.Store().SetReport(p.ID, false); err != nil {
				t.Fatal(err)
			}
		}
	}
	if found != 1 || len(ppl) != 4 {
		t.Fatalf("directory %+v", ppl)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	defer func() { cancel(); <-done }()
	a.DirectoryChanged()
	if got := strings.Join(a.Engine().ReportTeams(), ","); got != "admin,web" {
		t.Fatalf("report teams %q", got)
	}
	web := a.Engine().DailyReport("web")
	if !strings.HasPrefix(web.Title, "[web] ") || !strings.Contains(web.Body, "10.0.0.2") || strings.Contains(web.Body, "10.0.0.3") || web.Policy != "team:web" {
		t.Fatalf("web's report: %+v", web)
	}
	if all := a.Engine().DailyReport(""); !strings.Contains(all.Body, "10.0.0.2") || !strings.Contains(all.Body, "10.0.0.3") || all.Policy != "system" {
		t.Fatalf("admin's report: %+v", all)
	}
	if n := len(a.Engine().SendDailyReport("")); n != 2 {
		t.Fatalf("sent %d reports", n)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		got := strings.Join(rec.recipients(), ",")
		if got == "ops@example.com,web@example.com" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("report recipients %s", got)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

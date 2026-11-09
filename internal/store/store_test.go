package store

import (
	"bytes"
	"math"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/engine"
	"github.com/githubflyideas/unagikuma/internal/notify"
	"github.com/githubflyideas/unagikuma/internal/probe"
)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func open(t *testing.T) (*Store, *clock, string) {
	t.Helper()
	dir := t.TempDir()
	c := &clock{t: t0}
	s, err := Open(dir, Options{Now: c.now, CommitInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, c, dir
}

func TestOpenCleanShutdown(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !s.FirstRun || !s.PrevClean {
		t.Fatalf("first run: %+v", s)
	}
	s.Beat()
	// Simulate a crash: drop the handles without Close.
	s.batch.stop()
	s.core.Close()
	s.hist.Close()
	s, err = Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if s.FirstRun || s.PrevClean || s.PrevBeat.IsZero() {
		t.Fatalf("after crash: first=%v clean=%v beat=%v", s.FirstRun, s.PrevClean, s.PrevBeat)
	}
	s.Close()
	s, _ = Open(dir, Options{})
	if !s.PrevClean {
		t.Fatal("clean close not recorded")
	}
	s.Close()
}

func TestStatesIncidentsIntents(t *testing.T) {
	s, _, _ := open(t)
	st := engine.NewState()
	st.Avail, st.Attempt = engine.StatusCritical, 2
	s.SaveState(engine.StateRecord{Name: "web", State: st, Status: "critical", Reason: "timeout", LastCheck: t0, DefHash: "h1"})
	states, err := s.States()
	if err != nil || len(states) != 1 || states[0].State.Attempt != 2 || states[0].DefHash != "h1" || !states[0].LastCheck.Equal(t0) {
		t.Fatalf("%v %+v", err, states)
	}
	s.DeleteState("web")
	if states, _ = s.States(); len(states) != 0 {
		t.Fatal("delete")
	}

	inc := &engine.Incident{Monitor: "web", Kind: engine.IncidentNormal, Policy: "default", Title: "官网", State: engine.IncidentState{OpenedAt: t0, NotifyCount: 1}}
	if err := s.SaveIncident(inc); err != nil || inc.ID == 0 {
		t.Fatalf("insert: %v %d", err, inc.ID)
	}
	inc.State.Acked, inc.State.AckedBy = true, "alice"
	if err := s.SaveIncident(inc); err != nil {
		t.Fatal(err)
	}
	open, _ := s.OpenIncidents()
	if len(open) != 1 || !open[0].State.Acked || open[0].Title != "官网" {
		t.Fatalf("%+v", open)
	}
	row, _ := s.Incident(inc.ID)
	if row == nil || row.State.AckedBy != "alice" {
		t.Fatalf("%+v", row)
	}

	in := &engine.Intent{ID: "i1", IncidentID: inc.ID, Kind: "PROBLEM", Policy: "default", State: engine.IntentPending, CreatedAt: t0, DueAt: t0,
		Message: notify.Message{Kind: "PROBLEM", Title: "官网"}, Delivery: engine.NewDelivery([]string{"tg", "mail"}, "")}
	in.Delivery.Begin(0)
	if err := s.SaveIntent(in); err != nil {
		t.Fatal(err)
	}
	pending, _ := s.PendingIntents()
	if len(pending) != 1 || pending[0].Delivery.Steps[0].State != engine.StepSending || pending[0].Message.Title != "官网" {
		t.Fatalf("%+v", pending)
	}
	in.State = engine.IntentDone
	s.SaveIntent(in)
	if pending, _ = s.PendingIntents(); len(pending) != 0 {
		t.Fatal("done intent still pending")
	}
	// A digest keeps what it stands for, an escalation its chain, and an
	// incident whether a problem notice reached anyone, across a restart.
	d := &engine.Intent{ID: "d1", Kind: "DIGEST", Policy: "default", State: engine.IntentPending, CreatedAt: t0, DueAt: t0,
		Covers: []int64{inc.ID, 42}, Chain: []string{"oncall"}, Delivery: engine.NewDelivery([]string{"oncall"}, "")}
	if err := s.SaveIntent(d); err != nil {
		t.Fatal(err)
	}
	if pending, _ = s.PendingIntents(); len(pending) != 1 || len(pending[0].Covers) != 2 || pending[0].Covers[1] != 42 || len(pending[0].Chain) != 1 {
		t.Fatalf("digest after restart: %+v", pending)
	}
	inc.State.Tracked = true
	inc.State.Delivered()
	s.SaveIncident(inc)
	if open, _ = s.OpenIncidents(); len(open) != 1 || open[0].State.Told != 1 || !open[0].State.Tracked {
		t.Fatalf("delivered record after restart: %+v", open[0].State)
	}
}

func verdict(m string, at time.Time, status string, hard bool, lat float64) engine.VerdictRecord {
	return engine.VerdictRecord{Monitor: m, At: at, Verdict: engine.VerdictOK, Status: status, Hard: hard, LatencyMS: lat, Interval: time.Minute,
		Excluded: status == "unknown" || status == "pending"}
}

func TestHistoryUptimeCells(t *testing.T) {
	s, c, _ := open(t)
	// 60 minutes up, 30 minutes hard down, 10 min soft (counts as up),
	// then a 2-hour gap, then 20 minutes up.
	at := t0
	for i := 0; i < 60; i++ {
		s.Verdict(verdict("web", at, "ok", true, 20))
		at = at.Add(time.Minute)
	}
	for i := 0; i < 30; i++ {
		s.Verdict(verdict("web", at, "critical", true, 0))
		at = at.Add(time.Minute)
	}
	for i := 0; i < 10; i++ {
		s.Verdict(verdict("web", at, "critical", false, 0))
		at = at.Add(time.Minute)
	}
	at = at.Add(2 * time.Hour)
	for i := 0; i < 20; i++ {
		s.Verdict(verdict("web", at, "ok", true, 40))
		at = at.Add(time.Minute)
	}
	s.Flush()
	c.t = at
	u, err := s.UptimeOf("web", t0, at)
	if err != nil || u.Ratio == nil {
		t.Fatalf("%v %+v", err, u)
	}
	// up: 60 + 9 soft minutes + 2.5 (the part of the gap still attributed
	// to the last soft round) + 19 = 90.5 min; down 30 min.
	wantUp := (60 + 9 + 2.5 + 19) * 60.0
	if math.Abs(u.UpS-wantUp) > 1 || math.Abs(u.DownS-1800) > 1 {
		t.Fatalf("up %.0f down %.0f", u.UpS, u.DownS)
	}
	if u.ExcludedS < 115*60 {
		t.Fatalf("gap not excluded: %.0f", u.ExcludedS)
	}
	cells, err := s.Cells("web", t0, t0.Add(2*time.Hour), 4)
	if err != nil {
		t.Fatal(err)
	}
	if cells[0].Status != "ok" || cells[1].Status != "ok" || cells[2].Status != "critical" || cells[3].Status != "critical" {
		t.Fatalf("cells %+v", cells)
	}
	long, err := s.Cells("web", t0.Add(-24*time.Hour), t0.Add(9*24*time.Hour), 10)
	if err != nil || long[1].Status != "critical" {
		t.Fatalf("aggregate cells %+v %v", long, err)
	}
	lat, _ := s.Latency("web", t0, at)
	if len(lat) != 80 || lat[0].Avg != 20 {
		t.Fatalf("latency %d %+v", len(lat), lat[:1])
	}
	hourly, _ := s.Latency("web", t0.Add(-3*24*time.Hour), at)
	if len(hourly) == 0 || hourly[0].Avg != 20 {
		t.Fatalf("hourly latency %+v", hourly)
	}
	if u, _ := s.UptimeOf("nope", t0, at); u.Ratio != nil {
		t.Fatal("no data must give no ratio")
	}
}

func TestEventsDeliveriesCountersEvidence(t *testing.T) {
	s, c, _ := open(t)
	for i := 0; i < 5; i++ {
		s.Event(engine.Event{At: t0.Add(time.Duration(i) * time.Second), Type: engine.EventState, Severity: "critical", Monitor: "web", Message: "x", Data: map[string]any{"i": i}})
	}
	s.Event(engine.Event{At: t0, Type: engine.EventConfig, Message: "applied"})
	s.Delivery(engine.DeliveryRecord{IntentID: "i1", Kind: "PROBLEM", At: t0, OK: false, Attempts: []notify.Attempt{{Channel: "tg", Error: "timeout"}}})
	s.Delivery(engine.DeliveryRecord{IntentID: "i2", Kind: "PROBLEM", At: t0, OK: true, Fallback: true})
	s.Counter(engine.CounterSoftFiltered, 2)
	s.Counter(engine.CounterSoftFiltered, 3)
	for i := 0; i < 30; i++ {
		s.Evidence("web", "local", t0, probe.Result{Vote: probe.VoteOK, Detail: "n"})
	}
	s.Flush()
	p, err := s.Events(EventFilter{Monitor: "web", Limit: 2})
	if err != nil || len(p.Items) != 2 || p.NextCursor == nil || p.Items[0].Data["i"].(float64) != 4 {
		t.Fatalf("%v %+v", err, p)
	}
	p2, _ := s.Events(EventFilter{Monitor: "web", Limit: 10, Before: *p.NextCursor})
	if len(p2.Items) != 3 || p2.NextCursor != nil {
		t.Fatalf("page 2: %+v", p2)
	}
	if p3, _ := s.Events(EventFilter{Types: []string{engine.EventConfig}}); len(p3.Items) != 1 {
		t.Fatal("type filter")
	}
	d, _ := s.Deliveries("failed", 0, 10)
	if len(d.Items) != 1 || d.Items[0].Attempts[0].Error != "timeout" {
		t.Fatalf("%+v", d)
	}
	if d, _ = s.Deliveries("fallback", 0, 10); len(d.Items) != 1 {
		t.Fatal("fallback filter")
	}
	cnt, _ := s.Counters(t0.AddDate(0, 0, -7))
	if cnt[engine.CounterSoftFiltered] != 5 {
		t.Fatalf("%v", cnt)
	}
	ev, _ := s.EvidenceOf("web", 100)
	if len(ev) != 20 {
		t.Fatalf("evidence ring kept %d", len(ev))
	}
	// Prune by retention.
	c.t = t0.AddDate(1, 0, 0)
	if err := s.Prune(Retention{Raw: 7 * 24 * time.Hour, Agg1h: 30 * 24 * time.Hour, Events: 24 * time.Hour}); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.Events(EventFilter{}); len(p.Items) != 0 {
		t.Fatal("events not pruned")
	}
}

func TestSnapshots(t *testing.T) {
	s, _, _ := open(t)
	for i := uint32(1); i <= 5; i++ {
		if err := s.SaveSnapshot(Snapshot{Serial: i, At: t0, Source: "file", Text: "v" + string(rune('0'+i)), SHA256: "x", Summary: map[string]int{"added": 1}}, 3); err != nil {
			t.Fatal(err)
		}
	}
	list, _ := s.Snapshots(10)
	if len(list) != 3 || list[0].Serial != 5 || list[0].Text != "" || list[0].Summary["added"] != 1 {
		t.Fatalf("%+v", list)
	}
	sn, err := s.SnapshotText(4)
	if err != nil || sn.Text != "v4" {
		t.Fatalf("%v %+v", err, sn)
	}
}

func TestAuth(t *testing.T) {
	s, c, _ := open(t)
	if n, _ := s.UserCount(); n != 0 {
		t.Fatal("users")
	}
	if _, err := s.CreateUser("admin", "short"); err == nil {
		t.Fatal("short password accepted")
	}
	id, err := s.CreateUser("admin", "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate("admin", "wrong"); err != ErrNotFound {
		t.Fatal("wrong password accepted")
	}
	if _, err := s.Authenticate("nobody", "correct horse"); err != ErrNotFound {
		t.Fatal("unknown user")
	}
	u, err := s.Authenticate("admin", "correct horse")
	if err != nil || u.ID != id {
		t.Fatal(err)
	}
	sid, csrf, err := s.CreateSession(id, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	se, err := s.SessionByID(sid)
	if err != nil || se.CSRF != csrf || se.User.Username != "admin" {
		t.Fatalf("%v %+v", err, se)
	}
	c.t = c.t.Add(2 * time.Hour)
	if _, err := s.SessionByID(sid); err != ErrNotFound {
		t.Fatal("expired session accepted")
	}
	plain, err := s.CreateToken(id, "ci", []string{"read", "write"})
	if err != nil || len(plain) < 20 {
		t.Fatal(err)
	}
	tok, err := s.TokenByPlain(plain)
	if err != nil || tok.Name != "ci" || len(tok.Scopes) != 2 {
		t.Fatalf("%v %+v", err, tok)
	}
	if _, err := s.TokenByPlain(plain + "x"); err != ErrNotFound {
		t.Fatal("bad token accepted")
	}
	toks, _ := s.Tokens()
	if len(toks) != 1 || toks[0].LastUsed == nil {
		t.Fatalf("%+v", toks)
	}
	s.DeleteToken(tok.ID)
	if _, err := s.TokenByPlain(plain); err != ErrNotFound {
		t.Fatal("revoked token accepted")
	}
	if err := s.SetPassword(id, "another password"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate("admin", "another password"); err != nil {
		t.Fatal(err)
	}
	// reset-password --revoke-tokens
	p1, _ := s.CreateToken(id, "a", []string{"read"})
	s.CreateToken(id, "b", []string{"read"})
	if len(p1) < 3 || p1[:3] != "uk_" {
		t.Fatalf("token prefix: %.3s", p1)
	}
	if n, err := s.DeleteUserTokens(id); err != nil || n != 2 {
		t.Fatalf("revoked %d, %v", n, err)
	}
	if _, err := s.TokenByPlain(p1); err != ErrNotFound {
		t.Fatal("token survived revoke")
	}
}

func TestPushTokens(t *testing.T) {
	s, _, _ := open(t)
	a, err := s.PushToken("backup")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := s.PushToken("backup"); a != b {
		t.Fatal("push token must be stable")
	}
	if m, _ := s.MonitorByPushToken(a); m != "backup" {
		t.Fatal(m)
	}
	r, _ := s.RotatePushToken("backup")
	if r == a {
		t.Fatal("rotation")
	}
	if b, _ := s.PushToken("backup"); b != r {
		t.Fatal("rotation must stick")
	}
	if _, err := s.MonitorByPushToken(a); err != ErrNotFound {
		t.Fatal("old token still valid")
	}
}

// An imported push token (from Uptime Kuma) keeps the old push URL.
func TestSetPushToken(t *testing.T) {
	s, _, _ := open(t)
	if err := s.SetPushToken("cron", "FAKEkumaToken1"); err != nil {
		t.Fatal(err)
	}
	if tok, _ := s.PushToken("cron"); tok != "FAKEkumaToken1" {
		t.Fatalf("token %q", tok)
	}
	if m, _ := s.MonitorByPushToken("FAKEkumaToken1"); m != "cron" {
		t.Fatalf("monitor %q", m)
	}
	if err := s.SetPushToken("other", "FAKEkumaToken1"); err == nil {
		t.Fatal("a token used by another monitor was accepted")
	}
	if err := s.SetPushToken("other", "bad token!"); err == nil {
		t.Fatal("an invalid token was accepted")
	}
	// Renaming keeps it; rotating replaces it.
	if err := s.RenameMonitor("cron", "cron-job"); err != nil {
		t.Fatal(err)
	}
	if tok, _ := s.PushToken("cron-job"); tok != "FAKEkumaToken1" {
		t.Fatalf("after rename %q", tok)
	}
	r, _ := s.RotatePushToken("cron-job")
	if tok, _ := s.PushToken("cron-job"); tok != r || r == "FAKEkumaToken1" {
		t.Fatal("rotation must replace the imported token")
	}
	if _, err := s.MonitorByPushToken("FAKEkumaToken1"); err != ErrNotFound {
		t.Fatal("old token still valid")
	}
}

func TestSecrets(t *testing.T) {
	s, _, dir := open(t)
	t.Setenv("UNAGIKUMA_SECRET_KEY", "")
	kek, err := LoadOrCreateKey(dir)
	if err != nil || len(kek) != 32 {
		t.Fatal(err)
	}
	again, _ := LoadOrCreateKey(dir)
	if !bytes.Equal(kek, again) {
		t.Fatal("key not persisted")
	}
	x := s.Secrets(kek)
	if err := x.Put("tg_token", "123:ABC", []string{"telegram"}); err != nil {
		t.Fatal(err)
	}
	if v, err := x.Get("tg_token"); err != nil || v != "123:ABC" || !x.Has("tg_token") {
		t.Fatal(v, err)
	}
	list, _ := x.List()
	if len(list) != 1 || list[0].Scopes[0] != "telegram" {
		t.Fatalf("%+v", list)
	}
	newKEK := bytes.Repeat([]byte{7}, 32)
	if err := x.Rotate(newKEK); err != nil {
		t.Fatal(err)
	}
	if v, err := s.Secrets(newKEK).Get("tg_token"); err != nil || v != "123:ABC" {
		t.Fatal(v, err)
	}
	if _, err := s.Secrets(kek).Get("tg_token"); err == nil {
		t.Fatal("old key still decrypts")
	}
	x.Delete("tg_token")
	if x.Has("tg_token") {
		t.Fatal("delete")
	}
}

func TestBackupAndStats(t *testing.T) {
	s, _, _ := open(t)
	s.SetSetting("a", "b")
	var buf bytes.Buffer
	if err := s.Backup(t.Context(), &buf); err != nil || !bytes.HasPrefix(buf.Bytes(), []byte("SQLite format 3")) {
		t.Fatalf("%v %q", err, buf.Bytes()[:16])
	}
	st := s.Stats()
	if st.CoreBytes == 0 {
		t.Fatalf("%+v", st)
	}
}

func TestPasswordHash(t *testing.T) {
	h := HashPassword("pw")
	if !CheckPassword(h, "pw") || CheckPassword(h, "pW") || CheckPassword("garbage", "pw") || HashPassword("pw") == h {
		t.Fatal("argon2id")
	}
}

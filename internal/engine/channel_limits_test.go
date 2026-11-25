package engine

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/hostclock"
	"github.com/githubflyideas/unagikuma/internal/netscope"
	"github.com/githubflyideas/unagikuma/internal/notify"
	"github.com/githubflyideas/unagikuma/internal/probe"
	"github.com/githubflyideas/unagikuma/internal/tunables"
)

// tries records when each channel was asked to send.
type tries struct {
	mu sync.Mutex
	at map[string][]time.Time
}

func (r *tries) add(ch string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.at == nil {
		r.at = map[string][]time.Time{}
	}
	r.at[ch] = append(r.at[ch], time.Now())
	return len(r.at[ch])
}

func (r *tries) of(ch string) []time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]time.Time(nil), r.at[ch]...)
}

// failingMonitors runs n monitors that fail at once, alerting through the
// chain, with send as the channels' answer.
func failingMonitors(t *testing.T, n int, set map[string]string, channels map[string]ChannelSpec, chain []string, send func(ch string, try int) notify.Attempt) *tries {
	t.Helper()
	rec := &tries{}
	e := New(Deps{
		Probe: func(context.Context, probe.Spec) probe.Result {
			return probe.Result{Vote: probe.VoteFail, Reason: probe.ReasonHTTPStatus, Detail: "HTTP 500", Evidence: &probe.Evidence{}}
		},
		Send: func(_ context.Context, ch notify.Channel, _ *notify.Message) notify.Attempt {
			return send(ch.Name, rec.add(ch.Name))
		},
		Sink: newMemSink(), HostClock: func() (hostclock.Reading, bool) { return hostclock.Reading{}, false },
		Gateways: func() ([]netscope.Gateway, error) { return nil, nil },
	})
	all := map[string]string{
		"engine.first-round-delay": "0s", "suspend.recheck-spread": "0s", "engine.round-grace": "0s", "localnet.enabled": "off",
		"notify.backstop-max": "0", "notify.coalesce-queue": "1000", "notify.channel-rate": "6000/m", "notify.retry-backoff": "0s",
	}
	for k, v := range set {
		all[k] = v
	}
	ts, err := tunables.New(all)
	if err != nil {
		t.Fatal(err)
	}
	var mons []MonitorSpec
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("m%02d", i)
		mons = append(mons, MonitorSpec{Name: name, Title: name, Policy: "p", DefHash: name, Interval: 50 * time.Millisecond, Attempts: 1, RecoverAttempts: 1,
			Probe: probe.Spec{Name: name, Type: "HTTP", Target: "http://10.0.0.1/" + name, Timeout: 100 * time.Millisecond}})
	}
	e.Apply(Config{Tunables: ts, Location: time.UTC, Channels: channels, Monitors: mons,
		Policies: map[string]PolicySpec{"p": {Name: "p", Chain: chain, Mode: DeliverFallback, Notify: DefaultNotifyPolicy(ts)}}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return rec
}

func spec(name, kind string) ChannelSpec {
	return ChannelSpec{Channel: notify.Channel{Name: name, Kind: kind}}
}

func waitTries(t *testing.T, rec *tries, ch string, n int) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); len(rec.of(ch)) < n; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("%s was tried %d times, want %d", ch, len(rec.of(ch)), n)
		}
	}
}

// retries=0 on a channel means one try: the next channel gets the alert.
func TestChannelRetriesZeroTurnsRetriesOff(t *testing.T) {
	a := spec("a", "webhook")
	a.RetriesSet = true
	rec := failingMonitors(t, 1, nil, map[string]ChannelSpec{"a": a, "b": spec("b", "webhook")}, []string{"a", "b"}, func(ch string, _ int) notify.Attempt {
		return notify.Attempt{OK: ch == "b", Error: "HTTP 500", Status: 500}
	})
	waitTries(t, rec, "b", 1)
	time.Sleep(200 * time.Millisecond)
	if n := len(rec.of("a")); n != 1 {
		t.Fatalf("a was tried %d times", n)
	}
}

// A failure the service calls final (a wrong token under HTTP 200) is not
// tried again.
func TestPermanentFailureGoesStraightToTheNextChannel(t *testing.T) {
	rec := failingMonitors(t, 1, nil, map[string]ChannelSpec{"a": spec("a", "feishu"), "b": spec("b", "webhook")}, []string{"a", "b"}, func(ch string, _ int) notify.Attempt {
		return notify.Attempt{OK: ch == "b", Error: "code=19001 token invalid", Status: 200, Permanent: ch == "a"}
	})
	waitTries(t, rec, "b", 1)
	time.Sleep(200 * time.Millisecond)
	if n := len(rec.of("a")); n != 1 {
		t.Fatalf("a was tried %d times", n)
	}
}

// A service that asks to wait is asked again after that long, on the same
// channel; one that asks for longer than notify.retry-after-max hands the
// alert to the next channel and is left alone meanwhile.
func TestRetryAfterIsHonoured(t *testing.T) {
	chans := map[string]ChannelSpec{"a": spec("a", "slack"), "b": spec("b", "webhook")}
	rec := failingMonitors(t, 1, nil, chans, []string{"a", "b"}, func(ch string, try int) notify.Attempt {
		if ch == "a" && try == 1 {
			return notify.Attempt{Error: "HTTP 429", Status: 429, RetryAfter: 400 * time.Millisecond}
		}
		return notify.Attempt{OK: true}
	})
	waitTries(t, rec, "a", 2)
	at := rec.of("a")
	if gap := at[1].Sub(at[0]); gap < 380*time.Millisecond {
		t.Fatalf("asked again after %s, the service said 400ms", gap)
	}
	if len(rec.of("b")) != 0 {
		t.Fatal("the next channel was used although the wait was short")
	}

	rec = failingMonitors(t, 3, map[string]string{"notify.retry-after-max": "1s"}, chans, []string{"a", "b"}, func(ch string, _ int) notify.Attempt {
		if ch == "a" {
			return notify.Attempt{Error: "errcode=130101 send too fast", Status: 200, RetryAfter: time.Minute}
		}
		return notify.Attempt{OK: true}
	})
	// Alerts already on their way when the first answer came cannot be
	// called back; after it nothing more goes to the limited channel.
	waitTries(t, rec, "b", 3)
	n := len(rec.of("a"))
	time.Sleep(500 * time.Millisecond)
	if after := len(rec.of("a")); after != n || n > 3 {
		t.Fatalf("a limited channel was asked %d times, then %d, in the minute it asked for", n, after)
	}
}

// A channel never sends more in a minute than its service takes, whatever
// its own rate allows; rate= on a channel replaces the general rate.
func TestServiceLimitAndChannelRate(t *testing.T) {
	rec := failingMonitors(t, 30, map[string]string{"notify.service-limits": "dingtalk=5,wecom=20"}, map[string]ChannelSpec{"d": spec("d", "dingtalk")}, []string{"d"},
		func(string, int) notify.Attempt { return notify.Attempt{OK: true} })
	waitTries(t, rec, "d", 5)
	time.Sleep(600 * time.Millisecond)
	if n := len(rec.of("d")); n != 5 {
		t.Fatalf("%d messages to a service that takes 5 a minute", n)
	}

	slow := spec("s", "webhook")
	slow.Rate = 3.0 / 60 // rate=3/m
	rec = failingMonitors(t, 30, nil, map[string]ChannelSpec{"s": slow}, []string{"s"}, func(string, int) notify.Attempt { return notify.Attempt{OK: true} })
	waitTries(t, rec, "s", 3)
	time.Sleep(600 * time.Millisecond)
	if n := len(rec.of("s")); n != 3 {
		t.Fatalf("%d messages through a channel with rate=3/m", n)
	}
}

// An alert no channel delivered does not use up the incident's
// notifications and does not wait for the repeat interval: it is sent
// again after notify.failed-retry, and counted once it arrives.
func TestUndeliveredAlertIsSentAgainSoon(t *testing.T) {
	p := NotifyPolicy{Max: 3, Repeat: 30 * time.Minute, Recovery: true}
	t0 := time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)
	st, n, _ := Fail(nil, t0, "http_status", p)
	if len(n) != 1 || n[0].Kind != NoticeProblem || st.NotifyCount != 1 {
		t.Fatalf("first notice: %+v %+v", n, st)
	}
	// Three deliveries in a row fail: the waits double, the count stays.
	at := t0
	for i, want := range []time.Duration{2 * time.Minute, 4 * time.Minute, 8 * time.Minute} {
		st.NotDelivered(at, 2*time.Minute, p.Repeat)
		if st.NotifyCount != 0 || st.Capped || st.RetryAt.Sub(at) != want {
			t.Fatalf("failure %d: count=%d capped=%v wait=%s, want %s", i+1, st.NotifyCount, st.Capped, st.RetryAt.Sub(at), want)
		}
		if n, _ := Advance(st, st.RetryAt.Add(-time.Second), p); len(n) != 0 {
			t.Fatalf("failure %d: sent before the wait was over: %+v", i+1, n)
		}
		at = st.RetryAt
		n, _ := Advance(st, at, p)
		if len(n) != 1 || n[0].Kind != NoticeProblem || n[0].Seq != 1 {
			t.Fatalf("failure %d: resent as %+v", i+1, n)
		}
	}
	// It arrives: counted once, and the repeat interval applies again.
	st.Delivered()
	if st.Undelivered != 0 || !st.RetryAt.IsZero() || st.NotifyCount != 1 || st.Told != 1 {
		t.Fatalf("after delivery: %+v", st)
	}
	if n, _ := Advance(st, at.Add(29*time.Minute), p); len(n) != 0 {
		t.Fatalf("repeated early: %+v", n)
	}
	if n, _ := Advance(st, at.Add(30*time.Minute), p); len(n) != 1 || n[0].Kind != NoticeRepeat || n[0].Seq != 2 {
		t.Fatalf("repeat: %+v", n)
	}
	// A repeat that fails gives its number back too.
	st.NotDelivered(at.Add(30*time.Minute), 2*time.Minute, p.Repeat)
	if n, _ := Advance(st, at.Add(32*time.Minute), p); len(n) != 1 || n[0].Kind != NoticeRepeat || n[0].Seq != 2 {
		t.Fatalf("failed repeat resent as %+v", n)
	}
	// Acknowledged: nothing more, failed or not.
	st.NotDelivered(at.Add(33*time.Minute), 2*time.Minute, p.Repeat)
	st.Acked = true
	if n, _ := Advance(st, at.Add(time.Hour), p); len(n) != 0 {
		t.Fatalf("sent after an acknowledgement: %+v", n)
	}
}

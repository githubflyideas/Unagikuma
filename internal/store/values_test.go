package store

import (
	"testing"
	"time"
)

// Values judged by monitors' rules are kept and read back in order, by the
// definition that produced them.
func TestValueSamples(t *testing.T) {
	s, _, _ := open(t)
	for i := 0; i < 3; i++ {
		s.Value("room", "h1", t0.Add(time.Duration(i)*time.Minute), float64(30+i))
	}
	s.Value("port", "h1", t0, 1e9)
	s.Flush()
	got, err := s.ValueSamples(t0.Add(-time.Hour))
	if err != nil || len(got["room"]["h1"]) != 3 || got["room"]["h1"][2].V != 32 || !got["room"]["h1"][0].At.Equal(t0) || len(got["port"]["h1"]) != 1 {
		t.Fatalf("%v %v", got, err)
	}
	if later, _ := s.ValueSamples(t0.Add(90 * time.Second)); len(later["room"]["h1"]) != 1 {
		t.Fatalf("since: %v", later)
	}
}

// What was redefined, deleted or renamed leaves no values behind for a
// later monitor of the same name to take as its reference.
func TestValueSamplesFollowDefinitions(t *testing.T) {
	s, _, _ := open(t)
	if err := s.SyncMonitors([]MonitorDef{{"room", "h1"}, {"port", "h1"}, {"gone", "h1"}}); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"room", "port", "gone"} {
		s.Value(n, "h1", t0, 10)
	}
	s.Flush()
	// room is redefined, gone is deleted, port stays.
	if err := s.SyncMonitors([]MonitorDef{{"room", "h2"}, {"port", "h1"}}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ValueSamples(t0.Add(-time.Hour))
	if len(got["port"]["h1"]) != 1 || len(got["room"]) != 0 || len(got["gone"]) != 0 {
		t.Fatalf("after sync: %v", got)
	}
	// A monitor created again under a deleted name starts empty.
	if err := s.SyncMonitors([]MonitorDef{{"room", "h2"}, {"port", "h1"}, {"gone", "h1"}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ValueSamples(t0.Add(-time.Hour)); len(got["gone"]) != 0 {
		t.Fatalf("inherited: %v", got["gone"])
	}
}

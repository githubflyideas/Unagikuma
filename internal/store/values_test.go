package store

import (
	"testing"
	"time"
)

// Values judged by monitors' rules are kept and read back in order.
func TestValueSamples(t *testing.T) {
	s, _, _ := open(t)
	for i := 0; i < 3; i++ {
		s.Value("room", t0.Add(time.Duration(i)*time.Minute), float64(30+i))
	}
	s.Value("port", t0, 1e9)
	s.Flush()
	got, err := s.ValueSamples(t0.Add(-time.Hour))
	if err != nil || len(got["room"]) != 3 || got["room"][2].V != 32 || !got["room"][0].At.Equal(t0) || len(got["port"]) != 1 {
		t.Fatalf("%v %v", got, err)
	}
	if later, _ := s.ValueSamples(t0.Add(90 * time.Second)); len(later["room"]) != 1 {
		t.Fatalf("since: %v", later)
	}
}

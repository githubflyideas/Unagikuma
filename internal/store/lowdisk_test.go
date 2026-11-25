package store

import (
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/tunables"
)

func TestLowDiskShortensRawRetention(t *testing.T) {
	s, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, ok := s.FreeRatio(); !ok {
		t.Log("free space not readable on this platform")
	}
	ts, _ := tunables.New(map[string]string{"retention.raw": "7d", "storage.low-disk-ratio": "0.1", "storage.low-disk-raw": "1d"})
	defer func(f func(*Store) (float64, bool)) { diskFree = f }(diskFree)
	for _, c := range []struct {
		free float64
		want time.Duration
	}{{0.5, 7 * 24 * time.Hour}, {0.05, 24 * time.Hour}} {
		diskFree = func(*Store) (float64, bool) { return c.free, true }
		r := RetentionFrom(ts)
		shrunk := s.LowDisk(&r, ts)
		if r.Raw != c.want || shrunk != (c.free < 0.1) {
			t.Errorf("free %.2f: raw %s (shrunk %v), want %s", c.free, r.Raw, shrunk, c.want)
		}
	}
}

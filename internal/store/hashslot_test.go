package store

import (
	"testing"
	"time"
)

// TestHashSlotsBound: no more than maxHashes password hashes run at once.
func TestHashSlotsBound(t *testing.T) {
	var releases []func()
	for range maxHashes {
		releases = append(releases, hashSlot())
	}
	got := make(chan struct{})
	go func() { hashSlot()(); close(got) }()
	select {
	case <-got:
		t.Fatal("a hash started while every slot was taken")
	case <-time.After(100 * time.Millisecond):
	}
	releases[0]()
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("a freed slot was not handed on")
	}
	for _, r := range releases[1:] {
		r()
	}
}

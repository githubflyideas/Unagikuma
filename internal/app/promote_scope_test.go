package app

import (
	"github.com/githubflyideas/unagikuma/internal/cluster"
	"github.com/githubflyideas/unagikuma/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPromotionPreservesFormerMasterScopes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config")
	text := "$FILE revision=2026110601 master=a interval=1s\nb IN NODE b\nweb IN HTTP http://example.test nodes=a,b\np IN PUSH period=1h nodes=a\n"
	if err := os.WriteFile(p, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	old, err := adoptPromotedConfig(p, "b")
	if err != nil || old != "a" {
		t.Fatal(old, err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	_, cfg, d := config.Load(p, b, config.Options{})
	if config.HasErrors(d) {
		t.Fatal(d)
	}
	if cfg.MName != "b" || !cfg.HasName("a") || !cfg.HasName("b") {
		t.Fatal(string(b))
	}
	if !strings.Contains(string(b), "nodes=a") {
		t.Fatal("old-site scope lost", string(b))
	}
}

func TestFormerMasterCanRejoinExistingPhysicalSite(t *testing.T) {
	a, _, _, _ := newApp(t, "$FILE revision=2026110601 master=c\nb IN NODE original region=east\nx IN NODE other\n")
	defer a.Store().Close()
	a.savePromoted(promoted{Self: "c", OldMaster: "b", Term: 2})
	code, _, err := a.store.CreateJoinCode(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.AcceptJoin(cluster.JoinRequest{Code: code, Node: "x"}); err == nil {
		t.Fatal("allowed takeover of unrelated existing node")
	}
	resp, err := a.AcceptJoin(cluster.JoinRequest{Code: code, Node: "b", Title: "replacement", Region: "west"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Token == "" {
		t.Fatal("no source token")
	}
	if !strings.Contains(a.Config().Text, "original region=east") {
		t.Fatal("site settings overwritten", a.Config().Text)
	}
	if _, err = a.AcceptJoin(cluster.JoinRequest{Code: code, Node: "b"}); err == nil {
		t.Fatal("replayed join capability accepted")
	}
}

package config

import (
	"strings"
	"testing"
)

// retries=0 and report= are remembered as written: 0 turns retries off,
// and only a written report=yes makes an SMS channel take the report.
func TestChannelOptionsAreRememberedAsWritten(t *testing.T) {
	_, cfg, ds := load(t, `$FILE revision=2026111201
a IN CHANNEL feishu url=https://example.com/h retries=0 rate=6/m
b IN CHANNEL feishu url=https://example.com/h report=yes
c IN CHANNEL feishu url=https://example.com/h
admin IN TEAM channels=a,b,c
`)
	if len(errorsOf(ds)) != 0 {
		t.Fatal(errorsOf(ds))
	}
	a, b, c := cfg.FindChannel("a"), cfg.FindChannel("b"), cfg.FindChannel("c")
	if !a.RetriesSet || a.Retries != 0 || a.Rate != "6/m" {
		t.Errorf("a: %+v", a)
	}
	if !b.ReportSet || b.NoReport || c.ReportSet || c.RetriesSet {
		t.Errorf("b: %+v c: %+v", b, c)
	}
}

// What is wrong with a channel's templates is a warning: the file loads.
func TestTemplateProblemsAreWarnings(t *testing.T) {
	opt := Options{ChannelWarnings: func(kind string, o map[string]string) []string {
		if strings.Contains(o["template"], "{{.Owner}}") {
			return []string{"template: unknown name Owner"}
		}
		return nil
	}}
	_, cfg, ds := Load("monitors.conf", []byte("$FILE revision=2026111201\na IN CHANNEL feishu url=https://example.com/h template=\"{{.Owner}}\"\nadmin IN TEAM channels=a\n"), opt)
	if len(errorsOf(ds)) != 0 || cfg.FindChannel("a") == nil {
		t.Fatalf("the file must load: %v", ds)
	}
	found := false
	for _, d := range ds {
		found = found || (d.Code == "template_problem" && d.Severity != SevError && strings.Contains(d.Msg, "unknown name Owner"))
	}
	if !found {
		t.Fatalf("no template_problem warning: %v", ds)
	}
}

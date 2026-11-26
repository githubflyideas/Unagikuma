package config

import (
	"errors"
	"strings"
	"testing"
)

// TEAM records, team= and tenant= are rejected with a clear error.
func TestRemovedTeamAndTenantSyntax(t *testing.T) {
	const head = "$FILE revision=1\nc IN CHANNEL log\n"

	for src, want := range map[string]string{
		"ops IN TEAM members=li channels=c\n":                   "TEAM records were removed",
		"www IN HTTP https://example.com/ team=ops\n":           "team= was removed",
		"$DEFAULT team=ops\nwww IN HTTP https://example.com/\n": "team= was removed",
		"www IN HTTP https://example.com/ tenant=t1\n":          "tenant= is not supported",
	} {
		_, _, ds := load(t, head+src)
		if !strings.Contains(errCodes(ds), "removed") {
			t.Errorf("%q: want removed, got %v", src, ds)
		}
		var texts []string
		for _, d := range errorsOf(ds) {
			texts = append(texts, d.Msg)
		}
		got := strings.Join(texts, "; ")
		if !strings.Contains(got, want) {
			t.Errorf("%q: want %q in %q", src, want, got)
		}
	}

	_, err := ParseSelector("team:web")
	if err == nil || !strings.Contains(err.Error(), "team: was removed") {
		t.Errorf("selector: %v", err)
	}
}

func errCodes(ds []Diagnostic) string {
	var b strings.Builder
	for _, d := range errorsOf(ds) {
		b.WriteString(d.Code + " ")
	}
	return b.String()
}

// Saving a record never replaces one of another kind with the same name:
// a policy or channel named like a monitor is refused, while a monitor may
// change its check type.
func TestUpsertKeepsOtherKinds(t *testing.T) {
	src := "$FILE revision=1\nc IN CHANNEL log\nweb IN HTTP https://example.com/\n"
	f, _ := Parse("x", []byte(src))
	for _, typ := range []string{"POLICY", "CHANNEL"} {
		err := f.UpsertRecord(RecordSpec{Name: "web", Type: typ, Opts: []Opt{{Key: "chain", Value: "c"}}})
		if !errors.Is(err, ErrNameTaken) {
			t.Errorf("%s named like a monitor: %v", typ, err)
		}
	}
	if err := f.UpsertRecord(RecordSpec{Name: "web", Type: "TEAM"}); err == nil || !strings.Contains(err.Error(), "TEAM records were removed") {
		t.Errorf("TEAM upsert: %v", err)
	}
	if string(f.Bytes()) != src {
		t.Fatalf("file changed:\n%s", f.Bytes())
	}
	if err := f.UpsertRecord(RecordSpec{Name: "web", Type: "TCP", Target: "example.com:443"}); err != nil {
		t.Fatalf("a monitor changing its type: %v", err)
	}
	if err := f.UpsertRecord(RecordSpec{Name: "c", Type: "channel", Target: "log"}); err != nil {
		t.Fatalf("same kind, other case: %v", err)
	}
}

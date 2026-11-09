package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/githubflyideas/unagikuma/internal/i18n"
)

// Every message code an event is recorded with has a text in the UI
// catalog, so the event log reads in the user's language.
func TestEventMessagesTranslated(t *testing.T) {
	b, ok := i18n.Catalog("en")
	if !ok {
		t.Fatal("no English catalog")
	}
	var en map[string]string
	if err := json.Unmarshal(b, &en); err != nil {
		t.Fatal(err)
	}
	codes := map[string]bool{}
	files, _ := filepath.Glob("*.go")
	re := regexp.MustCompile(`"msg": "(\w+)"[,}]`)
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			codes[m[1]] = true
		}
	}
	// Codes built at run time.
	for _, c := range []string{"notify_delivered", "notify_failed", "closed_redefined", "closed_deleted", "closed_local_network"} {
		codes[c] = true
	}
	for _, ev := range []string{IncidentCapped, IncidentAckExpired, IncidentAckCleared, IncidentSilenceEnded, IncidentReopened, IncidentClosed} {
		codes["incident_"+ev] = true
	}
	if len(codes) < 20 {
		t.Fatalf("found only %d codes", len(codes))
	}
	for c := range codes {
		if c == "state" {
			continue // drawn from the status names
		}
		if en["ev."+c] == "" && en["sys."+c] == "" {
			t.Errorf("event message %q has no ev.%s or sys.%s text", c, c, c)
		}
	}
}

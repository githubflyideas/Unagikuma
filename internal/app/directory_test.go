package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/githubflyideas/unagikuma/internal/store"
)

// Channels send only to active people in the directory. On the first
// start, the addresses the channels already use are added to it once, so
// an upgrade stops nothing; after that an unknown address is left out, and
// so is someone on leave, transferred or gone, or an address that keeps
// bouncing. @handles expand.
func TestChannelsFollowTheDirectory(t *testing.T) {
	dir := t.TempDir()
	bind := `@ IN SOA t. admin.localhost. ( 1 1m 15s 5s 3 )
mail IN CHANNEL email host=127.0.0.1 port=25 tls=none from=mon@example.com to="li@example.com, wang@example.com, @noc"
sms  IN CHANNEL aliyun-sms access-key-id=a access-key-secret=b sign-name=c template-code=d phones=13800000002
`
	if err := os.WriteFile(filepath.Join(dir, "monitors.conf"), []byte(bind), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := New(Options{DataDir: dir, Probe: (&fakeProbes{fail: map[string]bool{}}).run})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Store().Close()
	st := a.Store()
	opts := func(ch string) map[string]string { return a.engineConfig(a.applied.Load()).Channels[ch].Opts }
	byHandle := func(h string) store.Person {
		t.Helper()
		people, _ := st.People()
		for _, p := range people {
			if p.Handle == h {
				return p
			}
		}
		t.Fatalf("no contact %s in %+v", h, people)
		return store.Person{}
	}
	// Imported at the first start: li, wang and the phone number.
	if people, _ := st.People(); len(people) != 3 {
		t.Fatalf("imported: %+v", people)
	}
	if got := opts("mail")["to"]; got != "li@example.com,wang@example.com" {
		t.Fatalf("to after the import: %q", got)
	}
	if _, err := st.SavePerson(store.Person{Handle: "noc", Emails: []string{"noc@example.com"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetStatus(byHandle("wang").ID, store.StatusLeave); err != nil {
		t.Fatal(err)
	}
	a.DirectoryChanged()
	if got := opts("mail")["to"]; got != "li@example.com,noc@example.com" {
		t.Fatalf("to with wang on leave: %q", got)
	}
	if err := st.SetStatus(byHandle("wang").ID, store.StatusLeft); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := st.RecordBounce(store.ContactEmail, "NOC@example.com", "550 5.1.1 user unknown"); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetStatus(byHandle("phone-0002").ID, store.StatusTransferred); err != nil {
		t.Fatal(err)
	}
	a.DirectoryChanged()
	if got := opts("mail")["to"]; got != "li@example.com" {
		t.Fatalf("to after: %q", got)
	}
	ec := a.engineConfig(a.applied.Load())
	if ec.Channels["sms"].NoRecipients == "" {
		t.Fatalf("sms to someone transferred: %+v", ec.Channels["sms"])
	}
	issues := a.ChannelIssues()
	if len(issues["mail"]) != 2 || len(issues["sms"]) != 2 {
		t.Fatalf("issues: %+v", issues)
	}
	// The import runs once: an address removed from the directory later
	// is left out.
	if err := st.DeletePerson(byHandle("li").ID); err != nil {
		t.Fatal(err)
	}
	a.importContactsOnce()
	a.DirectoryChanged()
	if got := opts("mail")["to"]; got != "" {
		t.Fatalf("to without li: %q", got)
	}
	if people, _ := st.People(); len(people) != 3 {
		t.Fatalf("imported again: %+v", people)
	}
}

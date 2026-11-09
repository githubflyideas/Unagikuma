package license

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"
)

func TestSignAndCheck(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	ring := SingleKey(pub)
	exp := "2027-10-31"
	f := Sign(priv, File{Customer: "Example KK", Licensee: LicenseeOrganization, Product: Product, License: TypeSubscription,
		Tier: TierStandard, Expires: &exp, Features: []string{FeatAudit}})
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	s := Check(ring, f, "12345678", now)
	if !s.Registered || !s.Allowed(FeatAudit) || s.Days < 360 {
		t.Fatalf("state: %+v", s)
	}
	// Any change to a signed field breaks it.
	g := f
	g.Customer = "Other"
	if Check(ring, g, "1", now).Registered {
		t.Fatal("edited licence accepted")
	}
	g = f
	g.Features = []string{FeatAudit, "everything"}
	if Check(ring, g, "1", now).Registered {
		t.Fatal("added feature accepted")
	}
	// Expired.
	if s := Check(ring, f, "1", now.AddDate(2, 0, 0)); s.Registered || s.Kind != KindExpired || s.Allowed(FeatAudit) {
		t.Fatalf("expired: %+v", s)
	}
	// Trial pinned to another installation.
	tr := Sign(priv, File{Customer: "T", Licensee: LicenseeIndividual, Product: Product, License: TypeTrial, Tier: TierTrial,
		InstallationID: "11111111", Expires: &exp, Features: []string{FeatAudit}})
	if Check(ring, tr, "22222222", now).Registered || !Check(ring, tr, "11111111", now).Registered {
		t.Fatal("installation pinning")
	}
	// Unknown and disabled keys.
	pub2, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := SingleKey(pub2).Verify(f); err != ErrUnknownKey {
		t.Fatalf("unknown key: %v", err)
	}
	r := SingleKey(pub)
	r.Keys[0].Disabled, r.Keys[0].Note = true, "replaced"
	if s := Check(r, f, "1", now); s.Registered {
		t.Fatal("disabled key accepted")
	}
	// Allow list.
	r = SingleKey(pub)
	r.Allow = []string{"0000000000000000"}
	if Check(r, f, "1", now).Registered {
		t.Fatal("allow list ignored")
	}
}

func TestCheckerInstall(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	dir := t.TempDir()
	c, err := OpenWith(dir, SingleKey(pub))
	if err != nil {
		t.Fatal(err)
	}
	if c.State().Registered || len(c.InstallationID()) != 8 {
		t.Fatalf("fresh: %+v", c.State())
	}
	if _, err := c.Install([]byte(`{"customer":"x"}`), time.Now()); err == nil {
		t.Fatal("unsigned licence installed")
	}
	f := Sign(priv, File{Customer: "C", Licensee: LicenseeOrganization, Product: Product, License: TypePerpetual, Tier: TierTeam, Features: []string{FeatAudit}})
	b, _ := json.Marshal(f)
	st, err := c.Install(b, time.Now())
	if err != nil || !st.Allowed(FeatAudit) || st.Days != -1 {
		t.Fatalf("install: %+v %v", st, err)
	}
	c2, _ := OpenWith(dir, SingleKey(pub))
	if !c2.State().Registered || c2.InstallationID() != c.InstallationID() {
		t.Fatal("not kept across restarts")
	}
	if st, _ := c2.Remove(time.Now()); st.Registered {
		t.Fatal("remove")
	}
}

func TestReleaseKeys(t *testing.T) {
	if r := Release(); len(r.Keys) != 3 || r.Keys[0].ID != "1012acd9" {
		t.Fatalf("release keys: %+v", r.Keys)
	}
}

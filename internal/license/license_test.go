package license

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"
)

func testPrivateKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P521(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

func TestSignAndCheck(t *testing.T) {
	priv := testPrivateKey(t)
	ring := SingleKey(&priv.PublicKey)
	exp := "2027-10-31"
	f := Sign(priv, File{Customer: "Example KK", Licensee: LicenseeOrganization, Product: Product, License: TypeSubscription,
		Expires: &exp})
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	s := Check(ring, f, "12345678", now)
	if !s.Registered || s.Days < 360 {
		t.Fatalf("state: %+v", s)
	}
	// Any change to a signed field breaks it.
	g := f
	g.Customer = "Other"
	if Check(ring, g, "1", now).Registered {
		t.Fatal("edited licence accepted")
	}
	g = f
	g.MaxUsers = 1000
	if Check(ring, g, "1", now).Registered {
		t.Fatal("raised quota accepted")
	}
	// Expired.
	if s := Check(ring, f, "1", now.AddDate(2, 0, 0)); s.Registered || s.Kind != KindExpired {
		t.Fatalf("expired: %+v", s)
	}
	// Trial pinned to another installation.
	tr := Sign(priv, File{Customer: "T", Licensee: LicenseeIndividual, Product: Product, License: TypeTrial,
		InstallationID: "11111111", Expires: &exp})
	if Check(ring, tr, "22222222", now).Registered || !Check(ring, tr, "11111111", now).Registered {
		t.Fatal("installation pinning")
	}
	// Unknown and disabled keys.
	other := testPrivateKey(t)
	if _, err := SingleKey(&other.PublicKey).Verify(f); err != ErrUnknownKey {
		t.Fatalf("unknown key: %v", err)
	}
	r := SingleKey(&priv.PublicKey)
	r.Keys[0].Disabled, r.Keys[0].Note = true, "replaced"
	if s := Check(r, f, "1", now); s.Registered {
		t.Fatal("disabled key accepted")
	}
	// Allow list.
	r = SingleKey(&priv.PublicKey)
	r.Allow = []string{"0000000000000000"}
	if Check(r, f, "1", now).Registered {
		t.Fatal("allow list ignored")
	}
}

func TestCheckerInstall(t *testing.T) {
	priv := testPrivateKey(t)
	dir := t.TempDir()
	c, err := OpenWith(dir, SingleKey(&priv.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	if c.State().Registered || len(c.InstallationID()) != 8 {
		t.Fatalf("fresh: %+v", c.State())
	}
	if _, err := c.Install([]byte(`{"customer":"x"}`), time.Now()); err == nil {
		t.Fatal("unsigned licence installed")
	}
	f := Sign(priv, File{Customer: "C", Licensee: LicenseeOrganization, Product: Product, License: TypePerpetual})
	b, _ := json.Marshal(f)
	st, err := c.Install(b, time.Now())
	if err != nil || !st.Registered || st.Days != -1 {
		t.Fatalf("install: %+v %v", st, err)
	}
	c2, _ := OpenWith(dir, SingleKey(&priv.PublicKey))
	if !c2.State().Registered || c2.InstallationID() != c.InstallationID() {
		t.Fatal("not kept across restarts")
	}
	if st, _ := c2.Remove(time.Now()); st.Registered {
		t.Fatal("remove")
	}
}

func TestReleaseKeys(t *testing.T) {
	r := Release()
	if len(r.Keys) != 3 {
		t.Fatalf("release keys: %+v", r.Keys)
	}
	want := []string{"ec0599d8", "675c11b1", "bc828b1b"}
	for i, id := range want {
		if r.Keys[i].ID != id {
			t.Fatalf("release key %d: %s, want %s", i, r.Keys[i].ID, id)
		}
	}
}

package app

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha512"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/license"
)

// testLicenseKey signs licences for app tests.
var testLicenseKey = func() *ecdsa.PrivateKey {
	sum := sha512.Sum512([]byte("unagikuma app test licence key"))
	raw := make([]byte, 66)
	copy(raw[len(raw)-len(sum):], sum[:])
	priv, err := ecdsa.ParseRawPrivateKey(elliptic.P521(), raw)
	if err != nil {
		panic(err)
	}
	return priv
}()

// newRegisteredApp starts an app that accepts the test licence key and
// installs a licence, as a person registering after the first start would.
func newRegisteredApp(t *testing.T, text string) *App {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "monitors.conf"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	ring := license.SingleKey(&testLicenseKey.PublicKey)
	a, err := New(Options{DataDir: dir, Probe: (&fakeProbes{fail: map[string]bool{}}).run, Instance: "test", LicenseRing: &ring})
	if err != nil {
		t.Fatal(err)
	}
	f := license.Sign(testLicenseKey, license.File{Customer: "Test", Licensee: license.LicenseeOrganization, Product: license.Product, License: license.TypePerpetual})
	b, _ := json.Marshal(f)
	if _, err := a.License().Install(b, time.Now()); err != nil {
		t.Fatal(err)
	}
	a.LicenseChanged()
	return a
}

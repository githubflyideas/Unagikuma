package license

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/base64"
	"testing"
)

// vectorKey builds the fixed P-521 key of the vectors: scalar bytes
// off, off+1, … so both repositories sign with the same key.
func vectorKey(off int) *ecdsa.PrivateKey {
	d := make([]byte, 66)
	for i := range d {
		d[i] = byte(i + off)
	}
	priv, err := ecdsa.ParseRawPrivateKey(elliptic.P521(), d)
	if err != nil {
		panic(err)
	}
	return priv
}

// The signed message format is pinned here and in unagikuma-pro's copy
// of this test: if either side changes how a message is built, both
// fail, rather than a customer finding that an issued licence will not
// load.
func TestVector(t *testing.T) {
	priv := vectorKey(0)
	exp := "2027-10-31"
	f := Sign(priv, File{InstallationID: "12345678", Customer: "Example KK", Licensee: LicenseeOrganization, Product: Product,
		License: TypeTrial, Expires: &exp, MaxUsers: 10})
	if f.Version != CurrentVersion {
		t.Fatalf("version %d, want %d", f.Version, CurrentVersion)
	}
	const want = "unagikuma-license-v3\n80362f41\n12345678\nExample KK\norganization\nunagikuma\ntrial\n2027-10-31\nnull\n10\n"
	if got := string(Message(f)); got != want {
		t.Fatalf("message:\n%q\nwant\n%q", got, want)
	}
	// ECDSA signatures are randomised, so no fixed signature is pinned:
	// what is pinned is that the signature verifies, and covers the
	// message above and nothing else.
	if _, err := SingleKey(&priv.PublicKey).Verify(f); err != nil {
		t.Fatalf("vector does not verify: %v", err)
	}
	// Any change to a signed field breaks it.
	g := f
	g.Customer = "Other KK"
	if _, err := SingleKey(&priv.PublicKey).Verify(g); err != ErrSignature {
		t.Fatalf("tampered licence: %v", err)
	}
	// A signature over another licence does not carry over.
	other := Sign(priv, File{Customer: "Other KK", Licensee: LicenseeOrganization, Product: Product, License: TypePerpetual})
	g = f
	g.Signature = other.Signature
	if _, err := SingleKey(&priv.PublicKey).Verify(g); err != ErrSignature {
		t.Fatalf("transplanted signature: %v", err)
	}
}

// A licence signed by a key the ring does not hold is refused as
// unknown, not as corrupt.
func TestVectorWrongKey(t *testing.T) {
	priv, other := vectorKey(0), vectorKey(1)
	f := Sign(priv, File{Customer: "Example KK", Licensee: LicenseeOrganization, Product: Product, License: TypePerpetual})
	if _, err := SingleKey(&other.PublicKey).Verify(f); err != ErrUnknownKey {
		t.Fatalf("wrong key: %v", err)
	}
}

// Retired formats are refused outright: version 2 was the Ed25519
// format, and a licence with no version predates versioning. Neither is
// looked at any further, however good its signature once was.
func TestVectorRetiredFormat(t *testing.T) {
	priv := vectorKey(0)
	ring := SingleKey(&priv.PublicKey)
	exp := "2027-10-31"
	old := File{Version: 2, KeyID: "56475aa7", InstallationID: "12345678", Customer: "Example KK",
		Licensee: LicenseeOrganization, Product: Product, License: TypeTrial, Expires: &exp, MaxUsers: 10,
		Signature: "SW6T0zlwFWIOKMeBoRbH9esYDd9oKakdDJXsP1PgMGfzj3fSPY19zgHfze85HAwAdhgW6yF0zonNrjEEcAJCBg=="}
	if _, err := ring.Verify(old); err != ErrVersion {
		t.Fatalf("version 2 licence: %v", err)
	}
	old.Version = 0
	if _, err := ring.Verify(old); err != ErrVersion {
		t.Fatalf("unversioned licence: %v", err)
	}
}

// A signature that is not well-formed DER for this scheme is a bad
// signature, never a panic and never a pass.
func TestVectorMalformedSignature(t *testing.T) {
	priv := vectorKey(0)
	ring := SingleKey(&priv.PublicKey)
	f := Sign(priv, File{Customer: "Example KK", Licensee: LicenseeOrganization, Product: Product, License: TypePerpetual})
	raw, err := base64.StdEncoding.DecodeString(f.Signature)
	if err != nil || len(raw) < 100 {
		t.Fatalf("signature shape: %d bytes, %v", len(raw), err)
	}
	for name, sig := range map[string]string{
		"not DER at all": base64.StdEncoding.EncodeToString([]byte("this is not a signature")),
		"truncated DER":  base64.StdEncoding.EncodeToString(raw[:len(raw)/2]),
		"not base64":     "!!!",
	} {
		g := f
		g.Signature = sig
		if _, err := ring.Verify(g); err != ErrSignature {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

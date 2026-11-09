package license

import (
	"crypto/ed25519"
	"testing"
)

// The signed message format is pinned here and in unagikuma's copy of this
// test: if either side changes how a message is built, both fail, rather
// than a customer finding that an issued licence will not load.
func TestVector(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	exp := "2027-10-31"
	f := Sign(priv, File{InstallationID: "12345678", Customer: "Example KK", Licensee: LicenseeOrganization, Product: Product,
		License: TypeTrial, Tier: TierTrial, Expires: &exp, MaxNodes: 3, MaxUsers: 10, Features: []string{"audit", "audit", " "}})
	const want = "unagikuma-license-v1\n56475aa7\n12345678\nExample KK\norganization\nunagikuma\ntrial\ntrial\n2027-10-31\nnull\n3\n10\naudit\n"
	if got := string(Message(f)); got != want {
		t.Fatalf("message:\n%q\nwant\n%q", got, want)
	}
	// Ed25519 is deterministic: the same key and message give this.
	const sig = "zqBlIz4snVjS0JNNBvFTyuBnUo+/0Q8bBJcRYZfSLXWRNn+sOYYhjVE04q1P0NDQ4mVjNLdimqXUq0SBBS+5Dg=="
	if f.Signature != sig {
		t.Fatalf("signature %s", f.Signature)
	}
	if _, err := SingleKey(priv.Public().(ed25519.PublicKey)).Verify(f); err != nil {
		t.Fatalf("vector does not verify: %v", err)
	}
}

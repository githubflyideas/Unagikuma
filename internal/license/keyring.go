package license

import (
	"crypto/ed25519"
	"sort"
)

// Key is one issuing key as a release knows it. Disabled keys stay so a
// licence signed by one gets a clear answer instead of "corrupt".
type Key struct {
	ID       string
	Pub      ed25519.PublicKey
	Disabled bool
	Note     string
}

// Keyring is what a release accepts. Allow, when not empty, holds the
// fingerprints of the licences actually issued and nothing else passes.
type Keyring struct {
	Keys  []Key
	Allow []string
}

// Allows reports whether f is in the allow list, or there is none.
func (r Keyring) Allows(f File) bool {
	if len(r.Allow) == 0 {
		return true
	}
	fp := Fingerprint(f)
	i := sort.SearchStrings(r.Allow, fp)
	return i < len(r.Allow) && r.Allow[i] == fp
}

// Verify checks f against the key it names.
func (r Keyring) Verify(f File) (Key, error) {
	if len(r.Keys) == 0 {
		return Key{}, ErrNoKeys
	}
	if f.Signature == "" {
		return Key{}, ErrNoSignature
	}
	if err := printable(f); err != nil {
		return Key{}, err
	}
	sig, err := b64.DecodeString(f.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return Key{}, ErrSignature
	}
	if !r.Allows(f) {
		return Key{}, ErrNotAllowed
	}
	for _, k := range r.Keys {
		if k.ID != f.KeyID {
			continue
		}
		if k.Disabled {
			return k, ErrKeyDisabled
		}
		if !ed25519.Verify(k.Pub, Message(f), sig) {
			return Key{}, ErrSignature
		}
		if f.Product != Product {
			return Key{}, ErrProduct
		}
		return k, nil
	}
	return Key{}, ErrUnknownKey
}

// SingleKey is the ring of one key, for issuing and tests.
func SingleKey(pub ed25519.PublicKey) Keyring {
	return Keyring{Keys: []Key{{ID: KeyID(pub), Pub: pub}}}
}

package license

import (
	"crypto/ecdsa"
	"sort"
)

// Key is one issuing key as a release knows it. Disabled keys stay so a
// licence signed by one gets a clear answer instead of "corrupt".
type Key struct {
	ID       string
	Pub      *ecdsa.PublicKey
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

// Verify checks f against the key it names. The licence's format
// version picks the signature scheme; a version with no entry in
// formats is refused before anything else is looked at.
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
	spec, ok := formats[f.Version]
	if !ok {
		return Key{}, ErrVersion
	}
	sig, err := b64.DecodeString(f.Signature)
	if err != nil {
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
		if !spec.verify(k.Pub, Message(f), sig) {
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
func SingleKey(pub *ecdsa.PublicKey) Keyring {
	return Keyring{Keys: []Key{{ID: KeyID(pub), Pub: pub}}}
}

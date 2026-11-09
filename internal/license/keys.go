package license

import "sort"

// releaseKeys are the issuing keys this build accepts. The spares' private
// halves are kept offline and have never signed anything: if the key in
// use leaks, one release disables it (Disabled, with a Note the licence
// page shows) and issuing continues with a spare, without sending every
// customer a new file.
var releaseKeys = []struct {
	Pub      string
	Disabled bool
	Note     string
}{
	{Pub: "uRD4yMbdRP/y94ZpfX7HRQwFDkH8vemvu3mDaJupvME="},
	{Pub: "a/BXVg4iAvp76EXfoKmdbXcwWaLp+CQxkjpTfhz9Sz8="},
	{Pub: "mWo5YBRQ1hDZ4HCqt5OwfADLe3MnI0WPoeRfG0md0ts="},
}

// issued, when filled in by the release that answers a leaked key, is the
// fingerprints of every licence really issued; nothing else is accepted.
var issued []string

// Release returns the keyring this build verifies with.
func Release() Keyring {
	var r Keyring
	for _, k := range releaseKeys {
		pub, err := ParsePublicKey(k.Pub)
		if err != nil {
			continue
		}
		r.Keys = append(r.Keys, Key{ID: KeyID(pub), Pub: pub, Disabled: k.Disabled, Note: k.Note})
	}
	r.Allow = append(r.Allow, issued...)
	sort.Strings(r.Allow)
	return r
}

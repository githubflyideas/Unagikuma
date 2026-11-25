package license

import "sort"

// releaseKeys are the issuing keys this build accepts. The spares' private
// halves are kept offline and have never signed anything: if the key in
// use leaks, one release disables it (Disabled, with a Note the licence
// page shows) and issuing continues with a spare, without sending every
// customer a new file. The keys are ECDSA P-521, each in its base64 DER
// form; they replaced the Ed25519 keys of earlier releases outright, and
// no Ed25519 licence is accepted any more.
var releaseKeys = []struct {
	Pub      string
	Disabled bool
	Note     string
}{
	{Pub: "MIGbMBAGByqGSM49AgEGBSuBBAAjA4GGAAQAUSLau3JMhPjglbcV/yHl4nY50oEgdZoaLCw0ossVvWUlZTs/zBQSVUg7jFeXEFZkKv+a2n6jnrdVDRruIA5EaFMBJslkmfm1mdkti7MLRLGA8Oq9X8Nxjz/u9syUIXxEHgiCP2/QiQW9X1cRdNAB3qAfTGCbeDDgwjEnTkpb+7NWOes="},
	{Pub: "MIGbMBAGByqGSM49AgEGBSuBBAAjA4GGAAQA335dAlc7Iw8ZnpLlLvJPRD++137PG4f4qU8k4Dq+VKlkZHL26Y/+U+BSrtGsnDpRTrAOjauDr+8yxWE/aaFgNAIA3qqAqlaDdOqW1qaU7+6Ohta1AL4PyKfxhfIzZ1MpeXGW/KxWXZU3lf0iwckLljomqmCJLZdYkZGafcfbHIn4VSQ="},
	{Pub: "MIGbMBAGByqGSM49AgEGBSuBBAAjA4GGAAQAW4MNfAL3jD03DeM6g7SE8FlaHLJA+3yLyZ1iQHGtpEKdI3frJpZyT2mhnpg8J6UxBTwaIczsoTH4Mzl75EYjm3QAmk49c482d5UBm8S5O9SGiPyZwmUGloXOg27Ug+4CkFqzYTwBVV4QIQSoZOFyu/pMwXtFFeeYKOq8AuErkP44+2Q="},
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

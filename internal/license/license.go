// Package license decides what a unagikuma licence says and what an
// installation is therefore allowed to do.
//
// A licence is license.json in the data directory: who it is made out to,
// the tier, the quotas, the features and an Ed25519 signature over all of
// them. The private key stays with the author; the public keys are
// compiled in. Nothing is ever switched off by this package: an
// installation without a licence keeps monitoring, notifying and keeping
// its data. What it lacks is the registered-only features, such as reading
// the audit log; the audit log itself is written either way, so
// registering later shows the history from the start.
//
// Verification goes against a Keyring rather than one key: an offline
// licence cannot be withdrawn over the network, so if an issuing key ever
// leaks the answer is a release that stops accepting it. The key id is
// signed into the licence, spare keys ship in every release, and an allow
// list of issued fingerprints can be switched on.
package license

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// FileName is the licence inside the data directory.
const FileName = "license.json"

// Product is the product name a licence must carry.
const Product = "unagikuma"

// Licence types.
const (
	TypeTrial        = "trial"
	TypeSubscription = "subscription"
	TypePerpetual    = "perpetual"
)

// Who a licence is made out to.
const (
	LicenseeIndividual   = "individual"
	LicenseeOrganization = "organization"
)

// Licensees is every licensee kind.
var Licensees = []string{LicenseeIndividual, LicenseeOrganization}

// Tiers.
const (
	TierTrial      = "trial"
	TierTeam       = "team"
	TierStandard   = "standard"
	TierEnterprise = "enterprise"
	TierMSP        = "msp"
)

// Features a licence can grant. Everything not listed is free.
const (
	// FeatAudit opens the audit log of permission and configuration
	// changes: who gave whom which rights in which section.
	FeatAudit = "audit"
	// FeatBranding lets a theme pack replace the product name and logo
	// (white label) and leave out "Powered by" on status pages. Theme
	// pack colours and layouts are free.
	FeatBranding = "branding"
)

// Features is every feature this build switches on. The issuing tool
// refuses names that are not here.
var Features = []string{FeatAudit, FeatBranding}

// File is license.json. A quota of 0 means no limit. Expires nil means it
// never expires; UpdatesUntil then says how long new releases are covered.
type File struct {
	KeyID          string   `json:"key_id"`
	InstallationID string   `json:"installation_id,omitempty"`
	Customer       string   `json:"customer"`
	Licensee       string   `json:"licensee"`
	Product        string   `json:"product"`
	License        string   `json:"license"`
	Tier           string   `json:"tier"`
	Expires        *string  `json:"expires,omitempty"`
	UpdatesUntil   *string  `json:"updates_until,omitempty"`
	MaxNodes       int      `json:"max_nodes,omitempty"`
	MaxUsers       int      `json:"max_users,omitempty"`
	Features       []string `json:"features,omitempty"`
	Note           string   `json:"note,omitempty"`
	Signature      string   `json:"signature,omitempty"`
}

var b64 = base64.StdEncoding

// Fingerprint identifies one licence: the start of the SHA-256 of its
// signature. It is what support asks for and what an allow list holds.
func Fingerprint(f File) string {
	sum := sha256.Sum256([]byte(f.Signature))
	return hex.EncodeToString(sum[:8])
}

// KeyID names an issuing key by its public half.
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:4])
}

// Errors returned when a licence cannot be trusted.
var (
	ErrNoSignature = errors.New("license: no signature")
	ErrBadKey      = errors.New("license: public key is not an Ed25519 key")
	ErrBadField    = errors.New("license: a field contains a line break")
	ErrSignature   = errors.New("license: signature does not match")
	ErrNoKeys      = errors.New("license: no public key to check against")
	ErrUnknownKey  = errors.New("license: signed by a key this release does not know")
	ErrKeyDisabled = errors.New("license: signed by a key this release no longer accepts")
	ErrNotAllowed  = errors.New("license: not in this release's list of issued licences")
	ErrProduct     = errors.New("license: issued for another product")
)

// CanonFeatures returns the feature list as it is signed: trimmed,
// without duplicates or empties, sorted.
func CanonFeatures(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, f := range in {
		f = strings.TrimSpace(f)
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

func orNull(p *string) string {
	if p == nil || *p == "" {
		return "null"
	}
	return *p
}

// printable rejects line breaks: one would let a field pose as the next.
func printable(f File) error {
	for _, s := range []string{f.KeyID, f.InstallationID, f.Customer, f.Licensee, f.Product, f.License, f.Tier,
		orNull(f.Expires), orNull(f.UpdatesUntil), strings.Join(CanonFeatures(f.Features), ",")} {
		if strings.ContainsAny(s, "\n\r") {
			return ErrBadField
		}
	}
	return nil
}

// Message is what the signature covers and what the issuing tool signs.
func Message(f File) []byte {
	var b strings.Builder
	b.WriteString("unagikuma-license-v1\n")
	for _, s := range []string{f.KeyID, f.InstallationID, f.Customer, f.Licensee, f.Product, f.License, f.Tier,
		orNull(f.Expires), orNull(f.UpdatesUntil)} {
		b.WriteString(s)
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "%d\n%d\n", f.MaxNodes, f.MaxUsers)
	b.WriteString(strings.Join(CanonFeatures(f.Features), ","))
	b.WriteByte('\n')
	return []byte(b.String())
}

// Sign fills in the key id and signature (issuing side).
func Sign(priv ed25519.PrivateKey, f File) File {
	f.KeyID = KeyID(priv.Public().(ed25519.PublicKey))
	f.Features = CanonFeatures(f.Features)
	f.Signature = b64.EncodeToString(ed25519.Sign(priv, Message(f)))
	return f
}

// ParsePublicKey reads a base64 Ed25519 public key.
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	b, err := b64.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("license: public key: %w", err)
	}
	if len(b) != ed25519.PublicKeySize {
		return nil, ErrBadKey
	}
	return ed25519.PublicKey(b), nil
}

// ParsePrivateKey reads a base64 Ed25519 seed (issuing side).
func ParsePrivateKey(s string) (ed25519.PrivateKey, error) {
	b, err := b64.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("license: private key: %w", err)
	}
	if len(b) != ed25519.SeedSize {
		return nil, errors.New("license: private key is not an Ed25519 seed")
	}
	return ed25519.NewKeyFromSeed(b), nil
}

// Day parses a licence date (end of that day, UTC).
func Day(s string) (time.Time, error) {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, fmt.Errorf("license: bad date %q", s)
	}
	return t.Add(24*time.Hour - time.Nanosecond), nil
}

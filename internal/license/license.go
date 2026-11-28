// Package license decides what a unagikuma licence says and what an
// installation is therefore allowed to do.
//
// A licence is license.json in the data directory: who it is made out to,
// the number of accounts, the features and an ECDSA P-521 signature over
// all of them. The private key stays with the author; the public keys are
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
//
// A licence carries the version of its format. Each version fixes the
// wording of the signed message and the signature scheme that covers it;
// the formats table below is the whole dispatch, so a future scheme is
// one more entry, not a rewrite. Retired versions are removed from the
// table and their licences stop verifying.
package license

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// FileName is the licence inside the data directory.
const FileName = "license.json"

// Product is the product name a licence must carry.
const Product = "unagikuma"

// CurrentVersion is the licence format Sign produces. Version 5 is
// version 4 plus the node limit in the signed message; version 4 is
// version 3 plus the monitor limit. Version 3 is ECDSA P-521 over
// SHA-512, signatures in ASN.1 DER form. Versions 3 and 4 stay accepted
// (a licence already issued must keep working): their licences simply
// carry no node limit, and version 3 none for monitors either. Version 2
// was Ed25519; it is retired and no longer accepted.
const CurrentVersion = 5

// format is one licence format version: the domain line its message
// opens with and the scheme that signs and verifies it.
type format struct {
	domain string
	sign   func(priv *ecdsa.PrivateKey, msg []byte) ([]byte, error)
	verify func(pub *ecdsa.PublicKey, msg, sig []byte) bool
	// monitors: the signed message also covers MaxMonitors (version 4+).
	monitors bool
	// nodes: the signed message also covers MaxNodes (version 5+).
	nodes bool
}

// formats is the version dispatch: the versions this build accepts.
var formats = map[int]format{
	3: {"unagikuma-license-v3", signP521, verifyP521, false, false},
	4: {"unagikuma-license-v4", signP521, verifyP521, true, false},
	5: {"unagikuma-license-v5", signP521, verifyP521, true, true},
}

func signP521(priv *ecdsa.PrivateKey, msg []byte) ([]byte, error) {
	sum := sha512.Sum512(msg)
	return ecdsa.SignASN1(rand.Reader, priv, sum[:])
}

func verifyP521(pub *ecdsa.PublicKey, msg, sig []byte) bool {
	sum := sha512.Sum512(msg)
	return ecdsa.VerifyASN1(pub, sum[:], sig)
}

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

// Free is what an installation without a licence gets: up to FreeUsers
// accounts (every role counts); a single installation up to FreeSingle
// monitors, a cluster up to FreeShared shared monitors plus FreeSite of
// each site's own, and up to FreeNodes nodes (the master is one of them).
// The audit log, the installation's own name, logo and status page footer,
// and the four extra interface styles (after their trial) are
// registered-only. Everything else is free. A licence never gives less
// than this.
const (
	FreeUsers  = 5
	FreeNodes  = 3
	FreeSingle = 300
	FreeShared = 100
	FreeSite   = 100
	// FreeMonitors is the single-installation number, as the API reports it.
	FreeMonitors = FreeSingle
)

// File is license.json. MaxUsers, MaxMonitors and MaxNodes 0 mean no limit.
// Expires nil means it never expires; UpdatesUntil then says how long
// new releases are covered.
type File struct {
	Version        int     `json:"version,omitempty"`
	KeyID          string  `json:"key_id"`
	InstallationID string  `json:"installation_id,omitempty"`
	Customer       string  `json:"customer"`
	Licensee       string  `json:"licensee"`
	Product        string  `json:"product"`
	License        string  `json:"license"`
	Expires        *string `json:"expires,omitempty"`
	UpdatesUntil   *string `json:"updates_until,omitempty"`
	MaxUsers       int     `json:"max_users,omitempty"`
	MaxMonitors    int     `json:"max_monitors,omitempty"`
	MaxNodes       int     `json:"max_nodes,omitempty"`
	Note           string  `json:"note,omitempty"`
	Signature      string  `json:"signature,omitempty"`
}

var b64 = base64.StdEncoding

// Fingerprint identifies one licence: the start of the SHA-256 of its
// signature. It is what support asks for and what an allow list holds.
func Fingerprint(f File) string {
	sum := sha256.Sum256([]byte(f.Signature))
	return hex.EncodeToString(sum[:8])
}

// publicBytes is a public key in the form key ids and key files use:
// its DER (SubjectPublicKeyInfo) encoding.
func publicBytes(pub *ecdsa.PublicKey) []byte {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil
	}
	return der
}

// KeyID names an issuing key by its public half.
func KeyID(pub *ecdsa.PublicKey) string {
	sum := sha256.Sum256(publicBytes(pub))
	return hex.EncodeToString(sum[:4])
}

// Errors returned when a licence cannot be trusted.
var (
	ErrNoSignature = errors.New("license: no signature")
	ErrBadKey      = errors.New("license: public key is not a P-521 key")
	ErrBadField    = errors.New("license: a field contains a line break")
	ErrSignature   = errors.New("license: signature does not match")
	ErrNoKeys      = errors.New("license: no public key to check against")
	ErrUnknownKey  = errors.New("license: signed by a key this release does not know")
	ErrKeyDisabled = errors.New("license: signed by a key this release no longer accepts")
	ErrNotAllowed  = errors.New("license: not in this release's list of issued licences")
	ErrProduct     = errors.New("license: issued for another product")
	ErrVersion     = errors.New("license: issued in a licence format this release does not accept")
)

func orNull(p *string) string {
	if p == nil || *p == "" {
		return "null"
	}
	return *p
}

// printable rejects line breaks: one would let a field pose as the next.
func printable(f File) error {
	for _, s := range []string{f.KeyID, f.InstallationID, f.Customer, f.Licensee, f.Product, f.License,
		orNull(f.Expires), orNull(f.UpdatesUntil)} {
		if strings.ContainsAny(s, "\n\r") {
			return ErrBadField
		}
	}
	return nil
}

// Message is what the signature covers and what the issuing tool signs.
// A licence of a version this build does not accept has no message.
func Message(f File) []byte {
	spec, ok := formats[f.Version]
	if !ok {
		return nil
	}
	var b strings.Builder
	b.WriteString(spec.domain)
	b.WriteByte('\n')
	for _, s := range []string{f.KeyID, f.InstallationID, f.Customer, f.Licensee, f.Product, f.License,
		orNull(f.Expires), orNull(f.UpdatesUntil)} {
		b.WriteString(s)
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "%d\n", f.MaxUsers)
	if spec.monitors {
		fmt.Fprintf(&b, "%d\n", f.MaxMonitors)
	}
	if spec.nodes {
		fmt.Fprintf(&b, "%d\n", f.MaxNodes)
	}
	return []byte(b.String())
}

// Sign fills in the format version, key id and signature (issuing side).
// The current format's signature is randomised, so signing the same
// licence twice gives different bytes that both verify.
func Sign(priv *ecdsa.PrivateKey, f File) File {
	f.Version = CurrentVersion
	f.KeyID = KeyID(&priv.PublicKey)
	sig, err := formats[CurrentVersion].sign(priv, Message(f))
	if err != nil {
		return f // no signature: verification will refuse it
	}
	f.Signature = b64.EncodeToString(sig)
	return f
}

// ParsePublicKey reads a base64 DER public key.
func ParsePublicKey(s string) (*ecdsa.PublicKey, error) {
	b, err := b64.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("license: public key: %w", err)
	}
	any, err := x509.ParsePKIXPublicKey(b)
	if err != nil {
		return nil, ErrBadKey
	}
	pub, ok := any.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P521() {
		return nil, ErrBadKey
	}
	return pub, nil
}

// EncodePublicKey writes a public key in the base64 DER form
// ParsePublicKey reads, the form the compiled-in keyring holds.
func EncodePublicKey(pub *ecdsa.PublicKey) string {
	return b64.EncodeToString(publicBytes(pub))
}

// ParsePrivateKey reads a base64 PKCS#8 private key (issuing side).
func ParsePrivateKey(s string) (*ecdsa.PrivateKey, error) {
	b, err := b64.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("license: private key: %w", err)
	}
	any, err := x509.ParsePKCS8PrivateKey(b)
	if err != nil {
		return nil, errors.New("license: private key is not a P-521 key")
	}
	priv, ok := any.(*ecdsa.PrivateKey)
	if !ok || priv.Curve != elliptic.P521() {
		return nil, errors.New("license: private key is not a P-521 key")
	}
	return priv, nil
}

// EncodePrivateKey writes a private key in the base64 PKCS#8 form
// ParsePrivateKey reads (issuing side).
func EncodePrivateKey(priv *ecdsa.PrivateKey) (string, error) {
	b, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return "", fmt.Errorf("license: private key: %w", err)
	}
	return b64.EncodeToString(b), nil
}

// Day parses a licence date (end of that day, UTC).
func Day(s string) (time.Time, error) {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, fmt.Errorf("license: bad date %q", s)
	}
	return t.Add(24*time.Hour - time.Nanosecond), nil
}

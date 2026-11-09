// Package cluster connects probe agents to the master: certificates, the
// shared TLS/plain listener, the framed protocol and both of its ends.
package cluster

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// AgentServerName is the TLS server name agents ask for. The master always
// answers it with the certificate issued by its own CA, whatever certificate
// the web UI uses, so a pinned CA fingerprint keeps working when the UI
// certificate is replaced or renewed.
const AgentServerName = "agent.unagikuma.invalid"

const (
	caFile      = "cluster-ca.crt"
	caKeyFile   = "cluster-ca.key"
	leafFile    = "cluster.crt"
	leafKeyFile = "cluster.key"

	caLifetime   = 20 * 365 * 24 * time.Hour
	leafLifetime = 5 * 365 * 24 * time.Hour
	// A leaf this close to expiry is reissued at start.
	leafRenewBefore = 90 * 24 * time.Hour
)

// PKI is the master's private CA and the certificate agents connect to.
const PeerServerName = "peer.unagikuma.invalid"

type PKI struct {
	Peer  *PKI // stable site identity, never replaced by authority promotion
	CA    *x509.Certificate
	Leaf  tls.Certificate
	caKey *ecdsa.PrivateKey
}

// LoadOrCreatePKI loads the cluster CA and server certificate from dir,
// creating them on first use. Private keys are written with mode 0600.
func LoadOrCreatePKI(dir string, now time.Time) (*PKI, error) {
	ca, caKey, err := loadPair(filepath.Join(dir, caFile), filepath.Join(dir, caKeyFile))
	if errors.Is(err, os.ErrNotExist) {
		ca, caKey, err = createCA(dir, now)
	}
	if err != nil {
		return nil, fmt.Errorf("cluster CA: %w", err)
	}
	// The server certificate is derived from the CA, so a missing, damaged,
	// foreign or expiring one is simply reissued.
	leaf, leafKey, err := loadPair(filepath.Join(dir, leafFile), filepath.Join(dir, leafKeyFile))
	if err != nil || leaf.CheckSignatureFrom(ca) != nil || now.Add(leafRenewBefore).After(leaf.NotAfter) ||
		leaf.VerifyHostname(AgentServerName) != nil {
		leaf, leafKey, err = createLeaf(dir, ca, caKey, now)
	}
	if err != nil {
		return nil, fmt.Errorf("cluster certificate: %w", err)
	}
	return &PKI{CA: ca, caKey: caKey, Leaf: tls.Certificate{Certificate: [][]byte{leaf.Raw, ca.Raw}, PrivateKey: leafKey, Leaf: leaf}}, nil
}

// Fingerprint is the value agents pin: "sha256:" and the hex digest of the
// CA certificate.
func (p *PKI) Fingerprint() string { return Fingerprint(p.CA) }

// Fingerprint formats the SHA-256 fingerprint of a certificate.
func Fingerprint(c *x509.Certificate) string {
	h := sha256.Sum256(c.Raw)
	return "sha256:" + hex.EncodeToString(h[:])
}

// normalizeFingerprint accepts "sha256:ab:cd…", "AB CD …" and plain hex.
func normalizeFingerprint(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "sha256:")
	s = strings.NewReplacer(":", "", " ", "", "-", "").Replace(s)
	if b, err := hex.DecodeString(s); err != nil || len(b) != sha256.Size {
		return "", fmt.Errorf("invalid certificate fingerprint (want sha256:<64 hex digits>)")
	}
	return "sha256:" + s, nil
}

// TLSConfig serves the agent certificate to agents and ui (when set) to
// everyone else; without ui the agent certificate is used for browsers too.
func (p *PKI) TLSConfig(ui *tls.Certificate) *tls.Config {
	leaf := p.Leaf
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		ClientAuth: tls.RequestClientCert,
		NextProtos: []string{"h2", "http/1.1"},
		GetCertificate: func(h *tls.ClientHelloInfo) (*tls.Certificate, error) {
			if h.ServerName == PeerServerName && p.Peer != nil {
				return &p.Peer.Leaf, nil
			}
			if ui == nil || h.ServerName == AgentServerName {
				return &leaf, nil
			}
			return ui, nil
		},
	}
}

func createCA(dir string, now time.Time) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	host, _ := os.Hostname()
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "unagikuma cluster CA " + host},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(caLifetime),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	return savePair(filepath.Join(dir, caFile), filepath.Join(dir, caKeyFile), der, key)
}

func createLeaf(dir string, ca *x509.Certificate, caKey *ecdsa.PrivateKey, now time.Time) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: AgentServerName},
		DNSNames:     []string{AgentServerName},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(leafLifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, err
	}
	return savePair(filepath.Join(dir, leafFile), filepath.Join(dir, leafKeyFile), der, key)
}

func serial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	return n
}

func savePair(certPath, keyPath string, der []byte, key *ecdsa.PrivateKey) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	if err := writeFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})); err != nil {
		return nil, nil, err
	}
	if err := writeFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})); err != nil {
		return nil, nil, err
	}
	c, err := x509.ParseCertificate(der)
	return c, key, err
}

// writeFile replaces path atomically with a 0600 file.
func writeFile(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func loadPair(certPath, keyPath string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	cb, err := os.ReadFile(certPath)
	if err != nil {
		return nil, nil, err
	}
	kb, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, err
	}
	cblk, _ := pem.Decode(cb)
	kblk, _ := pem.Decode(kb)
	if cblk == nil || kblk == nil {
		return nil, nil, fmt.Errorf("%s: not PEM", certPath)
	}
	c, err := x509.ParseCertificate(cblk.Bytes)
	if err != nil {
		return nil, nil, err
	}
	k, err := x509.ParseECPrivateKey(kblk.Bytes)
	if err != nil {
		return nil, nil, err
	}
	if !k.PublicKey.Equal(c.PublicKey) {
		return nil, nil, fmt.Errorf("%s: key does not match certificate", certPath)
	}
	return c, k, nil
}

// pinnedVerifier checks a server chain against a pinned CA fingerprint: the
// chain must contain the pinned certificate and the leaf must verify up to
// it for AgentServerName.
func pinnedVerifier(pin string) func(tls.ConnectionState) error {
	return func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return errors.New("server sent no certificate")
		}
		roots, inter := x509.NewCertPool(), x509.NewCertPool()
		found := false
		for _, c := range cs.PeerCertificates {
			if Fingerprint(c) == pin {
				roots.AddCert(c)
				found = true
			} else {
				inter.AddCert(c)
			}
		}
		if !found {
			return fmt.Errorf("server certificate does not match the pinned fingerprint %s (got CA %s)", pin, Fingerprint(cs.PeerCertificates[len(cs.PeerCertificates)-1]))
		}
		_, err := cs.PeerCertificates[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter, DNSName: AgentServerName})
		return err
	}
}

// PinnedTLSConfig authenticates cluster peers even when the public UI uses
// a different certificate. An empty or malformed trust set fails closed.
func PinnedTLSConfig(pins ...string) *tls.Config {
	valid := make([]string, 0, len(pins))
	for _, pin := range pins {
		if p, err := normalizeFingerprint(pin); err == nil {
			valid = append(valid, p)
		}
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, ServerName: AgentServerName,
		NextProtos: []string{"http/1.1"}, InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			for _, p := range valid {
				if pinnedVerifier(p)(cs) == nil {
					return nil
				}
			}
			return errors.New("peer does not match an authenticated cluster identity")
		},
	}
}

func (p *PKI) PeerIdentity() *PKI {
	if p.Peer != nil {
		return p.Peer
	}
	return p
}
func PinnedPeerTLSConfig(pins ...string) *tls.Config {
	c := PinnedTLSConfig(pins...)
	c.ServerName = PeerServerName
	return c
}

// VerifyPeerIdentity authenticates the certificate and possession proved by TLS.
func VerifyPeerIdentity(cs tls.ConnectionState, pin string) error {
	return PinnedTLSConfig(pin).VerifyConnection(cs)
}

package cluster

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// RetirePath is where a new master tells a replaced one to step down.
const RetirePath = "/api/v1/agent/retire"

// RetireNotice says a master with Term took over. It is signed with the
// cluster CA key, which only the masters of the cluster hold, so nobody
// else can make a master step down.
type RetireNotice struct {
	Term uint64    `json:"term"`
	At   time.Time `json:"at"`
	Sig  string    `json:"sig"`
	// Join, when set, is a join code of the new master: the replaced one
	// rejoins as replica Node and offers what it wrote since the copy.
	Join string `json:"join,omitempty"`
	Node string `json:"node,omitempty"`
	// Version and SHA256 are those of the configuration the new master
	// took over with; a replaced master with another one keeps it aside.
	Version uint32 `json:"version,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
}

func retireDigest(term uint64, at time.Time) []byte {
	h := sha256.Sum256([]byte(fmt.Sprintf("unagikuma-retire\n%d\n%s\n", term, at.UTC().Format(time.RFC3339Nano))))
	return h[:]
}

// RetireNotice signs a notice for term.
func (p *PKI) RetireNotice(term uint64, now time.Time) (RetireNotice, error) {
	if p.caKey == nil {
		return RetireNotice{}, errors.New("no CA key")
	}
	sig, err := ecdsa.SignASN1(rand.Reader, p.caKey, retireDigest(term, now))
	if err != nil {
		return RetireNotice{}, err
	}
	return RetireNotice{Term: term, At: now, Sig: base64.StdEncoding.EncodeToString(sig)}, nil
}

// CheckRetire verifies a notice against this cluster's CA.
func (p *PKI) CheckRetire(n RetireNotice) error {
	sig, err := base64.StdEncoding.DecodeString(n.Sig)
	if err != nil {
		return errors.New("bad signature")
	}
	pub, ok := p.CA.PublicKey.(*ecdsa.PublicKey)
	if !ok || !ecdsa.VerifyASN1(pub, retireDigest(n.Term, n.At), sig) {
		return errors.New("not signed by this cluster's CA")
	}
	return nil
}

// SendRetire delivers a notice to a replaced master at server (pinned to
// the cluster CA, like agents).
func SendRetire(ctx context.Context, server, pin string, n RetireNotice) error {
	u, err := NormalizeServer(server)
	if err != nil {
		return err
	}
	addr := strings.TrimPrefix(u, "https://")
	pin, err = normalizeFingerprint(pin)
	if err != nil {
		return err
	}
	tr := &http.Transport{TLSClientConfig: &tls.Config{ServerName: AgentServerName, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12,
		VerifyConnection: pinnedVerifier(pin)}, DialContext: (&net.Dialer{Timeout: handshakeTimeout}).DialContext}
	defer tr.CloseIdleConnections()
	b, _ := json.Marshal(n)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+addr+RetirePath, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Transport: tr, Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

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

// RejoinOfferPath is where a superseded master asks the winner for a join
// code so it can come back as a replica without a restart.
const RejoinOfferPath = "/api/v1/agent/rejoin-offer"

// RejoinRequest is signed with the cluster CA key that only masters hold.
// A replaced master still has that key until it demotes, so it can prove
// it belongs to the cluster without an agent token yet.
type RejoinRequest struct {
	Node  string    `json:"node"`
	At    time.Time `json:"at"`
	Nonce string    `json:"nonce"` // one-time; master rejects replays
	Sig   string    `json:"sig"`
}

func rejoinDigest(node, nonce string, at time.Time) []byte {
	h := sha256.Sum256([]byte(fmt.Sprintf("unagikuma-rejoin\n%s\n%s\n%s\n", node, nonce, at.UTC().Format(time.RFC3339Nano))))
	return h[:]
}

// SignRejoin signs a rejoin request for node with a fresh nonce.
func (p *PKI) SignRejoin(node string, now time.Time) (RejoinRequest, error) {
	if p.caKey == nil {
		return RejoinRequest{}, errors.New("no CA key")
	}
	var nb [16]byte
	if _, err := rand.Read(nb[:]); err != nil {
		return RejoinRequest{}, err
	}
	nonce := base64.RawURLEncoding.EncodeToString(nb[:])
	sig, err := ecdsa.SignASN1(rand.Reader, p.caKey, rejoinDigest(node, nonce, now))
	if err != nil {
		return RejoinRequest{}, err
	}
	return RejoinRequest{Node: node, At: now, Nonce: nonce, Sig: base64.StdEncoding.EncodeToString(sig)}, nil
}

// CheckRejoin verifies a rejoin request against this cluster's CA.
func (p *PKI) CheckRejoin(r RejoinRequest) error {
	if strings.TrimSpace(r.Nonce) == "" {
		return errors.New("missing nonce")
	}
	sig, err := base64.StdEncoding.DecodeString(r.Sig)
	if err != nil {
		return errors.New("bad signature")
	}
	pub, ok := p.CA.PublicKey.(*ecdsa.PublicKey)
	if !ok || !ecdsa.VerifyASN1(pub, rejoinDigest(r.Node, r.Nonce, r.At), sig) {
		return errors.New("not signed by this cluster's CA")
	}
	// Reject stale requests (clock skew allowance).
	if d := time.Since(r.At); d > 5*time.Minute || d < -1*time.Minute {
		return errors.New("rejoin request expired")
	}
	return nil
}

// RequestRejoinOffer asks master at server for a join code to reclaim node.
func RequestRejoinOffer(ctx context.Context, server, pin string, req RejoinRequest) (RetireNotice, error) {
	u, err := NormalizeServer(server)
	if err != nil {
		return RetireNotice{}, err
	}
	addr := strings.TrimPrefix(u, "https://")
	pin, err = normalizeFingerprint(pin)
	if err != nil {
		return RetireNotice{}, err
	}
	tr := &http.Transport{TLSClientConfig: &tls.Config{ServerName: AgentServerName, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12,
		VerifyConnection: pinnedVerifier(pin)}, DialContext: (&net.Dialer{Timeout: handshakeTimeout}).DialContext}
	defer tr.CloseIdleConnections()
	b, _ := json.Marshal(req)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+addr+RejoinOfferPath, bytes.NewReader(b))
	if err != nil {
		return RetireNotice{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Transport: tr, Timeout: 30 * time.Second}).Do(httpReq)
	if err != nil {
		return RetireNotice{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return RetireNotice{}, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var n RetireNotice
	if err := json.Unmarshal(body, &n); err != nil {
		return RetireNotice{}, err
	}
	if n.Join == "" {
		return RetireNotice{}, errors.New("master returned an empty join offer")
	}
	return n, nil
}

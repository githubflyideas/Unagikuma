package cluster

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// JoinPath is the master endpoint a machine calls to join with a code.
const JoinPath = "/api/v1/agent/join"

const joinPrefix = "nkjoin1."

// JoinInfo is what a join code carries: where the master is, the CA it
// must present and the one-time secret.
type JoinInfo struct {
	Server      string    `json:"s"`
	Fingerprint string    `json:"f"`
	Code        string    `json:"c"`
	Expires     time.Time `json:"e"`
}

// EncodeJoin renders a join code to paste on the joining machine.
func EncodeJoin(j JoinInfo) string {
	b, _ := json.Marshal(j)
	return joinPrefix + base64.RawURLEncoding.EncodeToString(b)
}

// DecodeJoin parses a pasted join code.
func DecodeJoin(s string) (JoinInfo, error) {
	var j JoinInfo
	s = strings.Join(strings.Fields(s), "") // pasted across lines
	raw, ok := strings.CutPrefix(s, joinPrefix)
	if !ok {
		return j, errors.New("this is not a unagikuma join code")
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || json.Unmarshal(b, &j) != nil || j.Server == "" || j.Code == "" {
		return j, errors.New("the join code is damaged; copy it again")
	}
	if _, err := normalizeFingerprint(j.Fingerprint); err != nil {
		return j, err
	}
	return j, nil
}

// ImportRecord is a monitor the joining machine had, offered to the
// master (it is added paused, in a section named after the node).
type ImportRecord struct {
	Name     string            `json:"name"`
	Type     string            `json:"type"`
	Target   string            `json:"target"`
	Interval string            `json:"interval,omitempty"`
	Opts     map[string]string `json:"opts,omitempty"`
}

// JoinRequest asks the master to accept a machine as a node.
type JoinRequest struct {
	Code     string         `json:"code"`
	Node     string         `json:"node"`
	Title    string         `json:"title,omitempty"`
	Region   string         `json:"region,omitempty"`
	Version  string         `json:"version,omitempty"`
	Platform string         `json:"platform,omitempty"`
	Import   []ImportRecord `json:"import,omitempty"`
}

// JoinResponse gives the new node its token.
type JoinResponse struct {
	Node     string   `json:"node"`
	Token    string   `json:"token"`
	Imported []string `json:"imported,omitempty"` // monitor names as added on the master
	Skipped  string   `json:"skipped,omitempty"`  // why the import was dropped
}

// Join sends a join request to the master named in the code, verifying it
// by the CA fingerprint the code carries.
func Join(ctx context.Context, info JoinInfo, req JoinRequest) (JoinResponse, error) {
	var resp JoinResponse
	pin, err := normalizeFingerprint(info.Fingerprint)
	if err != nil {
		return resp, err
	}
	u, err := url.Parse(info.Server)
	if err != nil || u.Host == "" {
		return resp, fmt.Errorf("invalid master address %q in the join code", info.Server)
	}
	tr := &http.Transport{
		Proxy:       nil,
		DialContext: (&net.Dialer{Timeout: handshakeTimeout}).DialContext,
		TLSClientConfig: &tls.Config{
			ServerName: AgentServerName, MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"},
			InsecureSkipVerify: true, VerifyConnection: pinnedVerifier(pin),
		},
		TLSHandshakeTimeout: handshakeTimeout,
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 30 * time.Second}
	body, _ := json.Marshal(req)
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+u.Host+JoinPath, bytes.NewReader(body))
	if err != nil {
		return resp, err
	}
	r.Header.Set("Content-Type", "application/json")
	res, err := client.Do(r)
	if err != nil {
		return resp, fmt.Errorf("cannot reach the master at %s: %w", u.Host, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(b, &e)
		if e.Message == "" {
			e.Message = res.Status
		}
		return resp, fmt.Errorf("the master refused: %s", e.Message)
	}
	if err := json.Unmarshal(b, &resp); err != nil || resp.Token == "" {
		return resp, errors.New("unexpected answer from the master")
	}
	return resp, nil
}

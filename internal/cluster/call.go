package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
)

// AuthAgent checks an agent's token on a plain HTTPS request and returns
// the node it belongs to.
func (m *Master) AuthAgent(r *http.Request) (string, error) {
	if r.TLS == nil {
		return "", errors.New("agents must connect with TLS")
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		return "", errors.New("missing agent token")
	}
	node, err := m.o.Auth(token, r.RemoteAddr)
	if err != nil {
		return "", errors.New("invalid agent token")
	}
	return node, nil
}

// Call sends a JSON request to the master over the pinned connection and
// decodes the JSON answer into out.
func (a *Agent) Call(ctx context.Context, method, path string, in, out any) error {
	addr, _ := hostPort(a.Server())
	tr := &http.Transport{
		DialTLSContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return a.dialAddr(ctx, addr) },
	}
	defer tr.CloseIdleConnections()
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://"+addr+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.o.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("master answered %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

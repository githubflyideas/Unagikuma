package probe

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

var grpcServing = map[uint64]string{0: "UNKNOWN", 1: "SERVING", 2: "NOT_SERVING", 3: "SERVICE_UNKNOWN"}

// grpcCheck calls grpc.health.v1.Health/Check (service= names one
// service; empty asks about the server as a whole) and expects SERVING.
func grpcCheck(ctx context.Context, conn net.Conn, host string, o opts, r Result) Result {
	// The standard client speaks HTTP/2, with or without TLS; it is
	// handed the connection already made (and TLS-wrapped with ALPN h2).
	var once sync.Once
	give := func(context.Context, string, string) (net.Conn, error) {
		var c net.Conn
		once.Do(func() { c = conn })
		if c == nil {
			return nil, net.ErrClosed
		}
		return c, nil
	}
	var protos http.Protocols
	tr := &http.Transport{DisableKeepAlives: true}
	if o.on("tls") {
		protos.SetHTTP2(true)
		tr.DialTLSContext = give
	} else {
		protos.SetUnencryptedHTTP2(true)
		tr.DialContext = give
	}
	tr.Protocols = &protos
	defer tr.CloseIdleConnections()
	// The request message has one field: 1 = service name.
	svc := o.str("service", "")
	var msg []byte
	if svc != "" {
		msg = append([]byte{0x0a}, binary.AppendUvarint(nil, uint64(len(svc)))...)
		msg = append(msg, svc...)
	}
	frame := make([]byte, 5, 5+len(msg))
	binary.BigEndian.PutUint32(frame[1:], uint32(len(msg)))
	frame = append(frame, msg...)
	scheme := "http"
	if o.on("tls") {
		scheme = "https"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, scheme+"://"+host+"/grpc.health.v1.Health/Check", bytes.NewReader(frame))
	if err != nil {
		return failKeep(r, ReasonConfig, "%v", err)
	}
	req.Header.Set("Content-Type", "application/grpc")
	req.Header.Set("TE", "trailers")
	resp, err := tr.RoundTrip(req)
	if err != nil {
		if strings.Contains(err.Error(), "HTTP/1.1") {
			return failKeep(r, ReasonProtoError, "not a gRPC server: it answers HTTP/1.1, not HTTP/2")
		}
		return protoFail(r, "gRPC", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return protoFail(r, "gRPC", err)
	}
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/grpc") {
		return failKeep(r, ReasonProtoError, "not a gRPC server (HTTP %d)", resp.StatusCode)
	}
	status := resp.Trailer.Get("Grpc-Status")
	if status == "" {
		status = resp.Header.Get("Grpc-Status") // trailers-only reply
	}
	if status != "" && status != "0" {
		m := resp.Trailer.Get("Grpc-Message")
		if m == "" {
			m = resp.Header.Get("Grpc-Message")
		}
		if status == "12" {
			return failKeep(r, ReasonAssertFailed, "the server does not implement grpc.health.v1 (UNIMPLEMENTED)")
		}
		if status == "5" {
			return failKeep(r, ReasonAssertFailed, "unknown service %q (NOT_FOUND)", svc)
		}
		return failKeep(r, ReasonAssertFailed, "gRPC status %s %s", status, m)
	}
	if len(body) < 5 {
		return failKeep(r, ReasonProtoError, "empty gRPC reply")
	}
	payload := body[5:]
	serving := uint64(0)
	// The reply has one field: 1 = status (varint); an empty message
	// means the default, UNKNOWN.
	if len(payload) > 1 && payload[0] == 0x08 {
		if v, n := binary.Uvarint(payload[1:]); n > 0 {
			serving = v
		}
	}
	name := grpcServing[serving]
	if name == "" {
		name = "STATUS " + strconv.FormatUint(serving, 10)
	}
	if serving != 1 {
		return failKeep(r, ReasonAssertFailed, "gRPC health: %s", name)
	}
	r.Detail = "gRPC health: SERVING"
	if svc != "" {
		r.Detail += " (" + svc + ")"
	}
	return r
}

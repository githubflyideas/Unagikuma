package probe

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// Protocol checks run over a TCP connection that is already established
// (and TLS-wrapped when tls=yes): they speak enough of the service's own
// protocol to prove it answers, and log in when credentials are given.

var protoNames = map[string]bool{"redis": true, "postgres": true, "mysql": true, "grpc": true}

// protoOpts are the options each protocol accepts on top of the TCP ones.
var protoOpts = map[string]bool{"proto": true, "user": true, "password": true, "database": true, "service": true, "role": true, "ssl": true}

func validateProto(o opts) error {
	p := strings.ToLower(o.str("proto", ""))
	if p == "" {
		for k := range protoOpts {
			if o.has(k) {
				return fmt.Errorf("%s needs proto=", k)
			}
		}
		return nil
	}
	if !protoNames[p] {
		return fmt.Errorf("unknown proto %q (redis, postgres, mysql or grpc)", p)
	}
	for _, k := range []string{"send", "send-hex", "expect", "expect-hex", "expect-regex"} {
		if o.has(k) {
			return fmt.Errorf("%s cannot be combined with proto=%s", k, p)
		}
	}
	switch p {
	case "postgres", "mysql":
		if o.on("tls") {
			return fmt.Errorf("proto=%s negotiates TLS itself: use ssl=yes instead of tls=yes", p)
		}
		if _, err := o.boolOpt("ssl", false); err != nil {
			return err
		}
	default:
		if o.has("ssl") {
			return fmt.Errorf("ssl applies to postgres and mysql; use tls=yes for %s", p)
		}
	}
	if o.has("role") && p != "redis" {
		return errors.New("role applies to proto=redis")
	}
	if r := strings.ToLower(o.str("role", "")); r != "" && r != "master" && r != "replica" && r != "slave" && r != "sentinel" {
		return fmt.Errorf("role must be master, replica or sentinel")
	}
	if o.has("service") && p != "grpc" {
		return errors.New("service applies to proto=grpc")
	}
	if o.has("database") && p != "postgres" && p != "mysql" {
		return fmt.Errorf("database applies to postgres and mysql")
	}
	return nil
}

// runProto finishes a TCP check with the service's protocol.
func runProto(ctx context.Context, conn net.Conn, host string, o opts, r Result, start time.Time) Result {
	var res Result
	switch strings.ToLower(o.str("proto", "")) {
	case "redis":
		res = redisCheck(conn, o, r)
	case "postgres":
		res = postgresCheck(ctx, conn, host, o, r)
	case "mysql":
		res = mysqlCheck(ctx, conn, host, o, r)
	case "grpc":
		res = grpcCheck(ctx, conn, host, o, r)
	default:
		return failKeep(r, ReasonConfig, "unknown proto")
	}
	res.Latency = time.Since(start)
	if res.Metrics == nil {
		res.Metrics = map[string]float64{}
	}
	res.Metrics["latency_ms"] = ms(res.Latency)
	return res
}

// protoFail turns an I/O error during a protocol exchange into a result.
func protoFail(r Result, what string, err error) Result {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return failKeep(r, ReasonProtoError, "%s: the server closed the connection", what)
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return failKeep(r, ReasonTimeout, "%s: no reply in time", what)
	}
	return failKeep(r, ReasonProtoError, "%s: %v", what, rootErr(err))
}

// ---- Redis ----

func respCommand(args ...string) []byte {
	b := []byte("*" + strconv.Itoa(len(args)) + "\r\n")
	for _, a := range args {
		b = append(b, "$"+strconv.Itoa(len(a))+"\r\n"+a+"\r\n"...)
	}
	return b
}

func redisCheck(conn net.Conn, o opts, r Result) Result {
	br := bufio.NewReader(conn)
	line := func() (string, error) {
		s, err := br.ReadString('\n')
		return strings.TrimRight(s, "\r\n"), err
	}
	if pw := o.str("password", ""); pw != "" {
		args := []string{"AUTH", pw}
		if u := o.str("user", ""); u != "" {
			args = []string{"AUTH", u, pw}
		}
		if _, err := conn.Write(respCommand(args...)); err != nil {
			return protoFail(r, "AUTH", err)
		}
		s, err := line()
		if err != nil {
			return protoFail(r, "AUTH", err)
		}
		if !strings.HasPrefix(s, "+") {
			return failKeep(r, ReasonAuthError, "Redis refused the login: %s", strings.TrimPrefix(s, "-"))
		}
	}
	if _, err := conn.Write(respCommand("PING")); err != nil {
		return protoFail(r, "PING", err)
	}
	s, err := line()
	if err != nil {
		return protoFail(r, "PING", err)
	}
	switch {
	case s == "+PONG":
	case strings.HasPrefix(s, "-NOAUTH"), strings.HasPrefix(s, "-WRONGPASS"), strings.HasPrefix(s, "-NOPERM"):
		return failKeep(r, ReasonAuthError, "Redis needs a password: %s", strings.TrimPrefix(s, "-"))
	case strings.HasPrefix(s, "-LOADING"):
		return failKeep(r, ReasonAssertFailed, "Redis is loading its dataset")
	case strings.HasPrefix(s, "-"):
		return failKeep(r, ReasonProtoError, "Redis: %s", strings.TrimPrefix(s, "-"))
	default:
		return failKeep(r, ReasonProtoError, "not a Redis reply: %q", s)
	}
	r.Detail = "Redis PONG"
	want := strings.ToLower(o.str("role", ""))
	if want == "" {
		return r
	}
	if _, err := conn.Write(respCommand("ROLE")); err != nil {
		return protoFail(r, "ROLE", err)
	}
	// ROLE answers an array whose first element is the role name.
	head, err := line()
	if err != nil || !strings.HasPrefix(head, "*") {
		return failKeep(r, ReasonProtoError, "ROLE: unexpected reply %q", head)
	}
	l, err := line()
	if err != nil || !strings.HasPrefix(l, "$") {
		return failKeep(r, ReasonProtoError, "ROLE: unexpected reply %q", l)
	}
	role, err := line()
	if err != nil {
		return protoFail(r, "ROLE", err)
	}
	got := strings.ToLower(role)
	if got == "slave" {
		got = "replica"
	}
	if want == "slave" {
		want = "replica"
	}
	if got != want {
		return failKeep(r, ReasonAssertFailed, "Redis role is %s, expected %s", got, want)
	}
	r.Detail = "Redis PONG, role " + got
	return r
}

// tlsALPN is the protocol list offered in the TLS handshake: gRPC runs
// over HTTP/2, which must be agreed there.
func tlsALPN(o opts) []string {
	if strings.EqualFold(o.str("proto", ""), "grpc") {
		return []string{"h2"}
	}
	return nil
}

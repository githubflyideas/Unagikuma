package cluster

import (
	"bufio"
	"crypto/tls"
	"errors"
	"net"
	"sync"
	"time"
)

// SniffListener serves TLS and plain connections on one port: a connection
// whose first byte starts a TLS handshake is wrapped in tls.Server, any
// other is passed through. Browsers can keep using http:// while agents
// always connect with TLS. Each connection is classified in its own
// goroutine, so a client that sends nothing cannot hold up Accept;
// timeout bounds how long it may stay silent.
func SniffListener(l net.Listener, conf *tls.Config, timeout time.Duration) net.Listener {
	s := &sniffListener{Listener: l, conf: conf, timeout: timeout, conns: make(chan net.Conn), errs: make(chan error), done: make(chan struct{})}
	go s.loop()
	return s
}

type sniffListener struct {
	net.Listener
	conf    *tls.Config
	timeout time.Duration
	conns   chan net.Conn
	errs    chan error
	done    chan struct{}
	once    sync.Once
}

func (s *sniffListener) loop() {
	for {
		c, err := s.Listener.Accept()
		if err != nil {
			select {
			case s.errs <- err:
			case <-s.done:
				return
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		go s.sniff(c)
	}
}

func (s *sniffListener) sniff(c net.Conn) {
	_ = c.SetReadDeadline(time.Now().Add(s.timeout))
	br := bufio.NewReader(c)
	b, err := br.Peek(1)
	_ = c.SetReadDeadline(time.Time{})
	if err != nil {
		c.Close()
		return
	}
	var out net.Conn = &peekedConn{Conn: c, r: br}
	if b[0] == 0x16 { // TLS handshake record
		out = tls.Server(out, s.conf)
	}
	select {
	case s.conns <- out:
	case <-s.done:
		c.Close()
	}
}

func (s *sniffListener) Accept() (net.Conn, error) {
	select {
	case c := <-s.conns:
		return c, nil
	case err := <-s.errs:
		return nil, err
	case <-s.done:
		return nil, net.ErrClosed
	}
}

func (s *sniffListener) Close() error {
	s.once.Do(func() { close(s.done) })
	return s.Listener.Close()
}

// peekedConn reads the bytes consumed while sniffing before the rest.
type peekedConn struct {
	net.Conn
	r *bufio.Reader
}

func (p *peekedConn) Read(b []byte) (int, error) { return p.r.Read(b) }

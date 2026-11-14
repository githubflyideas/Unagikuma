package probe

import (
	"context"
	"encoding/binary"
	"net"
	"net/netip"
	"testing"
	"time"
)

// fakeDNS answers every A question with 10.1.2.3 over UDP.
func fakeDNS(t *testing.T) string {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 512)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			q := buf[:n]
			// Header: same id, response, recursion available, one question,
			// one answer if the question is A, none otherwise.
			end := 12
			for end < len(q) && q[end] != 0 {
				end += int(q[end]) + 1
			}
			end += 5 // root label, type, class
			if end > len(q) {
				continue
			}
			qtype := binary.BigEndian.Uint16(q[end-4:])
			resp := append([]byte{}, q[:end]...)
			resp[2], resp[3] = 0x81, 0x80
			binary.BigEndian.PutUint16(resp[6:], 0)
			binary.BigEndian.PutUint16(resp[8:], 0)
			binary.BigEndian.PutUint16(resp[10:], 0)
			if qtype == 1 {
				binary.BigEndian.PutUint16(resp[6:], 1)
				resp = append(resp, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 10, 1, 2, 3)
			}
			pc.WriteTo(resp, from)
		}
	}()
	return pc.LocalAddr().String()
}

func TestChecksUseTheDNSServersSet(t *testing.T) {
	addr := fakeDNS(t)
	SetDNSServers([]string{addr})
	defer SetDNSServers(nil)
	if got := DNSServers(); len(got) != 1 || got[0] != addr {
		t.Fatalf("servers: %v", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	a, err := resolve(ctx, "anything.example.test", opts{"family": "4"})
	if err != nil || a != netip.MustParseAddr("10.1.2.3") {
		t.Fatalf("resolved %v, %v", a, err)
	}
	SetDNSServers([]string{"", " "})
	if len(DNSServers()) != 0 {
		t.Fatal("blank servers kept")
	}
}

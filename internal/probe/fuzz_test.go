package probe

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"
)

// Replies come from the network: no reply, however malformed, may crash a
// probe or make it hang.

func FuzzParseDNS(f *testing.F) {
	f.Add(dnsQuery(7, "example.com", 1, true))
	f.Add([]byte{0, 1, 0x84, 0, 0, 1, 0, 1, 0, 0, 0, 0, 0xC0, 0x0C})
	f.Fuzz(func(t *testing.T, msg []byte) {
		m, err := parseDNS(msg)
		if err == nil && len(m.Answers) > 256 {
			t.Fatal("answer count not bounded")
		}
	})
}

func FuzzBER(f *testing.F) {
	f.Add([]byte{0x30, 0x03, 0x02, 0x01, 0x01})
	f.Add(snmpTrap("public"))
	f.Fuzz(func(t *testing.T, b []byte) {
		var walk func(b []byte, depth int)
		walk = func(b []byte, depth int) {
			els, err := berChildren(b, 64)
			if err != nil || depth > 8 {
				return
			}
			for _, e := range els {
				snmpValue(e)
				decodeOID(e.body)
				if e.tag&0x20 != 0 {
					walk(e.body, depth+1)
				}
			}
		}
		walk(b, 0)
	})
}

func FuzzJSONPath(f *testing.F) {
	f.Add([]byte(`{"a":{"b":[1,2,{"c":"ok"}]}}`), "$.a.b[2].c")
	f.Add([]byte(`[1,2,3]`), "$[1]")
	f.Fuzz(func(t *testing.T, data []byte, path string) {
		checkJSON(data, path, opts{"json-expect": "ok"})
	})
}

// FuzzUDPReplies answers real probes with fuzzed datagrams.
func FuzzUDPReplies(f *testing.F) {
	f.Add(uint8(0), []byte{0x1c, 2, 3})
	f.Add(uint8(1), []byte{0x30, 0x03, 0x02, 0x01, 0x01})
	f.Add(uint8(2), dnsQuery(1, "x", 1, true))
	templates := []string{"ntp", "snmp", "dns", "tftp", "sunrpc", "time"}
	f.Fuzz(func(t *testing.T, which uint8, reply []byte) {
		pc, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Skip(err)
		}
		defer pc.Close()
		go func() {
			buf := make([]byte, 2048)
			for {
				_, addr, err := pc.ReadFrom(buf)
				if err != nil {
					return
				}
				pc.WriteTo(reply, addr)
			}
		}()
		port := pc.LocalAddr().(*net.UDPAddr).Port
		tmpl := templates[int(which)%len(templates)]
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		done := make(chan struct{})
		go func() {
			Run(ctx, Spec{Name: "f", Type: "UDP", Target: "127.0.0.1:" + strconv.Itoa(port), Timeout: 300 * time.Millisecond,
				Opts: map[string]string{"template": tmpl, "authorized": "yes", "query": "example.com"}})
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatalf("%s probe hung on reply %x", tmpl, reply)
		}
	})
}

package probe

import (
	"bytes"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// The localized keys of RFC 3414 A.3: password "maplesyrup", engine
// 000000000000000000000002.
func TestSNMPv3KeyVectors(t *testing.T) {
	engine, _ := hex.DecodeString("000000000000000000000002")
	for proto, want := range map[string]string{
		"md5": "526f5eed9fcce26f8964c2930787d82b",
		"sha": "6695febc9288e36282235fc7151f128497b38f3f",
	} {
		if got := hex.EncodeToString(passwordToKey(authHash(proto), "maplesyrup", engine)); got != want {
			t.Errorf("%s: %s, want %s", proto, got, want)
		}
	}
}

// mibAgent is an SNMP agent over a fixed MIB (OID -> encoded value). It
// answers GET and GETNEXT in v2c with the community, or in v3 with user
// (whose keys it localizes to its own engine), checking digests and
// decrypting like a real agent.
type mibAgent struct {
	mu        sync.Mutex
	mib       map[string][]byte
	community string
	user      *usmUser
	engineID  []byte
	gets      int
}

func (a *mibAgent) set(oid string, v []byte) {
	a.mu.Lock()
	a.mib[oid] = v
	a.mu.Unlock()
}

func oidLess(x, y string) bool {
	xs, ys := strings.Split(x, "."), strings.Split(y, ".")
	for i := 0; i < len(xs) && i < len(ys); i++ {
		a, _ := strconv.Atoi(xs[i])
		b, _ := strconv.Atoi(ys[i])
		if a != b {
			return a < b
		}
	}
	return len(xs) < len(ys)
}

func (a *mibAgent) answer(tag byte, pdu berElem) []byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	f, _ := berChildren(pdu.body, 4)
	vbs, _ := berChildren(f[3].body, 64)
	var keys []string
	for k := range a.mib {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return oidLess(keys[i], keys[j]) })
	var out [][]byte
	for _, vb := range vbs {
		pair, _ := berChildren(vb.body, 2)
		oid := decodeOID(pair[0].body)
		val, name := []byte{0x80, 0}, oid // noSuchObject
		if tag == snmpGetNext {
			val = []byte{0x82, 0} // endOfMibView
			for _, k := range keys {
				if oidLess(oid, k) {
					name, val = k, a.mib[k]
					break
				}
			}
		} else if v, ok := a.mib[oid]; ok {
			val = v
			a.gets++
		}
		enc, _ := encodeOID(name)
		out = append(out, tlv(berSeq, enc, val))
	}
	return tlv(snmpResponse, tlv(berInt, f[0].body), berInteger(0), berInteger(0), tlv(berSeq, out...))
}

func (a *mibAgent) serve(t *testing.T) string {
	return udpServer(t, func(req []byte) []byte {
		msg, _, err := berRead(req)
		if err != nil {
			return nil
		}
		parts, err := berChildren(msg.body, 4)
		if err != nil {
			return nil
		}
		if berInt64(parts[0].body) != 3 {
			if a.user != nil || string(parts[1].body) != a.community {
				return nil
			}
			return tlv(berSeq, tlv(berInt, parts[0].body), tlv(berOctets, parts[1].body), a.answer(parts[2].tag, parts[2]))
		}
		return a.serveV3(req, parts)
	})
}

func (a *mibAgent) report(msgID []byte, oid string) []byte {
	enc, _ := encodeOID(oid)
	usm := tlv(berSeq, tlv(berOctets, a.engineID), berInteger(7), berInteger(5000), tlv(berOctets), tlv(berOctets), tlv(berOctets))
	rep := tlv(snmpReport, berInteger(0), berInteger(0), berInteger(0), tlv(berSeq, tlv(berSeq, enc, tlv(0x41, []byte{1}))))
	return tlv(berSeq, berInteger(3), tlv(berSeq, tlv(berInt, msgID), berInteger(65507), tlv(berOctets, []byte{0}), berInteger(3)),
		tlv(berOctets, usm), tlv(berSeq, tlv(berOctets, a.engineID), tlv(berOctets), rep))
}

func (a *mibAgent) serveV3(req []byte, parts []berElem) []byte {
	hdr, _ := berChildren(parts[1].body, 4)
	flags := hdr[2].body[0]
	sec, _, _ := berRead(parts[2].body)
	f, _ := berChildren(sec.body, 6)
	if len(f[3].body) == 0 {
		return a.report(hdr[0].body, "1.3.6.1.6.3.15.1.1.4.0") // discovery: here is my engine
	}
	u := a.user
	if u == nil || string(f[3].body) != u.name {
		return a.report(hdr[0].body, "1.3.6.1.6.3.15.1.1.3.0")
	}
	if flags&flagAuth != 0 {
		got := f[4].body
		zeroed := bytes.Replace(req, append([]byte{berOctets, byte(len(got))}, got...), append([]byte{berOctets, byte(len(got))}, make([]byte, len(got))...), 1)
		if !bytes.Equal(u.mac(zeroed), got) {
			return a.report(hdr[0].body, "1.3.6.1.6.3.15.1.1.5.0")
		}
	}
	scoped := parts[3]
	if flags&flagPriv != 0 {
		plain, err := u.decrypt(scoped.body, f[5].body, berInt64(f[1].body), berInt64(f[2].body))
		if err != nil {
			return a.report(hdr[0].body, "1.3.6.1.6.3.15.1.1.6.0")
		}
		if scoped, _, err = berRead(plain); err != nil {
			return a.report(hdr[0].body, "1.3.6.1.6.3.15.1.1.6.0")
		}
	}
	sp, _ := berChildren(scoped.body, 3)
	resp := tlv(berSeq, tlv(berOctets, a.engineID), tlv(berOctets), a.answer(sp[2].tag, sp[2]))
	var privParams []byte
	payload := resp
	if flags&flagPriv != 0 {
		enc, salt, _ := u.encrypt(resp, 7, 5000)
		privParams, payload = salt, tlv(berOctets, enc)
	}
	n := authLen(u.authProto)
	usm := tlv(berSeq, tlv(berOctets, a.engineID), berInteger(7), berInteger(5000), tlv(berOctets, []byte(u.name)), tlv(berOctets, make([]byte, n)), tlv(berOctets, privParams))
	out := tlv(berSeq, berInteger(3), tlv(berSeq, tlv(berInt, hdr[0].body), berInteger(65507), tlv(berOctets, []byte{flags &^ flagReportable}), berInteger(3)), tlv(berOctets, usm), payload)
	if flags&flagAuth != 0 {
		mac := u.mac(out)
		i := bytes.Index(out, append([]byte{berOctets, byte(n)}, make([]byte, n)...))
		copy(out[i+2:], mac)
	}
	return out
}

func counter64(v uint64) []byte {
	b := []byte{byte(v >> 56), byte(v >> 48), byte(v >> 40), byte(v >> 32), byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
	for len(b) > 1 && b[0] == 0 && b[1] < 0x80 {
		b = b[1:]
	}
	if b[0] >= 0x80 {
		b = append([]byte{0}, b...)
	}
	return tlv(0x46, b)
}

func switchMIB() map[string][]byte {
	return map[string][]byte{
		oidIfName + ".1": tlv(berOctets, []byte("Gi0/1")), oidIfName + ".2": tlv(berOctets, []byte("Gi0/2")),
		oidIfDescr + ".1": tlv(berOctets, []byte("GigabitEthernet0/1")), oidIfDescr + ".2": tlv(berOctets, []byte("GigabitEthernet0/2")),
		oidIfHCIn + ".2": counter64(1 << 40), oidIfHCOut + ".2": counter64(5000),
		oidIfHighSpeed + ".2": berInteger(1000), oidIfOper + ".2": berInteger(1), oidIfAdmin + ".2": berInteger(1),
		oidIfOper + ".1": berInteger(2), oidIfAdmin + ".1": berInteger(1), oidIfHighSpeed + ".1": berInteger(1000),
		oidIfHCIn + ".1": counter64(0), oidIfHCOut + ".1": counter64(0),
		oidEntName + ".1001": tlv(berOctets, []byte("Chassis")), oidEntName + ".1007": tlv(berOctets, []byte("Inlet Temperature")),
		oidEntSensorVal + ".1007": berInteger(415), oidEntSensorScl + ".1007": berInteger(9), oidEntSensorPrec + ".1007": berInteger(1),
		oidEntSensorType + ".1007": berInteger(8), oidHuaweiTemp + ".1007": berInteger(44),
		sysUpTime: tlv(0x43, []byte{0x01, 0x00}),
	}
}

func TestSNMPPortAndSensor(t *testing.T) {
	a := &mibAgent{mib: switchMIB(), community: "ro"}
	addr := a.serve(t)
	r := run(t, udpSpec(addr, "snmp", map[string]string{"community": "ro", "port": "Gi0/2"}))
	want(t, r, VoteOK, ReasonOK)
	if r.Metrics["if.in_octets"] != 1<<40 || r.Metrics["if.speed_bps"] != 1e9 || r.Metrics["if.oper"] != 1 {
		t.Fatalf("port metrics %v", r.Metrics)
	}
	if strings.Contains(string(r.Evidence.Request), "ro\x30") {
		t.Fatal("community in evidence")
	}
	// A port that is down (and not shut down by hand) fails.
	r = run(t, udpSpec(addr, "snmp", map[string]string{"community": "ro", "port": "GigabitEthernet0/1"}))
	want(t, r, VoteFail, ReasonAssertFailed)
	if !strings.Contains(r.Detail, "down") {
		t.Fatalf("detail %q", r.Detail)
	}
	want(t, run(t, udpSpec(addr, "snmp", map[string]string{"community": "ro", "port": "Te9/9"})), VoteFail, ReasonAssertFailed)
	// Renumbered after a reboot: found again by name.
	a.set(oidIfName+".2", tlv(berOctets, []byte("Gi0/9")))
	a.set(oidIfName+".5", tlv(berOctets, []byte("Gi0/2")))
	a.set(oidIfDescr+".5", tlv(berOctets, []byte("GigabitEthernet0/2")))
	for _, k := range []string{oidIfHCIn, oidIfHCOut, oidIfHighSpeed, oidIfOper, oidIfAdmin} {
		a.set(k+".5", a.mib[k+".2"])
	}
	r = run(t, udpSpec(addr, "snmp", map[string]string{"community": "ro", "port": "Gi0/2"}))
	if r.Vote != VoteOK || !strings.Contains(r.Detail, "ifIndex 5") {
		t.Fatalf("renumbered: %+v", r)
	}
	// Temperature: ENTITY-SENSOR-MIB with its scale and precision, and a
	// vendor table.
	r = run(t, udpSpec(addr, "snmp", map[string]string{"community": "ro", "sensor": "inlet"}))
	if r.Vote != VoteOK || r.Metrics["value"] != 41.5 {
		t.Fatalf("sensor: %+v", r)
	}
	r = run(t, udpSpec(addr, "snmp", map[string]string{"community": "ro", "sensor": "Inlet Temperature", "profile": "huawei"}))
	if r.Vote != VoteOK || r.Metrics["value"] != 44 {
		t.Fatalf("huawei sensor: %+v", r)
	}
}

func TestSNMPv3(t *testing.T) {
	engine := []byte{0x80, 0, 0x1f, 0x88, 0x80, 1, 2, 3, 4, 5}
	for _, c := range []struct{ auth, priv string }{{"sha", ""}, {"md5", "des"}, {"sha", "aes"}, {"sha256", "aes"}, {"sha512", "des"}} {
		agentUser := &usmUser{name: "mon", authProto: c.auth, privPro: c.priv, authPass: "auth-secret-1", privPass: "priv-secret-1", engineID: engine}
		agentUser.localize()
		a := &mibAgent{mib: switchMIB(), user: agentUser, engineID: engine}
		addr := a.serve(t)
		o := map[string]string{"version": "3", "user": "mon", "auth": c.auth, "auth-pass": "auth-secret-1", "port": "Gi0/2"}
		if c.priv != "" {
			o["priv"], o["priv-pass"] = c.priv, "priv-secret-1"
		}
		if err := Validate(udpSpec(addr, "snmp", o)); err != nil {
			t.Fatal(err)
		}
		r := run(t, udpSpec(addr, "snmp", o))
		if r.Vote != VoteOK || r.Metrics["if.in_octets"] != 1<<40 {
			t.Fatalf("%s/%s: %+v", c.auth, c.priv, r)
		}
		for _, k := range []string{"auth-secret-1", "priv-secret-1"} {
			if strings.Contains(string(r.Evidence.Request)+string(r.Evidence.Response), k) {
				t.Fatal("password in evidence")
			}
		}
		bad := map[string]string{}
		for k, v := range o {
			bad[k] = v
		}
		bad["auth-pass"], bad["port"] = "wrong-password", ""
		delete(bad, "port")
		r = run(t, udpSpec(addr, "snmp", bad))
		if r.Vote != VoteFail || r.Reason != ReasonAuthError || !strings.Contains(r.Detail, "wrong authentication") {
			t.Fatalf("%s/%s wrong password: %+v", c.auth, c.priv, r)
		}
	}
	for _, bad := range []map[string]string{
		{"version": "3"},
		{"version": "3", "user": "u", "auth": "sha", "auth-pass": "short"},
		{"version": "3", "user": "u", "priv": "aes", "priv-pass": "longenough"},
		{"version": "3", "user": "u", "auth": "sha1x", "auth-pass": "longenough"},
		{"port": "x", "sensor": "y"},
		{"sensor": "y", "profile": "acme"},
		{"warn": "hot"},
	} {
		if err := Validate(udpSpec("127.0.0.1:161", "snmp", bad)); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
}

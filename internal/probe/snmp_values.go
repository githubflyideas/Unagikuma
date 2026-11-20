package probe

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
)

// SNMP checks that read numbers: one OID (any version, v3 included), a
// network port found by its name (port=), or a temperature sensor found by
// its name (sensor=). A port or sensor is looked up by name once with
// GETNEXT and remembered; every round then GETs only its few values, and
// the name is read back each time so a renumbered device is noticed.

const (
	oidIfDescr       = "1.3.6.1.2.1.2.2.1.2"
	oidIfSpeed       = "1.3.6.1.2.1.2.2.1.5"
	oidIfAdmin       = "1.3.6.1.2.1.2.2.1.7"
	oidIfOper        = "1.3.6.1.2.1.2.2.1.8"
	oidIfInOctets    = "1.3.6.1.2.1.2.2.1.10"
	oidIfOutOctets   = "1.3.6.1.2.1.2.2.1.16"
	oidIfName        = "1.3.6.1.2.1.31.1.1.1.1"
	oidIfHCIn        = "1.3.6.1.2.1.31.1.1.1.6"
	oidIfHCOut       = "1.3.6.1.2.1.31.1.1.1.10"
	oidIfHighSpeed   = "1.3.6.1.2.1.31.1.1.1.15"
	oidIfAlias       = "1.3.6.1.2.1.31.1.1.1.18"
	oidEntName       = "1.3.6.1.2.1.47.1.1.1.1.7"
	oidEntSensorType = "1.3.6.1.2.1.99.1.1.1.1"
	oidEntSensorScl  = "1.3.6.1.2.1.99.1.1.1.2"
	oidEntSensorPrec = "1.3.6.1.2.1.99.1.1.1.3"
	oidEntSensorVal  = "1.3.6.1.2.1.99.1.1.1.4"
	// Vendor temperature tables, indexed like the entity table (Huawei,
	// H3C) or with their own names (Cisco).
	oidHuaweiTemp    = "1.3.6.1.4.1.2011.5.25.31.1.1.1.1.11"
	oidH3CTemp       = "1.3.6.1.4.1.25506.2.6.1.1.1.1.12"
	oidCiscoTempName = "1.3.6.1.4.1.9.9.13.1.3.1.2"
	oidCiscoTempVal  = "1.3.6.1.4.1.9.9.13.1.3.1.3"
)

// snmpProfiles are the temperature sources sensor= can read.
var snmpProfiles = map[string]bool{"entity": true, "huawei": true, "h3c": true, "cisco": true}

// maxLookup bounds a name lookup: GETNEXT steps through one column.
const maxLookup = 1024

// snmpView names who is asking: the same device lists its interfaces and
// sensors differently to another community or user (a view, a context).
func snmpView(o opts) string {
	return o.str("version", "2c") + "/" + o.str("community", "") + "/" + o.str("user", "") + "/" + o.str("context", "")
}

// snmpIndexes remembers where a port or sensor was found, per device.
var snmpIndexes sync.Map // key string -> string (the index)

func validateSNMPValues(o opts) error {
	v := o.str("version", "2c")
	if v == "3" {
		if o.str("user", "") == "" {
			return errors.New("SNMPv3 needs user=")
		}
		if len(o.str("context", "")) > 32 {
			return errors.New("context= is at most 32 characters (an SNMP contextName)")
		}
		switch a := o.str("auth", ""); a {
		case "", "md5", "sha", "sha1", "sha224", "sha256", "sha384", "sha512":
			if a != "" && len(o.str("auth-pass", "")) < 8 && !strings.HasPrefix(o.str("auth-pass", ""), "${") {
				return errors.New("auth-pass must be at least 8 characters")
			}
		default:
			return errors.New("auth must be md5, sha, sha224, sha256, sha384 or sha512")
		}
		switch p := o.str("priv", ""); p {
		case "":
		case "des", "aes", "aes128":
			if o.str("auth", "") == "" {
				return errors.New("priv= needs auth=: SNMPv3 encrypts only authenticated messages")
			}
			if len(o.str("priv-pass", "")) < 8 && !strings.HasPrefix(o.str("priv-pass", ""), "${") {
				return errors.New("priv-pass must be at least 8 characters")
			}
		default:
			return errors.New("priv must be des or aes")
		}
	}
	if o.has("port") && o.has("sensor") {
		return errors.New("port= and sensor= cannot be used together")
	}
	if p := o.str("profile", ""); p != "" && !snmpProfiles[p] {
		return errors.New("profile must be entity, huawei, h3c or cisco")
	}
	if o.has("profile") && !o.has("sensor") {
		return errors.New("profile= goes with sensor=")
	}
	return nil
}

// usesSNMPClient reports whether a spec needs the multi-value client.
func usesSNMPClient(o opts) bool {
	return o.str("version", "2c") == "3" || o.has("port") || o.has("sensor")
}

func runSNMPValues(ctx context.Context, p udpPlan, o opts) Result {
	c, r, ok := newSNMPClient(ctx, p, o)
	if !ok {
		return r
	}
	defer c.Close()
	var res Result
	switch {
	case o.has("port"):
		res = snmpPort(ctx, c, p, o)
	case o.has("sensor"):
		res = snmpSensor(ctx, c, p, o)
	default:
		res = snmpOID(ctx, c, o)
	}
	res.Latency = c.lat
	if res.Evidence == nil {
		res.Evidence = newEvidence(c.u.peer, c.masked(c.req), c.masked(c.resp))
	}
	if res.Metrics == nil {
		res.Metrics = map[string]float64{}
	}
	res.Metrics["latency_ms"] = ms(c.lat)
	return res
}

// snmpFail turns a client error into a result.
func snmpFail(c *snmpClient, err error, o opts) Result {
	var auth snmpAuthFailed
	if errors.As(err, &auth) {
		return Result{Vote: VoteFail, Reason: ReasonAuthError, Detail: "SNMPv3: " + auth.why}
	}
	if errors.Is(err, errNotOurs) || strings.Contains(err.Error(), "error-status") || strings.Contains(err.Error(), "malformed") {
		return Result{Vote: VoteFail, Reason: ReasonProtoError, Detail: err.Error()}
	}
	hint := "check the community string and the agent's ACL"
	if c.version == 3 {
		hint = "check the user, passwords and security level, and the agent's ACL"
	}
	return udpFail(c.u, err, c.masked(c.req), o, hint)
}

func snmpOID(ctx context.Context, c *snmpClient, o opts) Result {
	oid := o.str("oid", sysUpTime)
	vbs, err := c.get(ctx, oid)
	if err != nil {
		return snmpFail(c, err, o)
	}
	if len(vbs) == 0 {
		return Result{Vote: VoteFail, Reason: ReasonProtoError, Detail: "empty varbind list"}
	}
	res := Result{Metrics: map[string]float64{}}
	snmpJudge(&res, oid, vbs[0].Value, o)
	return res
}

// snmpJudge reads one value into res (its numbers for the value rules) and
// judges it against expect= and expect-num=. Both paths that read an OID
// (v1/v2c and the client) use it, so they cannot drift apart.
//
// A text is matched by containing expect=; a number is not text: expect=
// must be that number exactly (10 does not match 100), and a comparison
// is expect-num=. A counter (Counter32 and Counter64) only ever grows, so
// the value rules judge its rate and not its total.
func snmpJudge(res *Result, oid string, e berElem, o opts) {
	text, num, isNum, missing := snmpValue(e)
	if isNum {
		res.Metrics["snmp.value"], res.Metrics["value"] = num, num
		if e.tag == 0x41 || e.tag == 0x46 {
			res.Metrics["value.counter"] = 1
		}
	}
	res.Vote, res.Reason, res.Detail = VoteOK, ReasonOK, oid+" = "+text
	fail := func(format string, args ...any) {
		res.Vote, res.Reason, res.Detail = VoteFail, ReasonAssertFailed, fmt.Sprintf(format, args...)
	}
	exp, expNum := o.str("expect", ""), o.str("expect-num", "")
	switch {
	case missing:
		res.Vote, res.Reason = VoteFail, ReasonAssertFailed
	case exp != "" && isNum:
		if want, err := strconv.ParseFloat(strings.TrimSpace(exp), 64); err != nil {
			fail("%s = %s is a number: expect= must be that number (use expect-num= to compare), got %q", oid, text, exp)
		} else if want != num {
			fail("%s = %s, expected %s", oid, text, exp)
		}
	case exp != "" && !strings.Contains(text, exp):
		fail("%s = %q does not contain %q", oid, text, exp)
	}
	if res.Vote == VoteOK && expNum != "" {
		ne, _ := parseNumExpect(expNum)
		if !isNum || !ne.match(num) {
			fail("%s = %s, expected %s", oid, text, ne)
		}
	}
}

// snmpLookup finds the index of the row of column whose text is name: exactly
// (ignoring case), else the only row containing it.
func snmpLookup(ctx context.Context, c *snmpClient, column, name string) (string, error) {
	want := strings.ToLower(name)
	var partial []string
	oid := column
	for i := 0; i < maxLookup; i++ {
		vb, err := c.next(ctx, oid)
		if err != nil {
			return "", err
		}
		if !strings.HasPrefix(vb.OID, column+".") {
			break // past the column
		}
		oid = vb.OID
		text, _, _, missing := snmpValue(vb.Value)
		if missing {
			break
		}
		idx := strings.TrimPrefix(vb.OID, column+".")
		got := strings.ToLower(strings.TrimSpace(text))
		if got == want {
			return idx, nil
		}
		if strings.Contains(got, want) {
			partial = append(partial, idx)
		}
	}
	if len(partial) == 1 {
		return partial[0], nil
	}
	return "", nil
}

func snmpPort(ctx context.Context, c *snmpClient, p udpPlan, o opts) Result {
	name := o.str("port", "")
	key := "port|" + snmpView(o) + "|" + p.host + "|" + strconv.Itoa(p.port) + "|" + name
	idx, cached := "", false
	if v, ok := snmpIndexes.Load(key); ok {
		idx, cached = v.(string), true
	}
	for attempt := 0; attempt < 2; attempt++ {
		if idx == "" {
			var err error
			for _, col := range []string{oidIfName, oidIfDescr, oidIfAlias} {
				if idx, err = snmpLookup(ctx, c, col, name); err != nil {
					return snmpFail(c, err, o)
				}
				if idx != "" {
					break
				}
			}
			if idx == "" {
				return Result{Vote: VoteFail, Reason: ReasonAssertFailed, Detail: fmt.Sprintf("no port named %q on this device", name)}
			}
			snmpIndexes.Store(key, idx)
		}
		vbs, err := c.get(ctx, oidIfName+"."+idx, oidIfDescr+"."+idx, oidIfHCIn+"."+idx, oidIfHCOut+"."+idx, oidIfHighSpeed+"."+idx,
			oidIfOper+"."+idx, oidIfAdmin+"."+idx, oidIfInOctets+"."+idx, oidIfOutOctets+"."+idx, oidIfSpeed+"."+idx)
		if err != nil {
			return snmpFail(c, err, o)
		}
		val := map[string]berElem{}
		for _, vb := range vbs {
			val[strings.TrimSuffix(vb.OID, "."+idx)] = vb.Value
		}
		str := func(col string) string { s, _, _, _ := snmpValue(val[col]); return strings.TrimSpace(s) }
		num := func(col string) (float64, bool) {
			_, n, ok, missing := snmpValue(val[col])
			return n, ok && !missing
		}
		// Renumbered (a reboot, a new card): look the name up again.
		if cached && attempt == 0 && !strings.EqualFold(str(oidIfName), name) && !strings.EqualFold(str(oidIfDescr), name) {
			snmpIndexes.Delete(key)
			idx = ""
			continue
		}
		m := map[string]float64{}
		in, okIn := num(oidIfHCIn)
		out, okOut := num(oidIfHCOut)
		if !okIn || !okOut { // 32-bit counters on old devices
			in, okIn = num(oidIfInOctets)
			out, okOut = num(oidIfOutOctets)
		}
		if okIn && okOut {
			m["if.in_octets"], m["if.out_octets"] = in, out
		}
		if hs, ok := num(oidIfHighSpeed); ok && hs > 0 {
			m["if.speed_bps"] = hs * 1e6
		} else if sp, ok := num(oidIfSpeed); ok && sp > 0 && sp < math.MaxUint32 {
			m["if.speed_bps"] = sp
		}
		oper, _ := num(oidIfOper)
		admin, _ := num(oidIfAdmin)
		m["if.oper"], m["if.admin"] = oper, admin
		res := Result{Vote: VoteOK, Reason: ReasonOK, Metrics: m}
		label := name + " (ifIndex " + idx + ")"
		switch {
		case admin == 2:
			res.Detail = label + " is shut down by an administrator"
		case oper != 1:
			res.Vote, res.Reason, res.Detail = VoteFail, ReasonAssertFailed, fmt.Sprintf("%s is %s", label, operName(oper))
		default:
			res.Detail = label + " is up"
			if sp := m["if.speed_bps"]; sp > 0 {
				res.Detail += fmt.Sprintf(", %s", speedText(sp))
			}
		}
		return res
	}
	return Result{Vote: VoteFail, Reason: ReasonAssertFailed, Detail: fmt.Sprintf("no port named %q on this device", name)}
}

func operName(v float64) string {
	return map[float64]string{1: "up", 2: "down", 3: "testing", 4: "unknown", 5: "dormant", 6: "not present", 7: "lower layer down"}[v]
}

func speedText(bps float64) string {
	switch {
	case bps >= 1e9:
		return strconv.FormatFloat(bps/1e9, 'f', -1, 64) + " Gbps"
	case bps >= 1e6:
		return strconv.FormatFloat(bps/1e6, 'f', -1, 64) + " Mbps"
	}
	return strconv.FormatFloat(bps, 'f', -1, 64) + " bps"
}

func snmpSensor(ctx context.Context, c *snmpClient, p udpPlan, o opts) Result {
	name, profile := o.str("sensor", ""), o.str("profile", "entity")
	nameCol := oidEntName
	if profile == "cisco" {
		nameCol = oidCiscoTempName
	}
	key := "sensor|" + profile + "|" + snmpView(o) + "|" + p.host + "|" + strconv.Itoa(p.port) + "|" + name
	idx := ""
	if v, ok := snmpIndexes.Load(key); ok {
		idx = v.(string)
	} else {
		var err error
		if idx, err = snmpLookup(ctx, c, nameCol, name); err != nil {
			return snmpFail(c, err, o)
		}
		if idx == "" {
			return Result{Vote: VoteFail, Reason: ReasonAssertFailed, Detail: fmt.Sprintf("no sensor named %q on this device", name)}
		}
		snmpIndexes.Store(key, idx)
	}
	var oids []string
	switch profile {
	case "huawei":
		oids = []string{oidHuaweiTemp + "." + idx}
	case "h3c":
		oids = []string{oidH3CTemp + "." + idx}
	case "cisco":
		oids = []string{oidCiscoTempVal + "." + idx}
	default:
		oids = []string{oidEntSensorVal + "." + idx, oidEntSensorScl + "." + idx, oidEntSensorPrec + "." + idx, oidEntSensorType + "." + idx}
	}
	vbs, err := c.get(ctx, oids...)
	if err != nil {
		return snmpFail(c, err, o)
	}
	nums := make([]float64, len(vbs))
	for i, vb := range vbs {
		_, n, ok, missing := snmpValue(vb.Value)
		if !ok || missing {
			snmpIndexes.Delete(key) // look it up again next round
			return Result{Vote: VoteFail, Reason: ReasonAssertFailed, Detail: fmt.Sprintf("sensor %q gave no reading (%s)", name, vb.OID)}
		}
		nums[i] = n
	}
	v := nums[0]
	if profile == "entity" {
		// value × 10^(3·(scale−9)) ÷ 10^precision (ENTITY-SENSOR-MIB).
		v = v * math.Pow(10, 3*(nums[1]-9)) / math.Pow(10, nums[2])
	}
	return Result{Vote: VoteOK, Reason: ReasonOK, Detail: fmt.Sprintf("%s = %s °C", name, strconv.FormatFloat(v, 'f', -1, 64)),
		Metrics: map[string]float64{"value": v}}
}

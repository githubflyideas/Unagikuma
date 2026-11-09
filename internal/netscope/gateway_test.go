package netscope

import (
	"net/netip"
	"runtime"
	"strings"
	"testing"
)

func TestParseProcRoute(t *testing.T) {
	const tbl = `Iface	Destination	Gateway 	Flags	RefCnt	Use	Metric	Mask		MTU	Window	IRTT
wlan0	00000000	0101A8C0	0003	0	0	600	00000000	0	0	0
eth0	00000000	0100000A	0003	0	0	100	00000000	0	0	0
eth0	0000000A	00000000	0001	0	0	100	00FFFFFF	0	0	0
tun0	00000000	00000000	0001	0	0	50	00000000	0	0	0
`
	gws := parseProcRoute(strings.NewReader(tbl))
	if len(gws) != 1 || gws[0].Addr != netip.MustParseAddr("10.0.0.1") || gws[0].Iface != "eth0" {
		t.Fatalf("%+v", gws)
	}
	if gws := parseProcRoute(strings.NewReader("header\n")); gws != nil {
		t.Fatalf("no default route: %+v", gws)
	}
}

func TestParseProcIPv6Route(t *testing.T) {
	const tbl = `00000000000000000000000000000000 00 00000000000000000000000000000000 00 fe800000000000000000000000000001 00000400 00000001 00000000 00000003 eth0
00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 ffffffff 00000001 00000000 00200200 lo
20010db8000000000000000000000000 40 00000000000000000000000000000000 00 00000000000000000000000000000000 00000100 00000001 00000000 00000001 eth0
`
	gws := parseProcIPv6Route(strings.NewReader(tbl))
	if len(gws) != 1 || gws[0].Addr != netip.MustParseAddr("fe80::1") || gws[0].Iface != "eth0" {
		t.Fatalf("%+v", gws)
	}
}

func TestContainerCgroup(t *testing.T) {
	if !containerCgroup("0::/system.slice/docker-abc.scope") || !containerCgroup("12:pids:/kubepods/burstable/pod1") || containerCgroup("0::/init.scope") {
		t.Fatal("cgroup detection")
	}
}

func TestDefaultGatewaysRuns(t *testing.T) {
	gws, err := DefaultGateways()
	if runtime.GOOS == "linux" && err != nil {
		t.Fatal(err)
	}
	for _, g := range gws {
		if !g.Addr.IsValid() {
			t.Fatalf("invalid gateway %+v", g)
		}
	}
	_ = InContainer()
}

package importer

import (
	"strings"
	"testing"
)

// A set whose host definitions are generated elsewhere (the Zulip
// shape): services name hosts, and host groups name members, that no
// file defines. The import is not refused wholesale: a ping host is
// made up for each named host, the report says so, and the services
// attach to it. A service whose group has no members anywhere gets
// its own precise reason.
func TestNagiosSynthesizedHosts(t *testing.T) {
	data := []byte(`
define command {
	command_name check_http
	command_line $USER1$/check_http -H $HOSTADDRESS$ $ARG1$
}
define hostgroup {
	hostgroup_name db-servers
	alias DB
	members db1
}
define service {
	host_name db1, db2
	service_description HTTP
	check_command check_http!-u /health
}
define service {
	hostgroup_name db-servers
	service_description HTTP via group
	check_command check_http!-u /health
}
define service {
	hostgroup_name ghost-group
	service_description Nowhere
	check_command check_http!-u /health
}
define hostgroup {
	hostgroup_name ghost-group
	alias Ghosts
}
`)
	res, err := Nagios(data, nil, Options{Now: testNow, Resources: map[string]string{"USER1": "/usr/lib/nagios/plugins"}})
	if err != nil {
		t.Fatal(err)
	}
	// Two made-up hosts (db1 from the group and the service, db2 from
	// the service) and the two services that attach to hosts.
	if res.Monitors != 4 {
		t.Fatalf("monitors %d:\n%s", res.Monitors, res.Report(ReportOptions{}))
	}
	var synth, nowhere *Item
	for i := range res.Items {
		it := &res.Items[i]
		if it.Kind == "monitor" && it.Source == "db1" {
			synth = it
		}
		if it.Source == "Nowhere" {
			nowhere = it
		}
	}
	if synth == nil || synth.Status != StatusAttention || !strings.Contains(synth.Why, "No host definition for it was in the imported files") {
		t.Fatalf("synthesized host not marked: %+v", synth)
	}
	if nowhere == nil || nowhere.Status != StatusLeftOut || !strings.Contains(nowhere.Why, "membership is declared by the host definitions") {
		t.Fatalf("group-only service reason: %+v", nowhere)
	}
	// A defined host wins: nothing is synthesized for it, and it gets
	// no made-up note.
	data2 := []byte(`
define host {
	host_name real1
	address 192.0.2.1
}
define service {
	host_name real1
	service_description Ping
	check_command check_ping
}
define command {
	command_name check_ping
	command_line $USER1$/check_ping -H $HOSTADDRESS$
}
`)
	res2, err := Nagios(data2, nil, Options{Now: testNow, Resources: map[string]string{"USER1": "/usr/lib/nagios/plugins"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range res2.Items {
		if strings.Contains(it.Why, "made up") {
			t.Fatalf("defined host marked synthesized: %+v", it)
		}
	}
	// A file with neither hosts nor services is still refused as
	// before.
	if _, err := Nagios([]byte("define command {\n\tcommand_name x\n\tcommand_line /bin/true\n}\n"), nil, Options{}); err == nil {
		t.Fatal("a set with no hosts and no services was accepted")
	}
}

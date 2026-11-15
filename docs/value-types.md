# Value types

What a check reads has a type, and the type decides how it may be judged,
so a check never compares a text by size or a number by containing it.
Every check option that judges a value is listed under one of these types;
a new option picks its type from here, and its validation, its form control
and its help text follow from it.

| Type | May be judged by | Options (today) | Form control |
|---|---|---|---|
| **Text** | equal, contains, regular expression | `keyword`, `absent`, `keyword-regex`, `expect` (SNMP text, DNS TXT/MX/SRV), `json-expect`, `expect-regex`, `service` | text box |
| **Quantity** (temperature, percentage, rate, size) | above, below (or equal to) a limit, change against a reference | `warn`, `crit`, `change`, `min`, `expect-num`, `json-expect-num` | number with a fixed unit |
| **Counter** (Counter32, Counter64, interface octets) | turned into a rate first, then as a quantity | the same as a quantity | as a quantity |
| **Status code** (an enumeration: interface state, HTTP status) | is, is not, one of | `expect` (HTTP), `rcode`, `role`, `on-filtered` | a list |
| **Duration** | above, below | `for`, `compare`, `rtt`, `max-offset`, `degraded`, `timeout` | time with a unit |
| **Switch** | yes, no | `insecure`, `no-follow`, `tls`, `authorized`, `aa` | a switch |
| **Address or name** | the same, after normalising (case, the final dot, how an IPv6 address is spelled) | `expect` (DNS A, AAAA, CNAME, NS, PTR), `expect-all` | text box |

## Rules that follow

- A number is not text. SNMP `expect=` on a number must be that number
  (`10` does not match `100`); a comparison is `expect-num=`.
- An address or name is the same or it is another one: `10.0.0.1` is not
  `10.0.0.10`. A record with parts (MX, TXT, SRV) is matched by containing.
- A limit's unit must measure the value: `500Mbps` is not a limit on a
  percentage, `80%` is not one on a count of connections, `1GB` is one on
  a size in bytes and `300s` on a time in seconds. A value nobody has
  described (a number at a JSON path, an SNMP OID, a SQL query) takes a
  limit in any unit; a field of an application profile is described.
- A limit must be reachable: nothing is below zero except a temperature,
  and a percentage does not exceed 100.
- On one side, `crit` must lie further out than `warn`; at the same number
  `warn` could never show. On opposite sides (`warn=<10 crit=>90`) some
  value between them must be fine. `<=` and `>=` include the limit itself.
- `for` needs a rule to delay, `direction`, `compare` and `min` need
  `change`, and a fall of more than 100% cannot happen.
- One value is a text or a number, not both: `expect=up` with `warn=5`,
  or `expect=5` with `expect-num=>5`, is refused (the same for
  `json-expect`).
- A counter only grows, so its total is never compared with a limit: the
  rate is. A counter that goes backwards (wrapped, or the device
  restarted) costs one sample and never an alert.
- A time of uptime is a number of seconds, as it is shown.
- A status code has no size: limits on an SNMP port judge its traffic and
  need `value=in`, `out` or `util`; the port's state is not compared.
- A total that has not been counted yet is 0: an application lists a
  counter from its first event on, so its absence on the right page is
  not a failure.

# monitors.conf — syntax version 1

This is the reference for the file format. The [manual](../manual.md)
explains what the records do; this page says exactly how a file is read
and how Unagikuma writes it back. Version 1 is stable: every file that
release 1.x accepts will be read the same way by later 1.x releases, and
additions only use names that version 1 reserves or rejects today.

Every `conf` example on this page is a complete file and is checked by the
test suite (`go test ./internal/config/ -run TestSpecExamples`).

## Lexical structure

A file is UTF-8 text, read line by line. `\r\n` line ends are accepted and
kept when the file is rewritten.

- **Whitespace.** Spaces and tabs separate tokens. How many is free; the
  formatter lines columns up, the parser does not care.
- **Comments.** A `;` that starts a token begins a comment that runs to the
  end of the line. A `;` inside a token is an ordinary character, so
  `anchors=lan:a;wan:b` is one value.
- **Quoted strings.** `"..."` may hold spaces, `;`, `(`, `)` and `=`.
  Inside quotes `\"` is a quote and `\\` a backslash; any other backslash
  is kept as is. A quoted section can sit inside a token: `title="Main
  site"` is the option `title` with the value `Main site`.
- **Parentheses.** `(` and `)` are tokens of their own. Everything between
  a `(` and its matching `)` belongs to one statement, across lines; this
  is how long records (and the older SOA header) are spread over several lines.
- **Statements.** Outside parentheses each non-empty line is one
  statement. A statement whose first token starts with `$` is a
  directive; every other statement is a record.

```ebnf
file        = { line } ;
line        = [ statement ] [ comment ] newline ;
statement   = directive | record ;
comment     = ";" { any character } ;

directive   = "$" NAME { token } ;
record      = name [ interval ] "IN" TYPE [ target ] { option } ;

name        = "@" | letter-or-digit { letter | digit | "_" | "." | "-" } ;   (* at most 63 characters *)
interval    = digit { letter | digit | "." } ;                                (* 30s, 1m, 1h30m, 500ms *)
TYPE        = letter { letter } ;                                             (* case-insensitive *)
target      = token ;                                                         (* any token that is not an option *)
option      = key "=" value ;
key         = letter { letter | digit | "." | "_" | "-" } ;                   (* case-insensitive, at most 64 characters *)
value       = { character | quoted } ;
token       = { character | quoted } | "(" | ")" ;
quoted      = '"' { character | '\"' | '\\' } '"' ;
```

### Targets and options

The first token after the type is the **target** unless it is an option.
A token is an option only when it is not entirely quoted and the text
before its first `=` is a valid key. So:

| Token                          | Read as                              |
|--------------------------------|--------------------------------------|
| `https://h/api?token=abc`      | target (`https://h/api?token` is not a key) |
| `"https://h/api?token=abc"`    | target (quoted)                      |
| `url=https://h/api?a=b`        | option `url`, value `https://h/api?a=b` |
| `"title=x"`                    | target (quoted)                      |
| `Title=x`                      | option `title` (keys are lower-cased) |

Every token after the target must be an option. An option may appear only
once per record. Values are taken literally except for references:
`${secret:NAME}` is replaced by a stored secret and `${env:NAME}` by an
environment variable the server was allowed to read.

## The file header

The file header is the first statement of the file; only comments and
`$VERSION` may come before it. `$QUORUM` and `$DIALECT` follow it, before
the first record.

```
$FILE revision=<n> [master=<name>] [admin=<email>] [interval=<d>] [retry=<d>] [timeout=<d>] [attempts=<n>]
```

- **revision** — required, 1 to 4294967295. A changed file is applied only
  when its revision is higher than the running one; `YYYYMMDDnn` is the
  usual form. Saving from the web UI raises it for you.
- **master** — the name of the master node (default `unagikuma`).
- **admin** — the administrator's e-mail address (optional).
- **interval**, **retry**, **timeout** — durations (`500ms`, `15s`, `1m`,
  `1h30m`, `1d`): the default check interval (`1m`), the interval while a
  problem is being confirmed (`15s`), and the default timeout of one
  check (`5s`).
- **attempts** — 1 to 100: how many failed checks in a row make a problem
  HARD and notified (`3`).

```conf
$VERSION 1
$FILE revision=2026102901 master=unagikuma admin=admin@example.com interval=1m retry=15s timeout=5s attempts=3

www IN HTTP https://www.example.com/
```

### The SOA record (older form)

Files written before the `$FILE` header start with an SOA record that holds
the same values by position. It is read exactly as before, and a file has
one header or the other, not both.

```
@ IN SOA <master-name> <admin-mailbox> ( <revision> <interval> <retry> <timeout> <attempts> )
```

The admin mailbox may be written in dotted form (`admin.example.com.` is
`admin@example.com`) or as a plain address.

```conf
; revision  interval  retry  timeout  attempts
@ IN SOA unagikuma. admin.example.com. (
        2026102901 ; revision
        1m         ; interval
        15s        ; retry
        5s         ; timeout
        3 )        ; attempts

www IN HTTP https://www.example.com/
```

## Directives

| Directive | Arguments | Where |
|-----------|-----------|-------|
| `$VERSION 1` | the syntax version; a file without it is version 1 | once, usually the first line |
| `$FILE …` | the file header, see above | once, first |
| `$QUORUM v` | `majority`, `all`, `N` or `N%`: how many nodes must see a failure | after the file header, before other records, once |
| `$DIALECT v` | `auto`, `huawei`, `cisco`, `asa`, `xr`: vendor meaning of ambiguous UDP keywords | after the file header, before other records, once |
| `$SET key=value …` | tunables; each key once per file | anywhere |
| `$DEFAULT key=value …` | options added to the monitors after it, until the next `$DEFAULT`; an option on the record wins | anywhere |
| `$SCOPE spec` | nodes (`bj,sh`) or `region:NAME` that check the monitors after it; `$SCOPE *` returns to every node (`policy=NAME`, from older files, is still read) | anywhere |
| `$TAG key[:value] color=#rrggbb [description=…]` | how a tag is shown | anywhere |

`$INCLUDE` is recognised and rejected (`unsupported`). `$TIMEZONE` and
`$HOLIDAYS` are reserved (`needs_newer`). Any other directive is an error
(`unknown_directive`).

```conf
$VERSION 1
$FILE revision=2026102901
$QUORUM majority

$SET notify.repeat=30m
$TAG site:bj color=#2f80ed description="Beijing office"

bj       IN NODE "Beijing office" region=north
sh       IN NODE "Shanghai office" region=north
ops      IN CHANNEL feishu url=${secret:ops_webhook} system=yes
admin    IN TEAM channels=ops

$DEFAULT tags=site:bj
gw       IN PING 10.1.0.1
$SCOPE region:north
core-sw  IN PING 10.1.0.2 title="Core switch"
$SCOPE *
$DEFAULT
www  30s IN HTTP https://www.example.com/health?full=1 keyword=ok
```

## Records

Names are unique across all record types: a channel and a monitor cannot
share one. Only monitors have an interval; on other records it is ignored
with a warning (`interval_ignored`). Types are written in
upper case by convention and read in any case.

| Type | Kind | Target |
|------|------|--------|
| `HTTP`, `TCP`, `PING`, `UDP`, `DNS`, `NTP`, `SNMP`, `SYSLOG`, `PUSH`, `EXEC`, `REDIS`, `POSTGRES`, `MYSQL`, `GRPC`, `DOCKER`, `DOMAIN` | monitor | what is checked (URL, `host:port`, address, script, domain name); `PUSH` has none |
| `NODE` | probe node | display title |
| `CHANNEL` | where notifications go | the channel kind (`feishu`, `email`, `group`, …) |
| `POLICY` | which channels, how often (files from before 2.6; see below) | none |
| `PERIOD` | weekly time ranges | the ranges, quoted |
| `MAINT` | maintenance window | title |
| `STATUSPAGE` | public status page | title |
| `TEAM` | people who own monitors (`team=` on a monitor), and their channels | none |

Options common to all monitors and never sent to the check itself:
`title`, `description`, `tags`, `group`, `device`, `handler`, `was`. The
options of each type are listed in the manual and by `unagikuma check`,
which names an unknown option and the line it is on.

`SCHEDULE`, `ROTA`, `ROUTE`, `TEMPLATE`, `CHANGE` and `PERSON` are
reserved record types: a file using them is rejected with `needs_newer`, so
it fails clearly on a release that does not know them yet.

### Teams and regions

A `TEAM` record lists the members of a team by user name: `members=`
(comma-separated); `title=`/`description=` describe it. A user can be in
several teams. A monitor belongs to a team with `team=NAME` (also through
`$DEFAULT`); monitors without one belong to the built-in `admin` team,
which needs no record. `team=` naming a team that is not defined is an
error (`unknown_team`). The older `managers=`, `operators=` and `viewers=`
are read as members.

`channels=` lists the team's channels in order: the first that succeeds
is enough, or all are sent to with `mode=all`. Every monitor of the team
uses them, except a monitor with `mute=yes` (nothing is sent) or
`system-channel=yes` (sent to the system channels). Channels with
`system=yes` are the system channels: they get the system's own notices,
the problems of a team without channels, and those a team's channels
failed to deliver; the team's administrators are told in those cases.
A channel with `report=no` does not take the daily status report.

A `POLICY` record (files from before 2.6) still works for whatever names
it with `policy=`. When the server starts, policies that only list
channels are turned into team channels, keeping a copy of the file as
`monitors.conf.before-teams`.

`region=NAME` on a monitor has it checked by the nodes whose `NODE`
record has that `region=`; `region=global` is every node, also inside a
`$SCOPE`. `region=` and `nodes=` exclude each other.

`tenant=` is reserved for a later edition and ignored.

```conf
$FILE revision=2026110401
bj    IN NODE region=north
web-im IN CHANNEL feishu url=${secret:web_webhook}
web   IN TEAM title="Web team" members=li,wang channels=web-im

shop  IN HTTP https://shop.example.com/ team=web region=north
gw    IN PING 10.0.0.1 mute=yes
```

### Renaming

The name is how the file refers to a monitor, not what keeps its history.
Renaming in the web UI or API carries the history over. When renaming by
hand, write the old name once with `was=`; it can be removed after the
file has been applied.

```conf
$FILE revision=2026102902

pg-primary IN TCP 10.0.0.5:5432 was=pg title="Postgres"
```

## How the file is written back

Saving from the web UI or API changes the text, not just the meaning:

- Only the lines of the records that changed are rewritten. Comments,
  blank lines, directives and the order of records are kept.
- A changed record keeps its option order, its quoting and its column
  alignment. A changed value is replaced in place; a removed option
  disappears with the space before it; a new option is added at the end
  of the record.
- A new record goes after the last record of the same `group=`, otherwise
  after the last record of its kind in its `$SCOPE` section; a first
  monitor goes before the maintenance windows and status pages. It is
  lined up with the record above it.
- The revision is raised: to today's `YYYYMMDD01` (UTC) or, if that is not
  higher, by one.
- Values are quoted only when they must be: when they are empty or hold a
  space, a tab, `;`, `(`, `)`, `"`, `\` or `=`.

`unagikuma fmt monitors.conf` rewrites a file in the canonical layout —
single-line records lined up in columns per block, option names in lower
case, quotes only where needed — and never changes its meaning;
`unagikuma fmt --check` exits with status 1 if a file is not formatted.
Records spread over several lines and everything that is not a record are
left as written.

## Diagnostics

`unagikuma check monitors.conf` prints `file:line:col: severity: message
[code]`; `--json` prints a list of `{file, line, col, severity, code,
message}`. The codes are part of version 1 and keep their meaning:

| Code | Meaning |
|------|---------|
| `syntax` | the statement does not have the shape above |
| `unterminated_string`, `unbalanced_paren`, `invalid_utf8` | lexical errors |
| `soa_first`, `missing_soa`, `duplicate_soa` | placement of the file header (`$FILE` or SOA) |
| `invalid_serial`, `invalid_duration`, `invalid_value` | a value that cannot be read |
| `invalid_option` | an option a monitor's type does not accept, or a value it cannot use (the message names which) |
| `unknown_secret`, `env_not_allowed` | a `${secret:…}` that does not exist, or an `${env:…}` that is not allowed |
| `invalid_name`, `duplicate_name`, `duplicate_option` | names and options |
| `unknown_policy`, `unknown_team`, `unknown_parent`, `unknown_node` | a reference to something not defined |
| `unknown_type`, `unknown_directive`, `unknown_option` | names this release does not know |
| `needs_newer`, `unsupported_version` | reserved for, or written for, a later release |
| `duplicate_directive`, `directive_order`, `duplicate_tunable`, `invalid_tunable` | directives |

A file with any error is never applied; the last good version keeps
running. Warnings (for example a timeout not shorter than the interval) do
not stop a file from being applied. Among them: `conflicting_auth` (an
HTTP monitor sets both `basic=` and `bearer=`; `bearer=` is sent) and
`rotation_removed` (a group still has the options of on-call rotations,
which were removed; they are ignored and every member is notified) and
`standby_scheme_added` (a node's `standby=` is a bare `host:port`; it is
read as `https://host:port`).

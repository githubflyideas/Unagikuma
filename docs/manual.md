# Unagikuma user manual

How to install, configure and run Unagikuma. The [README](../README.md) has the short version.
**Contents**

- [Quick start](#quick-start)
- [monitors.conf](#monitorsconf)
- [Alerting](#alerting)
- [Status pages](#status-pages)
- [Two machines and clusters](#two-machines-and-clusters)
- [Users](#users)
- [Registration and quotas](#registration-and-quotas)
- [Moving from Uptime Kuma or Nagios](#moving-from-uptime-kuma-or-nagios)
- [Forgot your password?](#forgot-your-password)
- [Command line](#command-line)
- [Data and backups](#data-and-backups)
- [API](#api)
- [Troubleshooting](#troubleshooting)

## Quick start

Download the archive for your platform from the [releases](../../releases), unpack it and run:

```sh
./unagikuma            # listens on :8077, keeps its data in ./data
```

Open `http://<host>:8077/` and create the admin account. When you open the UI from another machine, Unagikuma asks for the one-time setup token it printed to its log at first start.

On macOS the binaries are not notarized yet, so the first start is blocked: run `xattr -d com.apple.quarantine ./unagikuma` once (or right-click it in Finder and choose *Open*).

Add a monitor in the UI, use **Quick add monitors** to paste a list of targets, or edit `data/monitors.conf`.

### As a service (Linux, systemd)

```ini
# /etc/systemd/system/unagikuma.service
[Unit]
Description=unagikuma monitoring
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/unagikuma server --data /var/lib/unagikuma
Restart=on-failure
DynamicUser=yes
StateDirectory=unagikuma
AmbientCapabilities=CAP_NET_RAW

[Install]
WantedBy=multi-user.target
```

`CAP_NET_RAW` is only needed for ICMP ping on systems where unprivileged ping sockets are disabled (`net.ipv4.ping_group_range`). Without either (as on a stock Debian or Ubuntu), a ping check knocks on TCP ports 443, 80 and 22 instead: a host that accepts or refuses the connection is up, and the result says it was checked over TCP. If all three ports stay silent, the check reports that this host lacks the privilege rather than marking the target down. `fallback=off` turns the TCP knock off.

### As a service (Windows)

In an Administrator PowerShell, in the folder of `unagikuma.exe`:

```powershell
.\unagikuma.exe service install -- server --data C:\ProgramData\unagikuma --log-file C:\ProgramData\unagikuma\unagikuma.log
.\unagikuma.exe service uninstall
```

The service starts automatically and is restarted if it stops.

Windows is supported, with some checks possibly unsupported:

- ICMP ping needs the service to run as Administrator (or LocalSystem); otherwise ping checks knock on TCP ports instead.
- A weak UDP check (L1) may report that this platform cannot see a closed port, and stays UNKNOWN instead of OK or down.
- A fixed source port (`src-port=`) held by another program may not be detected.

## monitors.conf

Releases before 2.6 called this file `monitors.bind`. The server renames it when it starts, and the command-line tools still find the old name.

```text
; revision, then the defaults for every monitor
$FILE revision=2026102301 interval=1m retry=15s timeout=5s attempts=3

$SET notify.repeat=30m

; notification channels, and the admin team that uses them
ops      IN CHANNEL feishu url=${secret:ops_webhook}
mail     IN CHANNEL email host=smtp.example.com port=587 tls=starttls username=alerts@example.com password=${secret:smtp} from=alerts@example.com to=oncall@example.com system=yes
admin    IN TEAM channels=ops,mail

; monitors: name [interval] IN TYPE target key=value...
www      IN HTTP https://www.example.com/ title="Website" keyword="Welcome" cert-days=62 group=public
api  30s IN HTTP https://api.example.com/health json-path=$.status json-expect=ok
db       IN TCP  10.0.0.5:5432 title="Postgres"
gw       IN PING 192.168.1.1
time     IN UDP  10.0.0.1:ntp authorized=yes
switch   IN UDP  10.0.0.2:snmp community=${secret:snmp_ro}
backup   IN PUSH period=24h grace=2h
company  IN DOMAIN example.com days=45 critical-days=10

; a public status page at /status/main
main     IN STATUSPAGE "Service status" select="group:public"
```

- After editing the file by hand, **increase the revision** on the `$FILE` line; Unagikuma then validates and applies it. Saving a monitor, channel or team in the web UI raises it for you. Files from earlier releases start with an `@ IN SOA … ( serial interval retry timeout attempts )` line instead; it still works the same way, and its first number is the revision. **Configuration › monitors.conf** shows the file read-only, with a copy button; edit it on the server. A file with errors is never applied: the last good version keeps running and the UI shows what is wrong.
- Credentials never need to be in the file: store them under **Settings › Secrets** and refer to them as `${secret:name}`. A secret the configuration still uses cannot be deleted: the refusal names the records that use it. Values from the environment can be used as `${env:NAME}` when the server is started with `--allow-env NAME`.
- Monitors are grouped into hosts by the address they check; `device=core-sw1` groups monitors of several addresses under one name. A host with a PING monitor is UP or DOWN by that ping.
- `parent=gw` (or `parent=sw1,gw`) declares a dependency: while a parent is down, a failing child is shown as UNREACHABLE and not notified. If the child is still down once its parents are back, it is notified as usual.
- Maintenance windows hold notifications for the monitors they select; that time does not count against uptime, and a monitor still down when the window closes is notified then:

  ```text
  sunday  IN MAINT "Server room work" select="tag:site:bj" every=sun start=02:00 duration=2h notify=yes
  upgrade IN MAINT "Core switch upgrade" select="cidr:10.0.0.0/24" at="2026-11-01 22:00" duration=3h
  backups IN MAINT "Nightly backup" select="group:db" cron="30 2 * * 1-5" duration=40m
  ```

  `select=` takes fields that only depend on the configuration: `name`, `title`, `type`, `svc`, `port`, `host`, `cidr`, `tag`, `group`, `policy`, `node`, `area`, plus free text; terms are AND-ed, commas mean "any of", `-` negates, `/…/` is a regular expression.
- HTTPS checks and TCP checks with `tls=yes` watch their certificate: inside `probe.cert-warn-days` (62) they are degraded, inside `probe.cert-critical-days` (7) they fail, and an expired certificate always fails — also with `insecure=yes`. `cert-days=` and `cert-critical-days=` on a monitor set its own thresholds (0 turns one off).
- A PING check sends 3 echo requests and fails when more than `probe.ping-loss` (50%) of them go unanswered; less loss marks it degraded. `loss=` on a monitor sets its own threshold (`loss=0` fails only when every request is lost).
- A UDP check that only shows the port is not closed (L1, marked *weak*: `SYSLOG`, `snmptrap`, unknown ports) counts silence as "probably open" only while the host itself answers a ping (or a TCP knock). If the host does not answer either, the check is UNKNOWN instead of OK; if it is reported unreachable, the check fails. `host-check=off` accepts silence alone, for hosts that answer nothing at all.
- Pings and weak UDP checks of the same address start at least `probe.same-target-gap` (1s) apart on each node, because devices limit how many ICMP replies they send and a burst can look like packet loss or an open port. Checks that share a fixed source port (`src-port=`) take turns; if the port stays busy, or another program holds it, the check is UNKNOWN for that round instead of down.
- Two monitors that check exactly the same thing (same type, target, options, timeout and nodes — typically one service watched by two teams) send every request twice. `unagikuma check` and the monitor form point this out; `share=yes` on one of them makes it use the result of the other's check from the last `probe.share-window` (10s) instead of checking again. Each monitor keeps its own thresholds, incidents and notifications.
- `DOMAIN` watches when a domain's registration expires, asking the registry over RDAP (or WHOIS where there is no RDAP), every 6 hours by default and at most hourly. Inside `days=` (30) it is degraded, inside `critical-days=` (7) it fails; if no registry answers it is UNKNOWN, never down. **Monitors › Expiry dates** lists every domain and TLS certificate, soonest first.
- `unagikuma keywords` lists the UDP keywords and what each check proves.
- Every day at 10:00 (`notify.daily-report-at`) a status report lists the hosts that are DOWN and UP and every service that is not OK; **Notices & status › Daily report** previews it and sends it on demand. Each team chosen there (the admin team by default, `notify.daily-report-teams`) gets a report on its own monitors through its channels; the admin team's covers every monitor. A person can be switched off in **Contacts**, a channel with `report=no`.

### Script checks

> Experimental.

`EXEC` runs a check script using the Nagios plugin conventions: exit status 0 is OK, 1 WARNING (shown as DEGRADED), 2 CRITICAL, 3 UNKNOWN; the first output line is the status text and performance data after `|` is kept as metrics. Existing plugins and in-house scripts work unchanged:

```text
disk-db1  IN EXEC check_disk args="-w 20% -c 10% -p /data" device=db1
load-db1  IN EXEC check_load args="-w 5,4,3 -c 10,8,6" device=db1
nrpe-web  IN EXEC "check_nrpe -H $HOSTADDRESS$ -c check_users" address=10.0.0.21
```

- Scripts are looked up only in the check-scripts directory of the machine that runs the check: `<data>/check-scripts`, or `--script-dir dir1,dir2`. The directory is created on first start with a README and an example (`check_example`). The configuration names a file there, never a path, and nothing goes through a shell.
- In a cluster each machine runs its own copy, so copy scripts to every machine that runs the check (scp, Ansible…); Unagikuma does not upload them. New script checks start **paused**: enabling one checks that every node running it has the script, and refuses with the list of nodes that lack it. **Settings › Check scripts** shows each check's script on each node (present, missing, not executable, another version) and the sidebar shows a red count while an enabled script check cannot run somewhere.
- Plugins get a minimal environment (`PATH`, `UNAGIKUMA_MONITOR`, `UNAGIKUMA_ADDRESS`, `UNAGIKUMA_TIMEOUT`), not the server's. `$HOSTADDRESS$`, `$SERVICEDESC$` and `$TIMEOUT$` are replaced in arguments. At the timeout the plugin and everything it started are killed and the check is CRITICAL.
- Only administrators can add EXEC checks or change what they run. `probe.exec-concurrency` limits plugins running at once per node.

## Alerting

Notifications go through **channels** (where a message is sent). Each team has an ordered list of them (see [Teams](#teams)); channels are records in `monitors.conf` with forms under **Alerting**, and a team's list is set under **Settings › Teams**.

```text
ops-im    IN CHANNEL feishu url=${secret:feishu_ops}
oncall    IN CHANNEL aliyun-sms access-key-id=LTAI… access-key-secret=${secret:ali} sign-name=Ops template-code=SMS_123 phones=13800000000
manager   IN CHANNEL email host=smtp.example.com from=alerts@example.com to=boss@example.com
office    IN PERIOD "mon-fri 09:00-18:00; sat 10:00-12:00"
team      IN CHANNEL group members=ops-im,oncall
night-sms IN CHANNEL aliyun-sms … period=!office
admin     IN TEAM channels=team,night-sms mode=all
```

- **E-mail.** An `email` channel sends either to an SMTP server (`host=`, `port=`, `tls=starttls|tls|none`, `username=`, `password=`, `from=`) or, with `via=sendmail`, through this machine's own mail system, the way Nagios and cron do:

  ```text
  ops-mail  IN CHANNEL email via=sendmail to=ops@example.com,dba@example.com
  ```

  Nothing else is needed: whatever mail system the machine runs (postfix, sendmail, exim, OpenSMTPD, msmtp, ssmtp, nullmailer, dma) installs a `sendmail` command, and that is used, with its own relay, queue and retries. It is looked for in `/usr/sbin`, `/usr/lib`, `/usr/bin`, `/usr/local/sbin` and `/usr/local/bin`; `program=` names another place (only a mail system's own command, such as `/usr/bin/msmtp`, is accepted). Without `from=` the mail system chooses the sender, as it does for Nagios. The message counts as sent once the mail system has accepted it; whether it then reaches the mailbox depends on that mail system, so try **Test** once after setting it up. Not available on Windows.
- **How often.** `notify.repeat`, `notify.max` and the other `notify.*` tunables (**Settings › Tunables**) set how often a problem is repeated and how many notices it sends.
- **Time periods.** `PERIOD` records are weekly rules (`mon-fri 09:00-18:00`, ranges past midnight such as `22:00-07:00`, `24:00` for end of day). `period=office` on a channel uses it only inside the period, `period=!office` only outside it. A skipped channel is not a failure: the next one in a fallback chain is tried.
- **Contact groups.** A `group` channel notifies all its `members=` at once; it succeeds if any member does. Members keep their own time periods.
- **Summaries say what kind of problem it is.** When many notices queue up at once (`notify.coalesce-queue`, `notify.coalesce-wait`) they are replaced by a summary, and the summary tells services from the network:
  - services that fail on their own come first, as "please act now", for their owners;
  - `notify.group-min` (3) or more monitors of one IP segment failing at the network level together are reported as a fault of that segment, its switch or uplink; monitors failing to resolve their names together, as a DNS fault — "please contact the network team".

- **When this host loses its network** it cannot notify anyone, so nothing is sent while it lasts and nothing queued meanwhile is sent afterwards. Failures that began just before the outage was recognised (`localnet.backdate`) and that nobody was told about are taken back. When the network is back every monitor is re-checked, and one message says how long the host was blind and what is still failing; outages shorter than `localnet.notify-after` (5 minutes) are only recorded. A recovery is sent only for problems someone actually received. To be told *while* the host is offline, use an external heartbeat (`--heartbeat-url`) or a second node.

  How the host recognises its own outage: its gateway stops answering; or internet targets in at least `localnet.min-domains` (3) different networks (an IPv4 /24, an IPv6 /48 or a registered domain) fail together while none of them answers; or every WAN anchor fails (**Run mode › Edit** on this machine, **Anchors** `wan:IP,IP`). Before an internet failure becomes an alert, the host also asks a few **reference points** on the internet (`localnet.references`: 223.5.5.5, 1.1.1.1 and 8.8.8.8, on TCP 443). If any of them answers, its own network works and the target is alerted as usual, even when all your targets fail at once; if none answers, they count towards the three networks, so a machine watching only one or two sites still recognises its own outage and sends the summary afterwards. Where your network blocks these addresses, set ones it can reach under **Settings › Tunables**.
- **SMS and voice calls.** `aliyun-sms`, `aliyun-voice`, `tencent-sms`, `tencent-voice`, `twilio-sms` and `twilio-voice` channels. Template parameters default to the monitor name and the status; `params="{{.Monitor}}|{{.StatusText}}"` changes them. Keys are stored as secrets when saved from the UI.
- **Event handlers.** `handler="restart-web $SERVICESTATE$ $SERVICESTATETYPE$"` on a monitor runs a script from the check-scripts directory on each state change, Nagios style, with `UNAGIKUMA_MONITOR`, `UNAGIKUMA_STATE`, `UNAGIKUMA_STATE_TYPE`, `UNAGIKUMA_ATTEMPT`, `UNAGIKUMA_ADDRESS` and `UNAGIKUMA_REASON` in its environment. It runs on the master, one at a time per monitor, for at most `engine.handler-timeout`; the result is in the event log. Only administrators may set it.
- **Contacts.** **Alerting › Contacts** lists everyone who may receive alerts, with their e-mail addresses, phone numbers and status: **active**, **on leave**, **transferred** or **left**. E-mail, SMS and voice channels send only to active people in it (chat and webhook channels are not checked):
  - an address of someone on leave, transferred or gone is not sent to; leaving also stops their linked login and API tokens;
  - an address refused as nonexistent (unknown mailbox, invalid number) `contacts.bounce-threshold` times (3) is no longer sent to;
  - an address that is not in the directory is not sent to.

  One left-out recipient never stops a message to the others. Each one is logged when the channel sends, and the channel, the teams using it and the **Alerting** menu get a red dot saying why. With **Tell the team's administrators** (`contacts.notify-admins`) on, they also get a message, once a day per channel.

  On the first start of 2.6, the addresses your channels already use are added to the directory as active, so nothing stops after the upgrade.

  Write `to=@li` or `phones=@li,@oncall` to use a person's addresses instead of typing them. Saving a channel with new addresses offers to add them to the directory, and so does an import that brings new ones; **Import them** adds every address your channels already use. `unagikuma check` reports the same problems when it can read the data directory.

  Every contact can be reached: they need at least one valid e-mail address or phone number, and **Notify via** names the e-mail, SMS or voice channels through which the system reaches them directly (as a team administrator, for example), each matching an address they have. Contacts added from a channel are reached through it. A contact with no way chosen, or whose channel is gone or lacks their kind of address, has a red dot in the list.
- **Every alert says what kind of failure it is.** A problem notice carries one line: a service failing on its own ("the service itself"), several monitors of one IP segment failing together ("probably that segment, its switch or uplink"), a single network-level failure that cannot be told apart yet ("uncertain"), or this host's own network. Webhooks get the same as `"class"`: `business`, `network`, `uncertain`, `channel_down` or `system`.
- **No more than three alerts a minute.** A channel sends at most `notify.backstop-max` (3) alerts in `notify.backstop-window` (1 minute); further repeats and reminders are held and sent as one summary at the end of the minute, worst first. Recoveries, escalations and system notices always go out; new problems have their own share of three a minute, so repeats never hold one back but a hundred services failing together is one digest, not a hundred pages. `notify.backstop-max=0` turns it off. Held alerts are kept in memory: after a restart they are not sent, but the incident report shows them.
- **Why was I (not) notified?** The monitor drawer's **Notifications** section says it in words: maintenance, a parent is down, this host's own outage, only some nodes failing, still SOFT, acknowledged, silenced, muted, or the notification limit.

### Incident reports

Every decision about an alert is recorded: queued, sent or failed (and on which channel), held during a local outage and released, merged into a summary, dropped, suppressed by maintenance, acknowledged. From that record each incident gets a report, a postmortem you can hand on:

- **What led up to it** — configuration changes and system events of the day before;
- **Detection** — the first failing check, how it was confirmed, and the evidence;
- **Notification** — who was told, when, on which channel, what was held or merged and why, how long until someone was told and until it was acknowledged;
- **Recovery** — when it was fine again and how long it lasted;
- whether this host was watching all along (gaps in the checks, restarts, local outages), so a quiet stretch is not mistaken for an all-clear.

Open it from the incident's **Report** link on the Problems page and download it as Markdown, HTML or JSON, or write it from the command line:

```text
unagikuma report incident 42 --format md|html|json [--lang zh] [-o report.md]
```

`GET /api/v1/incidents/ID/report?format=md|json|html` serves the same to anyone who may read it. The report uses only what was recorded; history pruned after its retention is gone from it too.

### Announced changes

**Notices & status › Changes** announces planned work — a switch upgrade, a database migration — to the people it concerns:

- The announcement goes out at once through a policy (or straight to chosen channels), with reminders before the start (`changes.reminders`, `24h,1h`; only those that come after the announcement), a notice when it starts and one if it runs past its end without being closed.
- The monitors matched by its filter (`group:core`, `tag:site:bj`, …) are in maintenance during the window: failures are not notified and not counted against uptime. No `MAINT` record is needed.
- It can put a maintenance notice on a status page, which is closed with the outcome.
- It ends with a receipt: **Done** or **Rolled back** with a note, or **Cancelled** before it starts. Moving the window tells everyone the new time and starts the reminders again.
- The history can be searched and exported as CSV. The notices are templates you can change; the built-in ones are in English and Chinese.

### How things are counted

- **Unhandled problems** are open incidents of any kind — including regional and low-priority ones — that nobody has acknowledged or silenced. The sidebar, the overview, the problems page and `/metrics` (`unagikuma_unhandled_problems`) all use this one definition. A monitor that cannot be judged (UNKNOWN) has no incident; it is counted under its status.
- **Uptime** is up time ÷ (up + down time). DEGRADED (slow) counts as up, and its share is reported separately. Maintenance windows and gaps without checks count as neither. Time spent confirming a failure (SOFT) is held back: it counts as down if the failure is confirmed and as up if the check recovers first, so a monitor that fails from the start shows 0%.
- **Coverage.** A window's uptime is shown only once the monitor was watched for at least `uptime.min-coverage` (10%) of it; until then the UI shows “—” and how much was watched, so a monitor added an hour ago does not claim a 30-day figure.
- **Changing a check** that is failing — a timeout, an expected keyword, even the target — keeps its open incident and checks again right away. If it still fails, nobody is paged a second time; if it passes, the usual recovery is sent. A check with no open problem starts over as PENDING.
- **Acknowledgements** are kept as a history: acknowledging again adds an entry with its note instead of replacing the first one, and the time of the first acknowledgement is kept.

## Status pages

```text
public IN STATUSPAGE "Example Inc. status" select="group:public" subscribe=mailer domain=status.example.com
```

- A page shows the selected monitors with their uptime over `windows=` (24h, 7d, 30d, 90d), at `/status/NAME`. Targets and reasons are never shown.
- **Announcements** are written under **Notices & status › Status pages**: a title, a message and a severity (notice, minor, major, maintenance); open ones are shown on top, resolved ones under *Past notices* for `statuspage.resolved-shown` (7 days). Maintenance windows of the page's monitors within the next week are listed too.
- **Subscriptions.** With `subscribe=CHANNEL` (an e-mail channel) visitors can subscribe by e-mail. They confirm with a link first, and every message has an unsubscribe link. When you publish or update an announcement, tick *E-mail subscribers*. Requests are limited per address (`statuspage.subscribe-rate`) and unconfirmed subscriptions are capped (`statuspage.max-pending`). Every page also has an Atom feed at `/status/NAME/feed.atom`.
- **Own host name.** With `domain=status.example.com`, requests for that name get the page at `/` (and `/feed.atom`); the console and its API are not reachable under it. Point the name at the server and put a TLS proxy in front, or give the server a certificate for it.
- Badges: `/badge/MONITOR/status.svg`, `uptime.svg` and `latency.svg` for monitors on a public page.

## Two machines and clusters

A single machine is a complete setup. Add a second machine, or more, when monitoring has to keep working while one machine or one site's network is down, or to see your targets from several places.

**Every machine is a complete server that checks and alerts on its own.** The machines of a cluster share one configuration and show each other what they see, but they never vote on whether a target is down and never hold back each other's alerts. When two machines see a problem, you get two messages. This is deliberate: a problem is never missed because another machine was expected to report it.

### Joining a machine

1. Install Unagikuma on the second machine and run it as usual, with its own data directory and admin account.
2. On the first machine, the **master**, open **Run mode › Join code** and generate a code. It works once, for `cluster.join-code-ttl` (1 hour).
3. On the second machine, open **Run mode › Join cluster** and paste the code. Its own monitors are deleted, or, if you choose, imported to the master paused, in a section named after it, to review and resume there.

The joining machine connects out to the master's port (8077 by default) over TLS, so it can sit behind NAT. If something has to sit in between, use a TCP proxy, not an HTTP one.

> Before 2.2 a node could also be a probe-only agent (`unagikuma agent`). Agents are gone: every member is a full server, and `unagikuma agent` only prints an error. To move an old agent over, see [upgrading a cluster to 2.2](per-site-upgrade.md).

### What every machine does

- **Checks** every monitor of the shared configuration, except monitors limited to other sites (below), with its own schedule, history and incidents.
- **Alerts** through the shared channels, on its own. Every message names the site that sent it by its node name, the master's too (`[b]` in the title, `site` in webhook payloads) and lists the latest result of every site, for example `Observations: b ×, unagikuma ×, 0/2`; a result that is out of date is marked `~`. These results are context only: they never hold back or trigger an alert.
- **Judges its own network.** Each machine recognises its own outage and holds its own alerts while it lasts, as described under [Alerting](#alerting). A machine whose network is down cannot send anything, which is what the other machines are for.
- **Keeps its own acknowledgements and silences.** Acknowledging or silencing an alert affects the incident of the site that sent it, also when done on a node. Maintenance windows in the shared configuration apply at every site.
- **Pauses and resumes on this site only.** Pausing or resuming a monitor stops or starts checks and alerts on the machine where you do it; other sites keep their own pause state. It works while the master is unreachable, and does not change the shared configuration. (`disabled=yes` in the configuration file remains a shared setting.)
- **Sends its records to the master** every minute while connected, and everything it recorded on its own as soon as the link is back, tagged with the site that wrote it. The master's history and incident list show every site; each site keeps its own audit chain.

What that looks like with two machines, the master `unagikuma` and `b`:

| What happens | Messages |
|---|---|
| The shop is down for everyone | two: one from each machine, each listing both results |
| The shop is unreachable from b's office only | one, from b |
| The master is down | b says once that its peer is unreachable, and goes on checking and alerting alone |
| The master comes back | the master says how long it was down; b says it is back online |

### When the master is down

The other machines keep checking and alerting with the last copy of the configuration they received. While the master is down the configuration cannot be changed: every other machine refuses edits with "configuration is read-only on this site". Edit again once the master is back, or move the master role to another machine (below).

### Monitors for some sites only

By default every machine checks every monitor. Targets that only exist at one site go in a section:

```text
bj  IN NODE "Beijing office" region=north
$SCOPE region:north                    ; checked by north sites only
bj-core IN PING 10.10.0.1
$SCOPE *                               ; back to every site
```

Only the sites named check these monitors. `nodes=bj,sh` (or `nodes=region:north`) on a single monitor does the same and wins over the section.

`quorum=` and `partial-escalate=` are still accepted so that older files load, but they no longer do anything: no site waits for the others before alerting.

### Site-local configuration

Under **Nodes › Site-local configuration** (`<data>/site-local.conf`, same format as `monitors.conf`, with its own `$FILE` line) each machine can have monitors and channels of its own — a router in that office, say. They are checked and alerted on there in every role, never sent to other machines, and never overwritten by the master's copy, a join or a promotion. A name the shared configuration already uses keeps the shared definition.

### Node status

**Run mode** shows every machine. Each one checks the others every `cluster.peer-interval` (10 seconds) and reports what it reaches, its own health and how its last notifications went out. A machine is reported **down** only when more than half of the cluster cannot reach it, and only by a machine that itself reaches more than half; a machine in the minority says it cannot judge. With two machines neither side can confirm the other is down, so each only reports its peer as unreachable. With three, one lost machine is confirmed by the two others. This status is about the machines only; it does not change how targets are judged.

`unagikuma node list` lists the machines and `unagikuma node revoke NAME` disconnects one within one ping interval. Machines are added with a join code only; `node add` and `node token`, which served the old agents, now say so and stop.

### Moving the master role

There is no election: which machine is the master is your decision, and moving it is one click.

1. Give the machine that should be able to take over a **standby address** under **Nodes › Edit** (`bj IN NODE standby=https://10.0.0.2:8077`). It then keeps a copy of the master's state — databases, `monitors.conf`, the secret key and the cluster CA — refreshed every `cluster.standby-sync` (5 minutes) and after each configuration change. Every machine learns the standby's address.
2. **Planned** (maintenance, new hardware): **Nodes › Make master** on the standby. It takes the current state and restarts as master; the other machines move over by themselves. Users, tokens, secrets and the CA come along, so nothing has to be joined again.
3. **Master lost**: on the standby, **Run mode › Take over as master** (or stop it and run `unagikuma promote`). While the master still answers, taking over is refused, since it would make two masters: move the role with a handover from the master instead. Only for a master that runs but is broken can the take-over be forced. A member cut off from the master by the network cannot tell that it still runs, so treat the administrator accounts of members like the master's own. The other machines move to it once the old master is unreachable.

Every handover raises a **term**. Machines follow the highest term they have seen. A replaced master that comes back is told so by the new one (with a notice signed by the cluster CA) or by the first machine that reaches it; it then rejoins as an ordinary member under its own site name and keeps checking, and only the new master accepts configuration changes. Taking over after a loss is always done by a person: without a quorum, an automatic takeover would split the cluster whenever the network splits.

## Users

**Settings › Users** manages accounts. There are three roles:

- **Administrator**: everything — all teams, users, contacts, channels, regions and nodes; creates and deletes teams.
- **Operator**: adds, changes, pauses, resumes and deletes the monitors of their own teams, acknowledges and silences their problems, and sets maintenance windows over them.
- **Viewer**: reads their own teams.

An operator can also be limited to some `$SCOPE` sections (`region:north`, `bj`): they then change only what is written in those sections.

### Teams

Every monitor belongs to one team. The built-in **admin** team always exists, cannot be renamed or deleted, and owns every monitor without `team=`. While it is the only team, everyone is in it and works as before.

**Settings › Teams** (administrators) adds teams and their members. Teams are `TEAM` records in monitors.conf, so they reach every node with the rest of the configuration:

```text
web   IN TEAM title="Web team" members=li,wang,zhang
shop  IN HTTP https://shop.example.com/ team=web
```

- With more than one team, a new user's teams are chosen when the account is created (one or more); administrators move people between teams and change roles later.
- A new monitor goes into the user's team when they have one, and they choose when they have several; an administrator who chooses nothing puts it in admin.
- A team is deleted only once its monitors are moved to another team.
- Older files with `managers=`, `operators=` and `viewers=` still load: all of them count as members, and what each person may do now follows their account role.

Teams are isolated from each other. A member who is not an administrator:

- sees only their teams' monitors, with their incidents, events, reports, hosts, expiry dates and maintenance windows; the live updates leave out the rest. A monitor of another team answers as if it did not exist; only the state of a monitor their own ones depend on (`parent=`) is shown, next to the dependant.
- has the menu Overview, Monitors, Problems and Events. Teams, channels, contacts, regions, status pages and settings stay with administrators. An incident report lists only the configuration changes that touched that monitor.
- API tokens are made by administrators and are not limited to a team.

Names are unique across the whole file, so a name another team uses cannot be taken again.

### Regions

A region is a group of nodes (`region=` on `NODE` records). A monitor is checked by the nodes of its region (`region=north`); **global**, the default, is every node. **Settings › Regions** (administrators) adds regions from nodes, changes their nodes, and deletes them: a deleted region's monitors move to global. Select monitors in the list and use **Move** to change their team or region in one change (administrators; it is in the audit log).

The monitor list filters by team, region and tag; the search accepts `team:web`, `region:north` and `tag:db` as well.

Notifications follow the teams too:

- Each team has an ordered list of channels (`channels=` on its `TEAM` record; administrators set it under **Settings › Teams**). They are tried in order until one succeeds, or all are sent to with `mode=all`. Every monitor of the team uses them.
- A monitor may have one of two exceptions: `mute=yes` (problems are recorded, nothing is sent) or `system-channel=yes` (it goes to the system channels instead).
- **System channels** are channels with `system=yes`. They receive the system's own notices, the problems of a team with no channels, and the problems a team's channels failed to deliver. In both cases the team's administrators are told as well (the administrators in the team, or every administrator when it has none).
- When a monitor of the shared infrastructure fails, the administrators of the teams it affects are told too: the teams with monitors that depend on it (`parent=`, directly or further up), or every team when none does. Each is reached at their own address through the channels chosen under **Notify via** in their contact entry, which must be linked to their login. This is sent right away, capped and logged, and not once the problem is acknowledged; they hear of the recovery too. **Settings › Teams** names administrators who cannot be reached.

Releases before 2.6 used `POLICY` records. When the server starts, a policy that only lists channels (`chain=`, `mode=`) becomes the channels of the teams that used it: the `default` policy those of the admin team and of teams without a policy, and the channels of the `system` policy (or, without one, of `default`) get `system=yes`. `policy=system` on a monitor becomes `system-channel=yes`. The file is kept as `monitors.conf.before-teams` and the change is in the audit log. Policies with other options (`repeat=`, `escalate=`, …) stay and keep working for whatever names them; edit them in the file. In a cluster, upgrade the other machines before the master, because older releases do not read the new options.

**Settings › Audit** lists every change to accounts, roles and sections (before and after), tokens, secrets (names only), nodes and the configuration, with the person or token, the section, and the source address; configuration edits made in the file on disk are listed too (each record as it was and as it is now), and so are sign-ins and failed logins, backups, restores, imports, password resets, and what the system did by itself (holds during a local outage and their release, restarts, crashes, clock jumps, upgrades). High-risk entries are highlighted and can be filtered by risk, person, source address, sign-in and time. `GET /api/v1/audit?actor=&action=&area=&session=` serves the same to administrators.

Every entry made through the web UI or the API also says which sign-in and which browser or program it came from (`detail.client`: `session` and `ua`; the CSV export has them as its last two columns). The sign-in is a short tag, the same for every change made in one sign-in and different for every sign-in, so people who share one account, even behind one office address, can still be told apart; click a tag to list that sign-in's changes. An API token's tag is its prefix. Giving each person their own account under **Settings › Users** still says more than a tag can.

Entries are kept for two years. Only the server's `--audit-retention` (or `UNAGIKUMA_AUDIT_RETENTION`), 30d to 3660d, can shorten that: `retention.audit` in the configuration, which the web UI can reach, only ever makes it longer, and the web UI shows it without letting anyone change it, so nobody can clear the log from the web.

The log cannot be changed quietly: each entry carries the hash of the one before it. The page's **Check integrity** button and `unagikuma audit verify` say whether an entry was changed, inserted or removed, and which. `verify` prints the newest entry's hash; keep it elsewhere and pass it later with `--head HASH` to prove nothing was cut off the end. `unagikuma audit export --format csv|json [--since 30d] [--high]` (or the page's export buttons) writes the log out.

Reading the audit log needs a registered installation; see [Registration and quotas](#registration-and-quotas).

## Wall screen

For a TV in the operations room: every monitor as a large hexagon, problems first and lit (critical ones pulse until someone acknowledges them), with the counts, a clock and the latest problems along the bottom. It is read-only and refreshes every 10 seconds; if no new data arrives for a minute, a red banner says so, so a frozen screen is never mistaken for a quiet one.

- Signed in, open `/wall` (or **Settings › Appearance › Open the wall screen**).
- For a TV that should not sign in, use **Create a link for a TV**. The link looks like `https://monitor.example.com/wall#token=uka_…`: it carries a read-only API token after the `#`, which browsers do not send to the server or in the Referer header. Anyone with the link can see monitor names and states, so treat it like a password; revoke it under **Settings › API tokens**.

The wall uses the active theme pack's colours (dark unless the pack is built on a light look).

## Summary for managers

`/summary` is one page for people who do not run the monitoring: whether everything is running right now, availability over 7, 30 or 90 days compared with the period before, the number of incidents, the average time to recover and the total downtime, daily availability as a chart, the services that need attention and the latest incidents. It is read-only, refreshes every minute and prints on A4.

Open it signed in (**Settings › Appearance › Summary for managers**), or create a link there for someone who does not sign in; like the wall screen's, it carries a read-only API token after `#` and can be revoked under **Settings › API tokens**. Readers see only the monitors their account or token may see. The figures come from `GET /api/v1/reports/summary?range=7d|30d|90d`.

## Theme packs

A theme pack changes how the console looks: its colours and, on a registered installation, the product name and logo (white label). Manage packs under **Settings › Appearance**: upload, use, export to share with another installation, delete. The pack in use becomes everyone's default look; each person can still choose another theme at the bottom of the sidebar.

A pack is a zip file holding:

- `theme.json` (required)
- `logo.png` or `logo.jpg` (optional): sidebar and login page, at least 16 and at most 2048 pixels wide and high
- `favicon.png` (optional): the browser tab icon

A complete example is in [`docs/theme-pack-example`](theme-pack-example/); zip its three files to try it.

```json
{
  "name": "Harbour Blue",
  "author": "Example Co.",
  "version": "1.0",
  "base": "light",
  "layout": "standard",
  "brand": { "product": "Harbour Monitor", "short": "HM", "hide_powered": false },
  "colors": { "bg": "#f2f6fa", "side": "#e3ebf3", "accent": "#0a5fc2" }
}
```

- `name` (required, up to 60 characters); `author`, `version` are shown in the list.
- `base` is `light` or `dark`: every colour the pack does not set comes from that look.
- `colors` sets any of these, written `#rrggbb`: `bg` (page), `side` (sidebar), `panel` (cards and the top bar), `panel-2`, `border`, `border-strong`, `text`, `muted`, `faint`, `accent`, `accent-soft`, `accent-text`, `link`, `ok`, `ok-soft`, `degraded`, `degraded-soft`, `critical`, `critical-soft`, `unknown`, `unknown-soft`, `pending`, `pending-soft`, `maint`, `maint-soft`, `on-status` (text on status blocks), `on-degraded`, `empty`.
- `layout` is `standard`, `duty`, `wall` or `exec`: the page layout the pack prefers.
- `brand` needs a registration with the branding feature: `product` (up to 40 characters) replaces "Unagikuma" in the console, the browser title and every notification and report; `short` (up to 12) is a short form; `hide_powered` leaves "Powered by" out of status pages. Without the registration a pack's colours are still used and the console says what is held back.

For safety a pack holds data only: no CSS, SVG or scripts, and images must be PNG or JPEG. Packs are kept in `<data>/themes/`, go into backups, and in a cluster every site takes the main server's packs (whether a site shows the product name and logo depends on that site's own registration). The program name on the command line stays `unagikuma`.

## Registration and quotas

Without a licence everything works except reading the audit log, and nothing is ever limited. Install the `license.json` you received under **Settings › Registration**; a trial licence is issued for the installation number shown there.

A licence may limit the number of nodes (the master included) and of accounts. Within the limit nothing changes; beyond it, adding a node, a joining machine, a new account or an imported account is refused with a message saying so. Nothing already in use is switched off, for example after a smaller licence replaces a larger one; the registration page shows the usage against each limit.

## Moving from Uptime Kuma, Nagios, Checkmate or Gatus

The import adds what Unagikuma can run and lists everything else with the reason and what to do instead. It never changes or deletes monitors that are already here, so running it twice is safe: what came over the first time is listed as already here. Always look first with `--dry-run`. On a new machine you can import before the first start: the import creates the data directory.

**From Uptime Kuma** (1.23 and 2.x). Stop Uptime Kuma, then:

```sh
unagikuma import kuma --dry-run /path/to/uptime-kuma/data/kuma.db
unagikuma import kuma /path/to/uptime-kuma/data/kuma.db
```

A JSON backup from Uptime Kuma 1.x works too, but has no accounts, maintenance windows or status pages. What comes over: HTTP, keyword, JSON query, TCP port, ping, DNS, push (the push URLs keep working: only the server address changes), Docker, gRPC, Redis, PostgreSQL and MySQL monitors with their intervals, retries, groups (nested groups become `Parent / Child`), tags and paused state; notifications (Feishu, DingTalk, WeCom, Telegram, Slack, Discord, webhook, SMTP, ntfy, Gotify, Pushover, Teams, PagerDuty, Matrix, Mattermost, Google Chat, Bark, ServerChan, PushPlus, Opsgenie) as channels with their credentials stored as secrets; weekly, monthly, cron and one-time maintenance windows; status pages; and accounts with their passwords (on a new installation the first one becomes the administrator). Monitors Unagikuma cannot run (MQTT, MongoDB, SQL Server, RADIUS, game servers, upside-down mode, …) are listed with a suggested replacement.

**From Nagios** (and Icinga 1, Naemon). Point it at `nagios.cfg`, so the files it names (`cfg_file=`, `cfg_dir=`, `resource_file=`) are read the way Nagios reads them:

```sh
unagikuma import nagios --dry-run /etc/nagios4/nagios.cfg
unagikuma import nagios /etc/nagios4/nagios.cfg
```

A folder or single `.cfg` files work too. Templates are resolved; hosts become ping checks that their services depend on (`parent=`); `check_http`, `check_tcp`, `check_ssh`, `check_dns`, `check_dig`, `check_ntp`, `check_snmp` (uptime), `check_mysql`, `check_pgsql` and similar plugins become built-in checks; other plugins become script checks that start paused until you copy the plugin into the check-scripts directory. `$USERn$` values from `resource.cfg` become secrets, passive-only services become push monitors, contacts with an e-mail address become e-mail channels (sent through this machine's sendmail command, as Nagios sent them; on a machine without one, to its SMTP port on localhost, which the report points out), contact groups become group channels, and time periods become `PERIOD` records.

**From Checkmate** (bluewave-labs/Checkmate: **Settings › Export monitors**, the `monitors.json` file) **or Gatus** (its YAML configuration file):

```sh
unagikuma import checkmate --dry-run monitors.json
unagikuma import gatus --dry-run config.yaml
```

HTTP checks come over: name, group, address, method, interval, paused state and the expected status codes, plus TLS and redirect settings, headers, request body and Checkmate's bearer token, which are stored as secrets. For Gatus, an endpoint must have exactly one `[STATUS] == N` condition. Anything that would not behave the same here (other check types, body or response-time conditions, alerting, proxies, addresses with a query string or credentials) is listed and stops the whole import, so nothing is half-imported: change it in the source file or add it by hand. A name that is already used here also stops it; rename the source monitor. No accounts or notification channels are created. The file may be up to 1 MiB.

**The report.** All of them print a summary (imported / needs a look / not imported), a table of what was imported, and for each item that needs a look or was not imported the reason and what to do. The same report is saved as text and Markdown in `<data>/import-reports/`. The new records go in one block at the end of `monitors.conf`, in sections headed by where they came from; `title=` keeps the original names. The web UI does the same under **Configuration › Import**. Options: `--area SECTION` puts the monitors in a section, `--role` sets the role of imported accounts, `--report DIR` saves the report elsewhere. Exit status: 0 done (or nothing new), 1 failed (nothing changed), 2 wrong usage.

## Forgot your password?

On the machine where Unagikuma runs (it may keep running):

```sh
unagikuma reset-password --data /path/to/data admin
```

Type the new password twice (it is not shown), or use `--generate` to get a random one printed once. Everyone signed in as that user is signed out. API tokens keep working; add `--revoke-tokens` to revoke that user's tokens too. If the user name is wrong, the command lists the existing users.

## Command line

```text
unagikuma [server] [flags]       run the monitor and its web UI (default)
unagikuma node list|revoke       list the machines of a cluster, or disconnect one
unagikuma check [file]           validate a monitors.conf file
unagikuma serial [bump] [file]   print or increase the revision
unagikuma probe <target> [k=v]   run one check now, e.g. "udp 10.0.0.1:ntp"
unagikuma keywords [dialect]     list the UDP keywords
unagikuma backup [--passphrase-file F] [--history]
                                write a backup archive
unagikuma restore ARCHIVE        restore a backup
unagikuma reset-password [user]  set a new password (--generate makes one)
unagikuma audit verify|export   check the audit log for tampering, or export it
unagikuma report incident ID     write an incident's report (Markdown, HTML or JSON)
unagikuma version
```

Main server flags: `--data DIR` (or `UNAGIKUMA_DATA`), `--listen ADDR` (or `UNAGIKUMA_LISTEN`, default `:8077`), `--tls-cert`/`--tls-key`, `--base-url` for links in notifications, `--heartbeat-url` for an external dead man's switch (for example healthchecks.io), `--trusted-proxy CIDR,...` when running behind a reverse proxy, `--audit-retention 730d` for how long the audit log is kept at least. The port answers plain HTTP and TLS; with `--tls-cert` the UI uses your certificate and plain requests are redirected to https.

## Data and backups

The data directory holds `monitors.conf`, `core.db` (users, incidents, notification log, configuration history, encrypted secrets), `history.db` (check results and uptime) and `unagikuma.key`, the key that encrypts the secrets. `cluster-ca.*` and `cluster.*` are the certificate authority the other machines of a cluster pin; keep them with the key so they keep trusting a restored master.

A backup is an archive made while the server runs, from **Settings › System** or the command line:

```sh
unagikuma backup --history                         # databases and monitors.conf
unagikuma backup --passphrase-file pass.txt        # encrypted, also the key and the cluster CA
unagikuma restore --passphrase-file pass.txt unagikuma-20261101-0300.ukbackup
```

With a passphrase the archive is encrypted (argon2id, AES-256-GCM) and a restored master keeps its secrets and its cluster. Without one, keep `unagikuma.key` (or `UNAGIKUMA_SECRET_KEY`) separately. Restore into a stopped installation; `--force` replaces an existing one.

## API

Everything the UI does goes through a REST API under `/api/v1`. Create a token under **Settings › API tokens** and send it as `Authorization: Bearer uk_…`; tokens are scoped to read, ack, write or admin.

- **Concurrent edits.** Every monitor, channel, policy, node and maintenance window the API returns carries an `etag`. Send it back as `If-Match` when changing or deleting the record: if someone changed that record in the meantime the request fails with `412` and code `record_changed`, and nothing is overwritten. Changes to other records never conflict. Without `If-Match` the last write wins.
- **Safe retries.** A `POST` with an `Idempotency-Key` header (any unique string, at most 255 characters) is applied once; repeating it within `api.idempotency-ttl` (24 hours) returns the first answer with `Idempotent-Replayed: true`. Failed requests are not remembered, so they can be retried with the same key.
- **Pages.** `GET /api/v1/monitors?limit=100` returns monitors in id order with a `next_cursor`; pass it as `cursor=` for the next page until it is `null`. Without `limit` the whole list comes back sorted for display.
- **Stable ids.** Every monitor has an `id` that survives renames; `/api/v1/monitors/id:42` addresses it wherever a name is accepted.
- The file format itself is specified in [monitors.conf syntax](spec/monitors-conf.md).

## Troubleshooting

- **Everything turned red at once.** Look at *Where it breaks* on the overview: if this host's gateway or uplink fails, the problem is on this side, and alerts for the targets are held until it is back. The Internet gateway under the internet targets (`localnet.exit-target`, 8.8.8.8) is pinged only once an internet target fails, to tell a broken site from a broken way out; while they all answer it shows as not checked. It is shown only; whether this host or the target is to blame is decided with the reference points (`localnet.references`). Change its address under **Settings › General** (empty turns it off).
- **A monitor is down but nobody was told.** Open it and read *Notifications* in the drawer; the event log shows each delivery with its error, and **Alerting › Channels › Test** sends a test message.
- **Ping checks say "checked over TCP" or "privilege".** This host does not allow ping for Unagikuma. Allow unprivileged ping (`sudo sysctl -w net.ipv4.ping_group_range="0 2147483647"`) or give the binary `CAP_NET_RAW`; see Quick start.
- **A script check stays paused.** Enabling it lists the nodes that lack the script; **Settings › Check scripts** shows each node's copy.
- **"Monitoring data cannot be written".** The data directory refuses writes (full disk, read-only file system) for longer than `storage.write-alert` (1 minute). Results and events are kept in memory and written once space is back; a red banner in the UI and `/healthz` (`"status": "degraded"`) say so meanwhile. Free disk space; the incident resolves itself.
- **An edit to monitors.conf is ignored.** Increase the revision; a file with errors is never applied, and the UI shows the error with its line.
- **The standby has no copy.** **Run mode** shows when it last fetched the master's state and the last error; it retries after `cluster.standby-retry` and on every configuration change.

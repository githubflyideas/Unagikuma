# Changelog

## Unreleased

**Changed**
- The ISP style is redrawn as a network operations centre: a dark blueprint grid, a slim icon rail that opens over the page instead of a hidden drawer, panels with bracketed corners, monospaced figures, and failing rows and the problem count that pulse until handled. The Starship bridge style turns violet so the two dark styles no longer look alike, and the Retail chain table header is readable again.
- Every style keeps the page centred on a wide screen, with the navigation beside it.
- Fewer things to type. Adding or changing a user: the role is a drop-down, teams are clicked, and an operator's sections are picked from the regions and nodes instead of typed as names separated by commas (on a single machine the choice is not shown). A channel group's members are clicked from the existing channels. An import's section is chosen from a list and shown only in a cluster; the import's "section manager" choice is gone (give an operator sections when editing the user instead).
- Adding a user asks for the name, e-mail address and phone number in the same form, so the user can receive alerts at once.
- The Small business, Data centre and Modern dashboard styles are gone. An installation or account set to one of them falls back to classic.

**Fixed**
- At a zoom of 125% or more, a dialog (such as adding a channel) and the menu ran past the bottom of the window; they now fit and scroll.
- On a phone, the ISP, Retail chain, Starship bridge and Atelier styles lost their menu: the menu button was hidden and the navigation spilled over the page. Every style now uses the usual menu drawer on a phone.

## v2.7.1 — 2026-11-26

**Added**
- In a cluster, each site's own configuration reaches the master as a copy (`<data>/sites/<node>.local.monitors.conf`, never applied, never sent on), so a site's own monitors survive the loss of its disk.
- On a node of a cluster, the console says that the shared configuration is changed on the master and links to it, and the buttons that would change it (quick add, add monitor) are greyed out instead of failing on save. This site's own monitors are still edited on the node.
- An import can go into this site's own configuration instead of the shared one ("Import into" in a cluster). On a node it is the only choice; a shared import there is refused at once, before anything is stored. An import is held by the same monitor limit as any other addition.
- In a cluster, a monitor that other sites check too asks them to check it at once the moment it first fails here, before the failure is confirmed. When the alert goes out after the retries, its site line says for each other site whether it was checked after the failure or gives that site's last word and how long before the failure it was — so it shows whether the target is down or only this site's way to it. Nothing ever waits for the other sites; an unreachable or slow site just shows its last word. Each monitor asks at most once in five minutes, many failures at once go to each site as one request, and only sites that prove their identity are heard.
- In a cluster, the monitor list shows what every site sees next to each monitor — ✓ normal, ✗ failing, ⊘ no recent word — so it is clear at a glance whether one site's way to a target fails or the target itself. Before, it was only on the monitor's own page.

**Changed**
- New logo: the eel and the bear in a green circle, in the console, the browser tab and the READMEs.
- Simpler monitor limits without a licence. A single installation may have up to 300 monitors in all — the shared configuration and the site-local one together, however they were added. In a cluster the shared configuration may have up to 100, and each site's own configuration up to 100 of its own, judged at that site alone. An import is held to the same limits; the separate 200-monitor import ceiling is gone. The registration page shows this site's own count in a cluster. The licence text and the READMEs say the same.
- The sites' observations an alert carries are read where the alert is sent, not in the engine loop: a slow disk holds back that one alert for three seconds at most, then it goes without the site line, and the checks and other alerts never wait. Before, the read happened in the engine loop and could hold up everything for up to ten seconds.
- A site's observation of a monitor counts as stale (⊘) after three missed checks and never sooner than three minutes, so a monitor checked every few seconds no longer flickers to unknown on a short network hiccup.

**Fixed**
- On a single installation the site-local configuration counts toward the monitor limit like the shared one. Before, it did not count at all. Saving one that would pass the limit is refused, and one changed by hand past it is not applied — the last one runs on.
- The monitor limit now holds at startup too. A monitors.conf grown past the limit while the program was stopped is not applied; the last version runs on and a banner says why. A first start has nothing to compare with: the file runs as it is, with the over-limit notice if it is over.

## v2.7.0 — 2026-11-25

**Changed**
- The registered styles are redrawn so each one changes the whole layout, not only the colours, and there are three more. Small business: a floating icon rail that opens on hover. Data centre: a green terminal with a function-key bar for navigation. Retail chain: big coloured touch tiles and coupon-shaped cards. ISP: a wallboard whose navigation is a drawer at the left edge and whose failing rows are solid. New: Modern dashboard (a navigation bar across the top), Starship bridge (a dock at the bottom over a starfield, glass panels) and Atelier (index tabs on the right edge, ledger pages). On a phone every style keeps the usual drawer. The free seven-day trial covers all of them.
- Unagikuma is now licensed under the Business Source License 1.1 (see LICENSE). Production use is free within the free limits — 100 monitors, 5 accounts, an import may bring the count to 200 — without getting around them and not as a hosted service for others; beyond that a commercial licence is needed. Each version becomes Apache License 2.0 four years after it is published. Releases up to v2.6.37 were published under the Apache License 2.0 and stay under it.
- An import may now take the number of monitors to 200, down from 300.
- No flood of messages when many monitors fail at once. Nobody is sent more than 20 alerts a minute (`notify.backstop-max`, `notify.backstop-window`); a person is someone in the contact directory (phone and e-mail together) or an unknown address, and a chat room is one recipient. When anyone an alert goes to has had their share, the whole alert waits for one summary through the same channels, listing every held alert with its target, ordered by address. Before, the limit was 3 a minute per channel, new problems had a share of their own, recoveries and escalations were never held, and the summary named only the first ten monitors.

**Fixed**
- Alerts no longer blame an address block. Several monitors in one /24 failing used to be called "a network failure: the segment, its switch or its uplink", in alerts and summaries alike, though an address block says nothing certain about switches or links; a target that does not answer is now said to be unreachable, nothing more. A refused connection was counted as a network failure too; it now says what it is: the host answers, but nothing accepts the connection on that port, so the service is not running or not listening.
- In a summary of held alerts, alerts on the same address are listed by port, not by monitor name.
- Adding a monitor past the monitor limit through the API is answered 402 `quota_exceeded`, like the account limit, instead of 400 `invalid`; the diagnostic is still included.

## v2.6.37 — 2026-11-25

**Added**
- The web UI can now be served under a path prefix behind a reverse proxy (for example `/uk/`): pages, assets, the API and the sign-in all stay inside the prefix when the proxy sends `X-Forwarded-Prefix` and is listed as a trusted proxy.
- A first start whose monitors.conf has errors no longer stops the program. It starts in repair mode — the web UI is up, nothing is monitored yet, and the file can be fixed in the editor; saving a correct configuration starts monitoring without a restart. A configuration that was already running is unchanged: its last good version keeps running, as before.

**Changed**
- Importing from Uptime Kuma keeps every check's timing as it was: retry intervals are carried over (they could be lost before, which made retries happen more often than in Uptime Kuma), timeouts are no longer shortened, and a check whose interval is missing says which default it will run at.
- Gatus and Checkmate imports now bring over everything they can instead of refusing the whole file when one item cannot be carried over; every item that is not carried over is listed with its reason. More Gatus conditions are understood, Checkmate's status windows become attempt counts, and Nagios services whose host is only named — by a host group or a service — get that host, marked as made up in the report.

**Fixed**
- Webhook channels with a form body and a custom template now send values intact: a value holding `&` or `=` stays one field's value. Use `{{raw .X}}` in the template for a value that must be sent unescaped.
- The notice that a cluster node has gone down now says when the node was last in contact.

## v2.6.36 — 2026-11-25

**Added**
- Licences can now carry a monitor limit of their own, next to the account limit; a licence without one allows any number of monitors.
- The four registered styles can be tried free for 7 days before a licence is needed; the style picker shows how many trial days are left.

**Changed**
- A free (unregistered) installation now allows 5 accounts and 100 monitors; the two gateway monitors Unagikuma adds by itself do not count towards the 100. Adding a monitor beyond the limit is refused — in the web UI, through the API and in a hand-edited configuration alike — while everything already there keeps running. Importing from another tool, or restoring, may bring the total up to 300 instead, and the import report says how many monitors there are afterwards; an installation above its limit shows a notice until it is back under it or registered.
- The audit trail — viewing it, exporting it and checking its integrity — is now registered-only. It is still recorded either way.

## v2.6.35 — 2026-11-25

**Changed**
- Licence files are now signed with ECDSA P-521 (licence format version 3), replacing the Ed25519 signatures of earlier releases. The old keys and format are retired outright: a licence issued for an earlier release is not accepted by this one, and a reissued licence file is needed to register. Everything else about licences — what they lift, the account limit, the audit window — is unchanged.

## v2.6.34 — 2026-11-25

**Added**
- Four scenario styles — Small business, Data centre, Retail chain and ISP — are now the registered styles. A registered installation can set any of them for an account or as the installation default; without a licence they are listed in the style picker but cannot be used. The wall and summary views now follow the installation's style too; before, they always used the classic look.

**Removed**
- The fifteen legacy styles are removed: modern, relic, legend, starship, rune, aurora, nebula, ocean, sunset, forest, sakura, dune, neon, glacier and ink. The classic style stays, and stays free. An installation or account still set to a removed style falls back to classic on its own.

## v2.6.33 — 2026-11-22

**Changed**
- In cluster alerts, the per-node summary now marks each node with a symbol that works without colour: ✓ for normal, ✗ for not normal and ⊘ for unreachable. The old circle/cross/tilde marks are gone, and the summary no longer appends the up-count fraction or the gateway/uplink reading — a delivered alert could only ever show those as fine, so they carried no information.
- The daily report's title now labels its host count as "down or unreachable" instead of folding unreachable hosts into "down".
- A notification delayed by less than a minute now shows the delay in seconds (for example "[delayed 30 s]") instead of "0 min".
- Removed data fields that were defined but never populated: the notification quorum value, the monitor votes/quorum data and the node witnesses count. The `{{.Quorum}}` and `{{.AckURL}}` notification template variables are removed with them — a template that still names one is refused at configuration-check time, so remove the name to save that channel.

**Fixed**
- When the server restarts after an abnormal exit and the previous shutdown left no heartbeat to measure from, the notice now says the downtime is unknown instead of reporting a made-up "0s". The event data no longer carries a downtime value in that case either.
- A public status page no longer claims an overall "ok" before anything has been judged: with no monitors, or only monitors that have never produced a verdict, the overall status is "unknown". It turns "ok" only once a monitor actually holds an ok verdict.
- A monitor that has never been measured no longer shows a latency of "0 ms". The interface shows "n/a", and the API's `latency_ms` is null until the first measurement, the same way `uptime_24h` already behaves.

## v2.6.32 — 2026-11-22

**Fixed**
- Branding changes (instance name, logo and icon) now require registration, like the other registered features. Styles are unchanged and stay free for everyone.
- When an upgrade adds the two gateway monitors to an existing installation, the addition is now recorded in the event stream and the audit log instead of happening silently.
- The two gateway monitors are pinned to the top of the monitor list, in that order, whichever sorting the list uses.
- The degraded status pill in the calm theme now meets the 4.5:1 contrast minimum.

## v2.6.31 — 2026-11-22

**Added**
- The /api/v1 API now has a machine-readable OpenAPI spec. The server serves it at /api/openapi.json, and every release archive ships it as docs/openapi.json. A test compares the spec with the real routes on every run, so the two cannot quietly drift apart.
- Releases now include a CycloneDX SBOM (unagikuma-v2.6.31-sbom.cdx.json) listing the exact modules in the build.
- Release checksums are signed. SHA256SUMS.sig and SHA256SUMS.pem are published with each release, and the README's "Verifying downloads" section shows how to check them with cosign before trusting a download.

## v2.6.30 — 2026-11-20

**Fixed**
- Operators can no longer use a secret or redirect an administrator's check through a `$DEFAULT`.
- Upgrading from a release before v2.6.28 with `warn=`/`crit=` on a PUSH monitor no longer stops the server from starting: the options are removed once and the original file is kept as `monitors.conf.before-push-options`.

## v2.6.29 — 2026-11-20

More of the review's findings.

**Fixed**
- A Redis-like page that lacks some INFO fields (Valkey, KeyDB, a managed service) is still Redis: the missing numbers are absent from the value, only a page without the identifying field is refused.
- No more "critical → critical" state events, and a value already in the check's own text is not added again ("Inlet = 31 °C · 31 °C").
- A service target written as a URL gets one clear error (host and port only; account and database go in their own options).
- `unagikuma probe -h` prints help. SNMP port and sensor indexes are kept per community and user.
- Kuma-style notifications: `tags` is an array, `description` is the monitor's own, `timezone` is a zone name.
- The daily report no longer counts the gateway monitors the installation adds.
- Incidents show as `#5` instead of the long internal id (the whole id stays as the tooltip).
- The code for registered-only styles is gone: every style was already free.
- Interface: the Run mode icon is drawn, the sign-in title is no longer squeezed by the language box, and a monitor's details name the common metrics (latency, HTTP status, packet loss, certificate days, clock offset) instead of showing their keys.

## v2.6.28 — 2026-11-19

A review by a tester: two critical and six high findings, and most of the medium ones.

**Fixed**
- A target answering "NaN", "Infinity" or 1e999 made every monitor list come back empty; such values are refused where they are read, never stored, and an answer that cannot be encoded is a 500 with a reason.
- The account limit could be passed with requests at the same moment; the count and the insert now share one transaction.
- Operators could read any saved secret by adding a check that sent it to their own server; records that use `${secret:…}` or `${env:…}` or run an SQL query are now the administrators'.
- SNMPv3 checks whose passwords are secrets could not be saved from the web page or the API.
- With 1000 monitors the list was unusable: it draws 200 rows (more on request), applies changes once a second and filters after typing pauses.
- warn=, crit= and unknown options on a PUSH monitor are refused instead of looking set.
- Importing another Unagikuma names what it would do here: secrets of this installation sent to its addresses, alerts going to a team or policy that exists here; team members are not carried over.
- Limits mean one thing: `5m` on a time is minutes, `1Gb` is refused (bits or bytes?), `1d12h` is 36 hours, no temperature below −273.15 °C.
- Values kept for a change rule belong to the definition that produced them; a redefined, deleted or renamed monitor leaves none behind. The new tables reserve `tenant_id`.
- "N notifications sent" counts what was delivered. A value beyond its limit is its own reason class.
- SQL checks are stopped by the server when the check gives up (`statement_timeout`, `max_execution_time`).
- The packet budget also covers DNS, NTP, SNMP and syslog checks.
- The daily report preview is as restricted as the weekly one.
- The built-in team is called the default team (Chinese role name: 可操作).
- Chinese: application field names and the messages of value rules and Redis are translated.
- Phone width: batch buttons only when rows are ticked, the toolbar wraps.
- Documentation, README and command-line help follow the product (APP, `$QUORUM`, import sources, `docs/value-types.md` in the packages).

## v2.6.27 — 2026-11-19

**Fixed**
- The outbound packet budget (`probe.max-pps`, 200 a second) held back every check, not only pings and UDP: with thousands of HTTP or TCP monitors on short intervals many went unchecked. It now applies to pings and UDP checks only.
- The monitor list and the overview slowed down with the square of the number of monitors. With 20,000 monitors a page of the list took 2.7 s; it now takes about 0.2 s.

## v2.6.26 — 2026-11-19

**Changed**
- Registration: without a licence the audit log shows the last 3 days (export and the integrity check need a registration), and up to 5 accounts can be added. All interface styles are free. A licence no longer limits nodes, has no tiers and lists no features (registered means all of them); it never gives fewer than the free 5 accounts.

## v2.6.25 — 2026-11-18

**Added**
- Config & import can import another Unagikuma's `monitors.conf`: what is already here is left alone (importing a file twice adds nothing), a monitor with a taken name is added under a new name with its `parent=` following, a taken channel, period, team or status page name keeps the one here. A secret the file names but this installation lacks blocks the import until it is added.
- Config & import is now Import and export, with two downloads: *Configuration only* (`monitors.conf`, no accounts, secret values or history) and *Everything* (one passphrase-encrypted archive with accounts, secrets and their key, settings and history). The old Settings › Configuration backup tab is gone.
- A backup can be restored from the same page: the archive is checked and kept, and the next start of the program puts it in place, keeping the files it replaces in a `restore-before-…` folder (a failed restore puts everything back). Cancel it while it waits.
- The monitor list's search takes `failed:2`, `failed:>=1`, `-failed:1`: monitors whose check has failed that many rounds in a row.

**Fixed**
- A monitor edited so that it must be checked again showed PENDING for a whole interval if it was paused and resumed meanwhile. A resumed monitor, and one with no result yet, is now checked at once.
- "Check now" answers with the result (`HTTP 200`, or why it failed) instead of "queued".
- The first-run screen said "No permission" when the setup token was wrong. It now says the token is wrong, where to copy it from and that restarting prints a new one.
- Every text of the console is translated in all 13 other languages (some were still English).

**Changed**
- Settings: the System tab is gone, its cards sit under General; Style and branding and Audit come just before Registration.
- Status pages › Wall screen now also holds the managers' summary.
- The overview no longer says "Not probed: …" under the internet gateway.
- The Sport theme is removed (accounts that had it get Calm); the Country theme is a little earthier and lighter.

## v2.6.24 — 2026-11-15

**Added**
- `warn=` and `crit=` on an application or Redis monitor can join two to four comparisons with `and` or `or`: `crit="busy>50 and queue>100"`.

**Fixed**
- Limits that could never work (same number for warn and crit, a limit below zero on a count, over 100%, a unit that does not fit the field) are refused when the file is checked.
- A text and a number are no longer judged on the same value; an SNMP port's limits need `value=`.
- `change=` now judges a rise from 0; a counter not yet listed by the application is 0, not a failure.
- `query=` refuses locking reads (`FOR UPDATE` and the like).
- The top bar's state label is readable in every theme.

**Changed**
- Five application fields whose meaning was not certain are no longer offered (Redis `master_last_io`, Traefik `reload_failures`, Apache `cpu` and `rps`, MinIO `open_fds`).
- The automatic gateway monitors head the list.
- `unagikuma probe` applies `warn=` and `crit=` to its reading.

## v2.6.23 — 2026-11-15

**Added**
- APP profiles for Prometheus, etcd, Caddy, RabbitMQ, Grafana, Traefik and MinIO, each run on the real application.

## v2.6.22 — 2026-11-15

**Added**
- REDIS monitors can judge a number of `INFO` with `field=`, `warn=` and `crit=`: clients, memory, hit rate, evictions, replication. Verified on Redis 7.0.15.
- POSTGRES and MYSQL monitors can judge the number a `query=` returns (one read-only `SELECT`). Verified on PostgreSQL 16 and MariaDB 10.11.

## v2.6.21 — 2026-11-15

**Fixed**
- The HAProxy profile read a metric HAProxy does not have (`haproxy_backend_up`) and failed on every real HAProxy; it now reads backend and server status as HAProxy names it, and a frontend in TCP mode (no HTTP counters) reads too.
- The Nginx, Apache, Lighttpd, PHP-FPM and HAProxy profiles were run on the real applications; each says the version.

## v2.6.20 — 2026-11-15

**Added**
- Application status pages (type APP): Nginx, Apache, Lighttpd, PHP-FPM and HAProxy, read through profiles kept as data.

## v2.6.19 — 2026-11-15

**Fixed**
- SNMP `expect=` on a number matched part of it (10 matched 100); it is now exact, and `expect-num=` compares.
- DNS `expect=` on an address or name matched part of it; it is now the same address or name.
- Value rules on a counter OID compared its ever-growing total; they now judge its rate.
- A limit whose unit does not measure the value (500Mbps on a percentage) is refused; so is crit lying nearer than warn.
- SNMP uptime is read as seconds, as shown.

**Added**
- `>=` and `<=` limits.
- Value types: what each option may judge, in docs/value-types.md.

## v2.6.18 — 2026-11-15

**Added**
- Rune style for registered installations: carved titles, corner brackets, framed status plates; its fonts are built in.
- Ten wallpaper styles for registered installations: Aurora, Nebula, Ocean, Sunset, Forest, Sakura, Dune, Neon, Glacier and Ink.

**Fixed**
- Starship's background grid did not show.

## v2.6.17 — 2026-11-15

**Added**
- Three page styles for registered installations: Relic, Legend and Starship.

## v2.6.16 — 2026-11-14

**Added**
- Page styles: Classic or Modern, chosen per account, with a default for everyone.
- Branding for registered installations: product name, logo and browser icon.

**Fixed**
- Contrast of warning text and pending labels.

## v2.6.15 — 2026-11-14

**Fixed**
- Value rules on an SNMP `oid=` check never fired.
- Value rules that get no value now show the monitor as degraded.
- `{{monitorJSON}}` and `{{heartbeatJSON}}` in Kuma templates print JSON.
- Contrast of the status page banner, status colours and sidebar text.

## v2.6.14 — 2026-11-14

**Added**
- SNMPv3 with authentication and encryption.
- SNMP port checks by name: traffic, utilisation, state and speed.
- SNMP temperature sensors, with Huawei, H3C and Cisco tables.
- Value rules for any number a check reads: limits, sudden change, duration.
- Value card on the monitor page.

## v2.6.13 — 2026-11-14

**Added**
- Perfume theme.
- Weekly report.
- Daily report up to four times a day.
- Monitor list sorted by clicking a column.

**Changed**
- Settings: internet gateway, external heartbeat and backup have their own tabs.
- Interface size: 100% or 125%.
- Page tabs and view switches look different.
- Live-update status no longer replaces the run mode label.
- Adding a region needs two or more nodes.

**Removed**
- The auto theme and the failures filter menu.

**Fixed**
- Paused monitors kept sending repeat and escalation alerts.

## v2.6.12 — 2026-11-14

**Added**
- Interface size: 100%, 125%, 150%, 200%.
- Ping monitors for the LAN and internet gateways.
- Failed checks marked on the latency chart.
- Monitor list filter by failures before an alert (`attempts:1`).
- Common values to pick for interval, timeout and failures.

**Changed**
- Larger small text; path view fonts made consistent.

**Fixed**
- Daily report through an Uptime Kuma webhook template showed an empty monitor that was DOWN.

## v2.6.11 — 2026-11-14

**Added**
- Five new themes: Country, Industrial, Calm, Sport, Passion.
- Suppressed alerts can be opened per day and per reason.
- Time periods are made from days and hours.
- Daily report: send time, teams and channels.
- Channel headers field; channels can be renamed.
- Health card shows the DNS servers in use; custom DNS servers.

**Changed**
- Users and contacts merged under Alerts › Users.
- Expiry is a tab on the monitors page; quick add is the main button.
- Account settings open from the user name in the sidebar.
- Monitor options match each monitor type.
- Change notices moved to status pages.

**Removed**
- Theme packs and the Appearance tab.

**Fixed**
- Gateway shown on Windows.
- Uptime Kuma channel headers imported.
- Channel edits keep options not shown in the form.

## v2.6.10 — 2026-11-13

**Added**
- Uptime Kuma webhook templates work as pasted; imports keep them.
- Channel forms show the request in each service's own format, with ready-made bodies.
- Delivery rules are set on the channels tab.
- Simpler external heartbeat form: GET or PUSH, host information and credentials as fields.
- Internet gateway chosen by city.

**Changed**
- Menus renamed and reordered; run mode shown in the top bar.
- Monitor list, status pages and problems pages tidied; expiry moved under problems.
- Channel kinds grouped by type, with search and sorting.
- Bright and Modern themes redesigned; themes picked from colour dots.
- Change-notice templates, team form and monitor options easier to use.
- Dialogs open over the page content.

**Fixed**
- First-account setup appears even when the browser holds an old session cookie.
- Form validation messages follow the interface language.

## v2.6.9 — 2026-11-13

**Added**
- Custom SMS and voice gateways (`custom-sms`, `custom-voice`): an operator's own HTTP interface, one request per number, with `{{.Phone}}` and the message names in the address, headers and body. They follow the contact directory like the provider channels. The generic webhook is shown as a custom webhook.
- Status pages can be edited (title and the monitors shown) and deleted, and another one added at any time. Only creating one was possible, and the form went away once a page existed. `PUT` and `DELETE /api/v1/status-pages/{name}`.
- The channel form: the built-in kinds are grouped too (chat robots, e-mail, SMS and calls, custom). The message template has buttons that insert a name at the cursor, ready-made templates (short, detailed, one line for SMS) and a preview of an example problem as it is typed (`POST /api/v1/channels/preview`). DingTalk and WeCom mentions are picked from the contact directory or set to everyone. The time period is chosen from the defined ones, inside or outside it (`GET /api/v1/periods`).
- SMS, voice-gateway and WhatsApp channels take `prefix=`, put in front of every message, such as `【Beijing host1】`, so the reader knows which site sent it. Aliyun and Tencent Cloud SMS start with their approved signature instead.
- **Advanced** in the sidebar holds the settings most installations set once: appearance, teams, regions, tunables, API tokens, secrets and UDP keywords. The old addresses still open them.
- **Settings › General › Internet gateway** can be chosen two ways. **By country**: pick one of 12 countries or regions and the public resolvers local to it are measured from this host. **Detect automatically**: this host's public address and country are read from two fixed HTTPS services (what `curl ifconfig.me` does), the route out is traced in Go (what traceroute does; it needs root or CAP_NET_RAW and is skipped otherwise), and the fastest resolver of that country and the first public router on the way out are proposed. Only listed addresses, or routers on this host's own route, are measured; no shell or external program runs; one run every 20 seconds at most; nothing is saved until the administrator picks one.
- 95 more channel presets, 112 in all, grouped in the channel menu by kind and region: team chat (Rocket.Chat, Webex, Zulip, Zoho Cliq, Nextcloud Talk, ClickUp, Signal, Threema …), China, Japan and Korea (QQ through OneBot and Milky, KOOK, WxPusher, PushDeer, Chatwork, KakaoWork, Dooray!, JANDI …), incident tools (Alerta, GoAlert, Grafana OnCall, ilert, incident.io, Squadcast, SIGNL4, Splunk On-Call, PagerTree, GitLab alerts …), WhatsApp (Meta Cloud API, Z-API and Zenvia for Brazil and Latin America, WAHA, Evolution …), SMS (Infobip, Vonage, Plivo, Telnyx, MessageBird, Unifonic and Taqnyat for the Gulf, Ooredoo …), e-mail APIs (SendGrid, Brevo, Resend) and automation (Zapier, n8n, IFTTT). Incident tools resolve on recovery. That covers 98 of the 111 Uptime Kuma notification kinds, 42 of 43 in Gatus and all of Checkmate's; the rest need request signing or a login step (AWS SES, Web Push, Nostr, Microsoft Graph) or are single-vendor stores.
- Template function `digits` keeps the digits of an id so it can be a JSON number.
- Quick add takes `icmp 9.9.9.9`, and its examples stay visible under the box, to copy or insert, down to one with every option: `https://pay.example.com name=pay title=支付网关 team=admin region=global scope=wan group=payments tags=env:prod,tier:1 policy=default`. A line with more than one target is refused instead of keeping the first and dropping the rest.

**Changed**
- The search box sits next to the page title instead of at the far right of the top bar.
- The screens tab keeps the on-duty wall screen only; the managers' summary is no longer offered as a wall link.
- The overview's texts are worded for operators, and the cluster summary says what the per-site design does: each node checks and alerts on its own, the others' observations are for reference. It still described the majority vote that 2.6 removed.
- The contact form shows name, status, e-mail, phone and the daily report; the channels follow the addresses until they are picked by hand, and the department, linked login and @name are under More. The status (active, on leave, moved, left) says what each one does.
- The site-local configuration is `local.monitors.conf` (`site-local.conf` is renamed at startup) and shows its file name. A missing `$FILE` line is added on save, a short form such as `ping 8.8.4.4` is answered with the full line to write, and the errors say what a monitor line looks like. On a single machine the section stays hidden until the file holds something.
- A new status page is titled "<this host> Unagikuma notice board", and the form says the title can be changed at any time.
- The managers' summary is opened from the report tab, signed in; its form no longer makes links with a token.
- On a wide screen the pages sit in the middle, with the top bar lined up.
- Themes: Mist is now Bright, Graphite is now Modern and Violet is now Egypt (papyrus, lapis lazuli and terracotta), with new colours. A theme chosen before stays chosen.
- **Configuration** in the sidebar is named after what it mostly shows: the configuration and its history.

## v2.6.8 — 2026-11-12

- Update golang.org/x/crypto to v0.59.0.

## v2.6.7 — 2026-11-12

Every channel kind was sent problems, recoveries and a long daily report through stand-ins that check requests the way the real services do, and through the real services with wrong credentials. What that found:

**Fixed**
- A long message, usually the daily report, went over the limit of WeCom (2048 bytes) and was refused. Every chat kind and preset now cuts a message to its service's limit at a line, and the last line says how many lines are left out.
- More than 20 alerts a minute to a DingTalk robot were lost: the robot refused them and nothing sent them again. A channel never sends more than its service takes in a minute (`notify.service-limits`); the rest wait and a long queue becomes one summary.
- An alert that no channel delivered counted towards `notify.max` and was only sent again at the repeat interval (30 minutes); after three failures the incident went quiet without anyone having been told. It no longer counts, and is sent again after `notify.failed-retry` (2 minutes, doubled each time up to the repeat interval). The system channels are told once, not at every try.
- `Retry-After` of a rate-limiting service was not read. The next try now comes when the service says; if that is longer than `notify.retry-after-max` (30s), the next channel takes the alert.
- `rate=` and `retries=0` on a channel had no effect.
- A wrong webhook token that the service reports under HTTP 200 (Feishu, DingTalk, WeCom) was tried three times, so the next channel got the alert 9 seconds late. It is tried once.
- An alert showed `<no value>` for a name its template did not know. It is empty, and such a template is reported: refused in the console, a `template_problem` warning in a file.
- A `webhook` with `method=GET` carried nothing: its `url=` may now hold names.
- A failed delivery says why, not only "HTTP 400": the service's own words follow.
- A Twilio call was read by the English voice whatever the language, emoji and brackets included. It is read in the message language, without them.
- The daily report lists services that failed at the same moment by name instead of in a different order each day.
- The diagnostic of a channel test no longer lists an issue without an address; the case is the `no_recipients` field, as in `people/lookup`.

**Added**
- `template=` (the **Message template** field) sets the text of problems and recoveries on chat, e-mail, push and Twilio channels; **Test** shows it filled with an example. New names: `{{.StatusText}}`, `{{.KindText}}`, `{{.ReasonText}}`, `{{.DurationText}}`, `{{.Started}}`, `{{.Icon}}`, `{{.Class}}`. The option used to be accepted and ignored.
- `at=` mentions people on DingTalk, WeCom and Feishu: `all`, or phone numbers (Feishu: user ids).
- Channels for Pushbullet, LINE (Messaging API), ClickSend SMS, Datadog events and Apprise API.
- The manual lists every channel kind, the names a template can use, and how limits, retries and falling back work.

**Changed**
- SMS and voice channels take the daily report only with `report=yes`. It used to fill an approved alert template with report text, and ring every morning.

## v2.6.6 — 2026-11-11

**Fixed**
- A master that restarts after a take-over joins the new master within a few seconds, also when it never learnt the new master's own identity. It checks the new master through the cluster CA and looks for it every 2 seconds for the first two minutes. It used to need about a minute, and both machines acted as master until then.
- A promote sent to a server that is already the master (for example a request repeated after a client timeout) is refused. It used to take over again at a higher term and forget which master it had replaced, so that master's rejoin was refused as a duplicate node name.
- A bare `standby=127.0.0.1:8077` is accepted again: it is read as `https://127.0.0.1:8077`, with a `standby_scheme_added` warning. 2.6.5 refused such a file and would not start.
- `POST /api/v1/people/lookup` no longer lists an empty issue when nobody would be notified. The case is a separate `no_recipients` field.
- `POST /api/v1/incidents/{id}/silence` accepts the duration wrapped as `{"record": {"duration_s": …}}` as well as flat. A wrapped and a flat duration that differ are refused.
- API errors are in the caller's language (the `Accept-Language` header, or the console's language), with a `Content-Language` header. They were always in English.
- Counted texts use the right plural in French, Portuguese, German, Spanish, Hindi, Bengali, Urdu, Russian and Arabic.

**Accessibility**
- Every console page and the public status page pass the WCAG 2.1 A and AA checks of axe-core in all seven themes. Form fields are tied to their labels, the segmented controls are tabs, code blocks can be scrolled with the keyboard, links in text are underlined and faint texts have enough contrast.

**Changed**
- The command line reads passwords and the terminal width without golang.org/x/term. The only modules besides the standard library are golang.org/x/crypto, x/net, x/sys and modernc.org/sqlite.

**Internal**
- Long functions in the importers, the configuration reader, the engine, the store, the checks, the start-up and the server are split into smaller ones. Behaviour is unchanged; no function outside the tests has a cyclomatic complexity above 30.
- New tests run the command line against a real installation, translate every API error message, check every plural form, and restart a replaced master in three ways (by address, by name, after losing its peer list).

## v2.6.5 — 2026-11-11

**Fixed**
- A replaced master that learns of the new master from its notice, and whose join is then refused (for example by the rate limit), keeps the offer and tries it again every 15 seconds while its code is valid, also after a restart. It used to stay outside the cluster.
- A master keeps the identities of its standbys within seconds of their joining, not up to 30 seconds later. A master lost right after a standby joined could not recognise the new master when it came back, and waited for its notice.
- A member refused because another member is taking over at the same time is told so, instead of being told that another master is already running.

**Security**
- Built with Go 1.27.4, which fixes an HTTP server crash in the standard library (GO-2026-6859).

**Tests**
- End-to-end tests run real clusters for the cases above: two promotes at once, a promote after a handover round trip (master killed or frozen), rejoining while refused and after a restart, several machines joining from one address, and editing a file whose revision counts 1, 2, 3.
- The browser test expects the Settings entry that team members have had since 2.6.3 for their own password.

## v2.6.4 — 2026-11-11

**Fixed**
- A master replaced by the same-term tie-break, whose first rejoin request was refused (for example by the rate limit), retries every 15 seconds, also after a restart. The retry loop never ran, so such a node stayed outside the cluster.
- Two members promoted at the same moment no longer both become master. A member waiting to take over shows it to its peers; the higher term goes ahead, and at the same term the smaller node name, and the other promote is refused.
- A member that was master once and handed the role back promotes to a term above the cluster's, so its take-over is not undone when the old master returns. A forced promote from such a member is no longer refused either.
- A configuration whose revision counts 1, 2, 3 … can be edited in the console and over the API. The first edit moves it to a date revision, which used to be refused as a jump of more than 1000000.
- `unagikuma probe` reports a warning (a certificate inside its warning days, loss above the threshold) as `warn` and exits 2; it used to say `ok`.
- `unagikuma restore` accepts its flags after the archive too.

**Changed**
- Joining, rejoining, retiring and node requests without a valid token have their own rate limit per address, `cluster.join-rate` (30 a minute), apart from sign-ins. Several nodes joining one after another from one address no longer run into the five sign-ins a minute.
- A preview from `POST /api/v1/import/{source}` carries an `Unagikuma-Import` header that says to send the same body with `?apply=1` to import. The manual describes the API import.

## v2.6.3 — 2026-11-11

**Fixed**
- A master replaced by a higher term rejoins as a replica without a restart (join URL works when listen is `:8077`; CA-signed rejoin offer with nonce / no replay; only after Reach confirms the winner).
- Channel **Test** uses the same active-contact directory as real alerts by default. An empty whitelist is not reported as success. Optional connection-only mode is labelled as such.
- A manual take-over (promote) is refused while the current master still answers, since it would make two masters; move the role with a handover from the master. It can be forced for a master that runs but is broken. Two members promoting at once are still refused as before.
- A secret that the configuration still uses can no longer be deleted. Deleting it used to make the file invalid, so every later edit was refused until the secret was put back. The refusal names the records that use it.
- Operators and viewers can change their own password (**Settings › Account**). An API token cannot change a password.
- HTTP and TCP checks of a name that resolves to both IPv6 and IPv4 try the other family when the first does not connect: `localhost` with a service listening on IPv4 only is no longer reported down. `family=4|6` still pins one.
- `unagikuma probe` and **Check now** apply the default certificate expiry thresholds (`probe.cert-warn-days`, `probe.cert-critical-days`) like running monitors do.
- HTTP checks follow up to 10 redirects, as the error message says (they stopped at 9).
- `/healthz` no longer says "replaced by a newer master" once that server has rejoined as a member.
- Running a Checkmate or Gatus import again skips what the first run brought over, instead of failing on the names it already added. The same name for a different check still stops the import.
- `GET /api/v1/reports/summary` refuses a `range` other than 7d, 30d or 90d instead of quietly answering for 30 days.
- A Telegram bot token without the `number:` part is refused when the channel is saved, not when the first alert fails. An Aliyun reply without a result code says so instead of an empty error.
- `unagikuma node revoke` fails for a node that has no token; `unagikuma report incident --format` checks the format before reading the incident; `unagikuma version` refuses extra arguments.

**Removed**
- On-call rotations, the **On call** page and its stand-ins, swaps and response-time statistics. A group that still has `rotate=`, `handoff=`, `start=` or `tz=` keeps loading with a `rotation_removed` warning and notifies all its members. The `oncall.*` tunables have no effect and stay only so old `$SET` lines remain valid.

**Changed**
- Default `notify.channel-retries` is 2 (about 2s then 5s). Permanent errors are not retried. Explicit `0` still means no retries. Probe retries are unchanged.
- Startup and the System page warn when the UI is served over plain HTTP. The listen address is unchanged; agent traffic on the same port remains pinned TLS.
- An HTTP monitor with both `basic=` and `bearer=` gets a `conflicting_auth` warning: only `bearer=` is sent. The configuration spec now lists the `invalid_option` and `unknown_secret` diagnostics.


## v2.6.2 — 2026-11-10

**Fixed**
- Pausing or resuming a monitor is local to the site where you do it. It no longer goes to the master, does not write `disabled=yes` into the shared configuration, and does not change other sites. A replica can pause while the master is unreachable. Shared `disabled=yes` in the configuration file remains a default; resume on one site overrides it only there.

## v2.6.1 — 2026-11-10

**Changed**
- The project moved to https://github.com/githubflyideas/Unagikuma. The README, the console's links and the status page footer point there. Nothing changes for running installations.

## v2.6.0 — 2026-11-10

**Added**
- Teams own monitors. The built-in admin team owns every monitor without `team=` and cannot be deleted or renamed. `TEAM` records list `members=`, `channels=` and `mode=all`. What a member may do follows their account role: administrator, operator or viewer.
- Regions: `region=` on monitors, **Settings › Regions**, and moving monitors between teams and regions in bulk (one audited change). The monitor list filters by team, region and tag, and quick add fills in the team and region.
- Notifications follow the teams. A team's monitors are sent to its channels. Channels with `system=yes` carry the system's own notices and the problems of a team whose channels are not set or failed; the team's administrators are told in those cases. On a monitor, `mute=yes` sends nothing and `system-channel=yes` sends to the system channels.
- Contacts have a status: active, on leave, transferred or left. E-mail and SMS go only to active contacts in the directory; anything else is left out and logged. `contacts.notify-admins` (off by default) tells the team's administrators, once a day per channel.
- The daily status report is sent per team (**Notices & status › Daily report**, `notify.daily-report-teams`, default `admin`), each on its own monitors. A person can switch it off in **Contacts**, and a channel with `report=no`.
- On a cluster node, pausing and resuming a monitor are passed on to the master, which writes them; acknowledging stays with the site's own incident.
- Under the logo: the edition (registered or not) and the version.
- Configuration diff: side by side, changes only, or unified.
- A `tenant_id` column is reserved in the core tables. It is not used.

**Changed**
- The configuration file is `monitors.conf`. `monitors.bind` (and a node's `master.bind` and `site-local.bind`) is renamed at startup, and backups with the old name are still accepted. The record syntax is unchanged.
- `POLICY` records that only list channels become team channels at startup. A copy of the file is kept as `monitors.conf.before-teams`. Policies with timing options keep working from the file. The **Policies** tab is gone.
- Channel addresses missing from the directory are added to it once on upgrade, so nothing stops being sent.
- The top search uses the monitor list's syntax and status filter; the monitor page no longer has a second box. List pages scroll inside the list, under a fixed header.
- **Run mode** (formerly Settings › Nodes) is in the sidebar. The wall screen and the managers' summary are under **Notices & status**.
- The Problems page shows its site selector only in a cluster.
- Status pages have no Atom link. A registered installation can change the footer line.
- The Paper and Ink themes are removed; saved choices move to Mist and Graphite.
- Channels, contacts, teams and regions are changed by administrators only.

**Upgrading a cluster**
- Upgrade the nodes before the master.

## v2.5.0 — 2026-11-07

**Added**
- Every audit entry made through the web UI or the API says which sign-in and which browser or program it came from: people who share one account, even behind one office address, can be told apart. Settings › Audit shows a sign-in tag and the browser for each entry; click a tag to list that sign-in's changes, or filter by it. The configuration change each action makes (`config.apply`) carries the same tag. The CSV export has two more columns at its end, `session` and `user_agent`; the API takes `session=`.
- `--audit-retention` (or `UNAGIKUMA_AUDIT_RETENTION`), 30d to 3660d, default 730d: how long the audit log is kept at least.

**Changed**
- The audit retention can no longer be shortened from the web, so nobody can clear the log from there. Settings › Tunables shows `retention.audit` without letting anyone change it, the API refuses it, and a `retention.audit` in the configuration only ever makes the retention longer than the server's.

## v2.4.3 — 2026-11-07

**Fixed**
- A standby could fail to copy the master's state, and so take over with an old copy or none. Its copies and the nodes' connections were held to the login limit of five a minute per address even with a valid token, so a few sign-ins or configuration changes from the same address, or several nodes behind one NAT reconnecting together, got them refused. Only requests whose token fails count against the limit now.
- Messages and the overview say "node" where they said "probe node" in every language, not only English and Chinese.

## v2.4.2 — 2026-11-07

**Fixed**
- Settings › Nodes no longer offers **Add node** or **New token**: both printed commands for the probe-only agents that are gone since 2.2, which only failed. Machines are added with **Join code**, now the page's main button. `unagikuma node add` and `node token` say so instead of printing the agent commands.
- Every message of a cluster names its site the same way, by node name: problems and system notices used the node name and the node title in turn ("[b]" and "[Site B]"), and the master's messages named no site at all when it kept the default name. A single machine still names none.
- Messages and the overview say "node" where they said "probe node".

## v2.4.1 — 2026-11-07

**Fixed**
- Targets that all fail at once are alerted on time when this host's network works. A machine watching only a few sites took their failing together for its own outage and held the alerts back for 10 to 15 minutes, and could report an outage of its own that never happened. Before an internet failure becomes an alert the host now also asks reference points on the internet (`localnet.references`, by default 223.5.5.5, 1.1.1.1 and 8.8.8.8 on TCP 443): if any answers, the failure is the target's.
- A machine watching sites in fewer than three networks recognises its own uplink outage: the reference points that do not answer count as evidence too. It holds the alerts while the outage lasts and sends one summary when it is back, as machines watching more sites already did.

**Changed**
- The manual describes clusters as they work since 2.2: every machine checks and alerts on its own, and probe-only agents, `quorum=`, partial escalation and the emergency webhook are gone. The tunables that no longer do anything say so.
- `localnet.exit-target` is only shown on the path view; the reference points replace it in the check before an alert.

## v2.4.0 — 2026-11-07

**Added**
- Two more themes: Sky (light blue) and Violet (purple).
- Theme packs (Settings › Appearance): upload a zip with your colours, and on a registered installation your own product name, logo and browser icon, used in the console, notifications and reports, with "Powered by" optionally left out of status pages. Packs can be exported, go into backups and are taken over by every site of a cluster. An example is in docs/theme-pack-example.

- Wall screen (`/wall`) for a TV in the operations room: every monitor as a large hexagon, problems first, counts and a clock, a loud banner when data stops arriving. It opens signed in, or from a read-only link made under Settings › Appearance.
- Summary for managers (`/summary`): whether everything is running, availability over 7, 30 or 90 days against the period before, incidents, average time to recover, downtime, a daily chart, what needs attention and the latest incidents. Read-only, printable, opened signed in or from a read-only link.

**Changed**
- The console's sidebar logo and the browser tab icon use the new logo.
- The sidebar has its own shade in every theme, a step apart from the page, and the current page is marked with an accent bar.

**Fixed**
- A team member's incident report could still label a kept configuration change with the whole file (monitors.bind); it is now labelled with the member's own monitor.

## v2.3.0 — 2026-11-06

**Added**
- Import monitors from Checkmate (its `monitors.json` export) and Gatus (its YAML configuration), on the command line and under Configuration › Import. HTTP checks are moved; anything that cannot be moved exactly is listed and stops the import.

## v2.2.2 — 2026-11-06

**Fixed**
- A newly joined site starts checking and alerting right after it joins. It used to wait for the next periodic copy of the main server's state, up to five minutes. The main server now sends the copy as soon as the site connects, and lists the site as online only once it has that copy.
- Recovery messages are never held back by the flood guard. When many alerts went out in a short time, a recovery could be folded into the "alerts held back" summary, leaving people unsure whether the problem was over.
- Messages from a server still using the default site name no longer repeat the product name in the subject ("[unagikuma] [Unagikuma] …").

**Changed**
- Five display tones from white to black: Paper, Mist, Slate, Graphite and Ink, plus "System", which follows the device. Earlier "light" and "dark" settings carry over as Paper and Ink.
- Status labels are solid colour blocks, and tables have alternating row shading, so states read at a glance in long lists.

## v2.2.1 — 2026-11-06

**Fixed**
- Incident reports for a team member no longer show configuration changes to other teams' monitors. A change that touched both this monitor and another one used to carry the whole change into the report, with the other monitor's name and values; now only this monitor's part of the change is kept.

## v2.2.0 — 2026-11-06

- Each site in a cluster now monitors and sends alerts independently, even when the main server is available. A problem may produce alerts from more than one site.
- Alerts and recovery messages include other sites' latest results, with out-of-date results marked. This information provides context; it does not control alerts.
- Acknowledging an alert affects its source site. Changing the main server is still manual.
- Back up before upgrading. Older probe-only nodes need manual migration to full servers. See the [cluster upgrade guide](https://github.com/githubflyideas/Unagikuma/blob/v2.2.0/docs/per-site-upgrade.md).
- Container images are no longer published. Use the downloadable application or run it as a system service.

## v2.1.1 — 2026-11-05

**New**
- E-mail channels can send through the machine's own mail system (`via=sendmail`), the way Nagios and cron do: no server, port or password to set, whatever runs behind the `sendmail` command (postfix, sendmail, exim, OpenSMTPD, msmtp, ssmtp, nullmailer, dma). `program=` names a sendmail command in an unusual place. Nagios contacts are imported this way when the machine has one.

## v2.1.0 — 2026-11-05

Version 2.1: teams. Several teams share one installation and its checks, each sees and changes only its own monitors, and when the shared infrastructure fails the managers of the teams it affects are told. Contacts now always have a way to be reached. Nothing changes until a team is defined.

**New**
- Teams (**Settings › Teams**): people who own monitors, each with one role per team (manager, operator, viewer); a person can be in several teams and manage several. A monitor joins a team with **Team** in its form (`team=NAME`, also through `$DEFAULT`); monitors without one are the shared infrastructure. Teams are `TEAM` records in monitors.bind, so they reach every node with the configuration. The monitor list shows the team and filters with `team:`.
- Teams are isolated from each other. A team member sees only their teams' monitors, incidents, events, reports and maintenance windows (and only the state of shared monitors theirs depend on); managers and operators change only their teams' monitors, viewers read. Everything else, from channels to the raw configuration, stays with administrators: an API route is closed to team members unless it was reviewed for them, so new routes are closed too. Live updates and incident reports are filtered the same way.
- Team notifications: a team's monitors use the team's `policy=` unless they name their own. When the shared infrastructure fails, the managers of the teams it affects (those with monitors depending on it, or every team when none does) are told right away through their own contact channels, and told again when it recovers. **Settings › Teams** names managers who cannot be reached.
- A team's managers change their own team's people, title and description under **Settings › Teams**; creating and deleting teams and changing a team's policy stay with administrators.
- Whoever an escalation step told about a problem also gets its recovery notice.

**Changed**
- A contact always has a way to be reached: at least one valid e-mail address or phone number (both are checked), and **Notify via**, the e-mail, SMS or voice channels the system uses to reach them directly. Contacts added while saving a channel, or imported from the channels, are reached through those channels; older contacts without one are marked in the list.
- The user manual is in English only; the separate Chinese manual is gone. The web UI and the README stay in 15 languages, and each README links to the English manual.

**Fixed**
- Saving a channel, policy or other record under a name a monitor already used replaced that monitor. Names of another kind are now refused.

## v2.0.0 — 2026-11-04

Version 2.0: servers in a cluster keep monitoring and alerting on their own while the master is out of reach, checks are more careful with the targets they share, and monitors.bind starts with a plain `$FILE` line. Files and data from 1.x are read and upgraded as they are.

**New**
- Every server that joined a cluster is a replica, like a secondary DNS server: it keeps a copy of the master's configuration, secrets and data, and when it cannot reach the master for `cluster.replica-after` (1 minute) it checks, stores and alerts on its own until the master is back. Every message names the site that sent it (`[site]` in the title, `site` in webhooks).
- Node status is judged by majority: every node checks the others and reports what it reaches, its own health and how its notifications went out. A node is reported down only when more than half of the cluster cannot reach it; a node in the minority reports itself cut off instead.
- After a split, what a replica recorded on its own is merged into the master: history, incidents, decisions and audit entries are tagged with site and term, each site keeps its own audit chain, and incidents show which site recorded them.
- Promoting a replica keeps this site's records and merges them in, and shows how old its copy is. A replaced master that comes back rejoins as a replica, offers its data, sends its agents to the new master, and a configuration that diverged meanwhile is kept aside under **Nodes**. Configuration versions carry their term.
- Site-local configuration (**Nodes › Site-local configuration**, `site-local.bind`): monitors, channels and policies of one server only, checked there in every role and never overwritten by the master.
- Monitors that check exactly the same thing (one service watched by two teams, say) are pointed out by `unagikuma check` and, while you type, by the monitor form. `share=yes` lets one use the other's check instead of sending its own (`probe.share-window`, 10s); each keeps its own thresholds, incidents and notifications. The form also says how many other monitors watch the same host.

**Changed**
- monitors.bind starts with a plain header line instead of an SOA record: `$FILE revision=2026110401 interval=1m retry=15s timeout=5s attempts=3`. Only `revision=` is required; `master=` and `admin=` are optional. New installations write it; existing files with `@ IN SOA … ( … )` are read exactly as before and need no change. The web UI and messages now say "revision" instead of "serial".
- A UDP check that only proves the port is not closed (L1: `SYSLOG`, `snmptrap`, unknown ports) no longer counts silence as OK on its own: a host that is switched off cannot say the port is closed either. The host is pinged alongside (or knocked on over TCP without ping permission); silence is OK while it answers, UNKNOWN when it does not, and a failure when it is reported unreachable. `host-check=off` keeps the old reading.
- Pings and weak UDP checks of the same address start at least a second apart on each node (`probe.same-target-gap`). A host watched by several people, or a ping next to a few UDP ports, no longer gets them in one burst, which devices answer only in part.
- PING checks fail when more than half of their echo requests go unanswered (`probe.ping-loss`, 50%); before, only a total loss failed. `loss=` on a monitor still wins, and `loss=0` keeps the old behaviour.

**Fixed**
- Pages and the menu badges stopped updating while live events kept coming faster than their refresh delay, as they do with a few failing monitors on short intervals: each event pushed the refresh back. They now refresh at most a moment after the first event.
- Two UDP checks with the same fixed source port (`src-port=`) at the same time: the second could not bind it and failed. They now take turns; a port that stays busy, or that another program holds, leaves that round UNKNOWN.
- A PING check with `size=8` reported an internal error when an ICMP error came back.
- A UDP reply that only came after a retransmission had its latency measured from the retransmission, so a service that needed a second try looked fast. It now counts from the first request.
- The UDP echo check failed on a stray datagram that arrived before the echo; it now waits for the echo and fails only if nothing else came.

## v1.5.6 — 2026-10-30

**New**
- Moving from Uptime Kuma or Nagios is no longer a preview. `unagikuma import kuma|nagios` prints a clear report (imported / needs a look / not imported, each with what to do) and saves it as text and Markdown in `<data>/import-reports/`. `--dry-run` checks everything without changing anything; running it again adds only what is new.
- Uptime Kuma: 1.23 and 2.x databases (also while it runs), nested groups, maintenance windows, status pages, push URLs that keep working, ntfy, Gotify, Pushover, Teams, PagerDuty and more notification types, passwords and tokens stored as secrets. On a new installation the first Uptime Kuma account becomes the administrator.
- Nagios: point it at `nagios.cfg` and the files it names and `resource.cfg` are read; quoted Debian plugin arguments, passive checks (push monitors), DNS, NTP and SNMP checks are understood; plugins come over paused until they are in the check-scripts directory.
- The imported records go in one tidy block at the end of `monitors.bind`, in sections headed by where they came from, aligned like `unagikuma fmt`.
- `unagikuma reset-password` asks for the new password twice without showing it, lists the users when the name is wrong, and `--generate` makes a random one.

- Incident reports: each incident has a report of what led up to it, how it was detected, who was told when and on which channel (and what was held or merged, and why), and how it recovered, with whether this host was watching all along. Open it from **Report** on the Problems page, download it as Markdown, HTML or JSON, or write it with `unagikuma report incident ID`.
- Every decision about an alert (queued, sent, failed, held, released, merged, dropped, suppressed, acknowledged) is recorded, so a missing or late alert can be explained afterwards.
- Problem notices say what kind of failure it is: the service itself, a network segment, not certain yet, or this host's network. Webhooks get it as `class`.
- A channel sends at most 3 alerts a minute (`notify.backstop-max`, `notify.backstop-window`); the rest come as one summary at the end of the minute. Escalations and system notices always go out; new problems have a share of their own.
- The audit log is tamper-evident: `unagikuma audit verify` and **Check integrity** find changed, inserted or removed entries. It also records sign-ins, backups, restores, imports, password resets and what the system did by itself, highlights high-risk entries, filters by risk, address and time, and exports CSV or JSON (`unagikuma audit export`).
- `unagikuma reset-password --revoke-tokens` also revokes that user's API tokens.
- On call: a month or week calendar of who is on call each day (`GET /api/v1/oncall/calendar`), and swapping two shifts (`POST /api/v1/oncall/swap`), on top of stand-ins.
- Monitors: tick monitors and **Pause** or **Resume** them together (`POST /api/v1/monitors/bulk-pause`, one change to the file).
- Certificates now warn 62 days before they expire (`probe.cert-warn-days`, was 21), also for monitors imported from Uptime Kuma; thresholds you set yourself are kept. The overview lists certificates inside that window, and the expiry list is now called **Certificate validity**.

**Fixed**
- On call: response times were counted for whoever was scheduled, even when a stand-in since cancelled, or the next member because the person on call could not be reached, took the alert. They now count for whoever was told.
- On call: shifts of hours (such as `rotate=12h`) moved by an hour at each daylight saving change; they now keep their local handoff times like day shifts. A handoff time the clocks skip happens when they resume, and one that happens twice is the first.
- On call: handoffs were always read in the server's time zone, so a server running in UTC handed over at the wrong hour for a team elsewhere. `tz=` gives a rotation its own time zone.
- On call: the recovery of a problem went to whoever was on call when it recovered, so a stand-in (or the person before a handoff) who was told about the problem never heard that it was over, and the next person got a recovery for a problem they never saw. It now goes to the person who was told.
- A local outage whose last reply and first failure came at the same instant was not recognised, and its failures were alerted.
- A problem that began while the disk was full got no recovery notice.
- A service that failed on its own shortly before this host lost its network was taken back as part of the outage.
- When this host's DNS resolver stopped answering while targets checked by address still answered, it was taken for a lost uplink; it is now recognised as the resolver, and its summary says so. An HTTP check that runs out of time before its name resolves says the name could not be resolved.
- A crash notice that could not be sent during a local outage was lost; the outage summary now names what could not be sent.
- After the process was stopped or the VM paused for a while, "Host overloaded" was reported on resume.
- A notice no longer repeats its reason ("No response within the timeout — no response within the timeout").
- The local-outage summary gives the time the network came back, not the time that was confirmed a minute later.
- `unagikuma check` now finds channel settings the server would refuse.
- A monitor added to a group from the web UI is aligned like that group's records.

## v1.5.1 — 2026-10-29

**Upgrading from v1.0.0**
- The database gets a table for on-call stand-ins on the first start; v1.0.0 then refuses to open it, so make a backup first (`unagikuma backup`) if you may want to go back.
- Announced changes, status pages and the daily report moved from Alerting to **Notices & status**; bookmarks to their old addresses still work.

**New**
- On call: `rotate=daily|weekly|Nd|Nh` on a contact group makes it a rotation that notifies only whoever is on call, and tries the others in turn only when that person cannot be reached; `handoff="mon 09:00"` sets when shifts change and `start=` the date from which the first member is on call. The **On call** page shows who is on call now and the next shifts, creates rotations, arranges stand-ins (someone covers for a while; the order stays), and measures MTTA and MTTR over `oncall.stats-window` (30 days), counting each incident for whoever was on call when it opened.
- The Problems page lists setup problems that keep alerts from reaching anyone: no channel, a default policy without channels, recipients missing from the contacts, broken check scripts. Its menu entry gets a red dot while there are any.
- The monitor list: tick monitors (or all of them) to check them now or delete them together; each row has an Edit button, and clicking a row opens the monitor's page. **Add monitor** and **Quick add monitors** sit together. `POST /api/v1/monitors/bulk-delete` deletes several monitors in one configuration change.
- The internet gateway of *Where it breaks* is set under **Settings › General** (8.8.8.8 by default, empty turns it off); its label on the path view links there.
- The sidebar lists Monitors before Problems, and On call and **Notices & status** get their own entries.
- **Configuration › monitors.bind** keeps only the copy button; the error banner offers to view the configuration rather than to edit it.
- A new logo: the eel and the bear.

## v1.0.0 — 2026-10-29

The first stable release. The `monitors.bind` syntax is frozen as version 1 (see `docs/spec/monitors-bind.md`): later 1.x releases read every file this one accepts the same way.

**Upgrading from v0.2.0**
- Make a backup first (`unagikuma backup`, or Settings › Backup) if you may want to go back: the history moves to a new layout on the first start, which takes a moment on large installations, and older versions then refuse to open it. A copy of the previous `core.db` is kept as `core.db.bak-N`.
- Monitors get a stable id. Renaming one in the web UI or the API keeps its history, incidents and push token; when renaming by hand in the file, write `was=OLD` once.
- The container image is `ghcr.io/githubflyideas/unagikuma`.
- Addresses used by e-mail, SMS and voice channels get a red dot until they are in the new contact directory: **Alerting › Contacts › Import them** adds them all at once. They are still sent to meanwhile.
- Uptime shows "—" with the share watched until a monitor has been watched for `uptime.min-coverage` (10%) of a window, instead of a figure from a few minutes of data.
- The "unhandled" count now includes regional and low-priority incidents and no longer counts UNKNOWN monitors, so the sidebar, the overview and the problems page always agree.
- One recipient that does not exist no longer fails a whole e-mail; SMS and voice channels send number by number.

**New in 1.0**
- Contact directory (**Alerting › Contacts**): channels only send to people in it; someone marked as left stops receiving alerts and their login and API tokens stop working; addresses refused as nonexistent `contacts.bounce-threshold` times (3) are no longer sent to; unknown addresses are flagged with a red dot on the channel, the policies using it and the Alerting menu. Channels can name people as `@handle`. New addresses are offered for the directory when a channel is saved, and `unagikuma check` reports the same problems.
- Announced changes (**Alerting › Changes**): planned work is announced through a policy or channels, reminded before it starts (`changes.reminders`), holds the monitors it touches in maintenance, can post a status page notice, and ends with a receipt (done, rolled back, cancelled). Searchable history with CSV export; the notice templates can be replaced.
- `DOMAIN` checks watch when a domain's registration expires (RDAP, or WHOIS where a registry has none); **Monitors › Expiry dates** lists every domain and certificate.
- Status pages render their first screen on the server: they read at once, without JavaScript and in search results. Status pages can be created from the UI.
- Changing a failing check (timeout, keyword, target) keeps its incident instead of closing it and paging again for the same outage.
- Acknowledgements are kept as a history with who, when, through what and the note; the first acknowledgement time is kept.
- API: every record has an etag for `If-Match` (412 when someone else changed it), creates accept `Idempotency-Key`, monitor lists page with `limit`/`cursor` in stable id order, and `/monitors/id:N` addresses a monitor by id.
- Writing back from the UI keeps option order, quoting and column alignment; `$VERSION 1`; `unagikuma fmt [--check]` and `unagikuma check --json`; URLs with a query are always targets.
- `/metrics` describes the monitoring host too: rounds started, dropped and late, history writes, database size, memory, live connections. Every engine event is also one structured log line.
- Nodes announce their check types: a node too old for a check is not sent it and is shown as needing an upgrade.
- The raw history shrinks to `storage.low-disk-raw` while the disk is nearly full; script directories given relative to the working directory work.
- The first-run guide adds registering who receives alerts and publishing a status page.
- The monitor form: the check type is a dropdown grouped by kind; host and port are separate fields, and an empty port uses the type's default without writing it into the target; DNS checks have their own fields for the name to look up, the record type and the expected answer; the group field suggests the existing groups.
- **Configuration › monitors.bind** shows the file read-only, with copy and download; monitors, channels and policies are changed on their own pages. **Import** shows Uptime Kuma and Nagios as two sources to pick from.
- *Where it breaks* draws the internet gateway (`localnet.exit-target`) as a small mark under the internet targets. It is pinged only once an internet target fails; while they all answer it is shown as not checked. The line to the internal targets shows as working when the gateway cannot be judged but those targets answer.

**Also since v0.2.0**
- A first-run guide on the overview: add the first monitors in one box, add a channel, put it in the default policy; each step ticks itself off.
- The command palette (Ctrl+K): type an address to add it, `host:?` for the UDP keywords, `ack NAME`, `silence NAME 1h`, `check NAME`, or part of a name to open a monitor.
- The monitor drawer says why a monitor did or did not notify: maintenance, its parent is down, this host's own outage, only some nodes failing, soft state, acknowledged, silenced, the notification limit, a policy without channels.
- The monitor page header no longer overlaps on phones.
- Service checks: `REDIS` (login, PONG, optional role=master/replica), `POSTGRES` (md5 or SCRAM login, SELECT 1, ssl=yes), `MYSQL` (native or caching_sha2 login, ping, ssl=yes), `GRPC` (the standard health check), and `DOCKER` (a container on each probing node: running and healthy). Without credentials the database checks prove the server answers. Uptime Kuma's Redis, PostgreSQL, MySQL, Docker and gRPC monitors are imported as these (without passwords).
- DNS checks can require an exact answer set with `expect-all=`, so an added or replaced record fails.
- Time periods: `office IN PERIOD "mon-fri 09:00-18:00; sat 10:00-14:00"`, and `period=office` (or `period=!office`) on a channel to use it only inside (or outside) them.
- Contact groups: `ops IN CHANNEL group members=feishu,oncall-sms` notifies all members at once and succeeds when any of them does.
- Escalation: `escalate=30m:leader,2h:director+sms` on a policy notifies more channels while an incident stays open and unacknowledged.
- Event handlers: `handler="restart.sh $SERVICESTATE$ $SERVICESTATETYPE$"` on a monitor runs a script from the check-scripts directory on each state change, with the Nagios macros and `UNAGIKUMA_*` variables; only administrators may set it.
- SMS and voice calls through Aliyun, Tencent Cloud and Twilio.
- Status pages: announcements with severity and resolution, upcoming maintenance, e-mail subscriptions with double opt-in (`subscribe=`), an Atom feed, and an own host name (`domain=`) that serves nothing but the page.
- Licence quotas: a licence that limits nodes or accounts refuses adding more beyond it (when adding a node, a machine joining, creating an account or importing accounts); nothing already there is switched off. Settings › Registration shows the usage. Without a licence there is no limit.
- A user manual in English and Chinese (`docs/manual.md`, also in the release archives), and a shorter README with a tour of the UI.
- Fixed: the overview and the monitor list stayed blank once a channel had delivered notifications (a missing import in the channel statistics).
- A standby whose copy of the master's state fails tries again after `cluster.standby-retry` (30 seconds, three times) instead of waiting for the next sync.
- End-to-end tests in CI: browser tests of the web UI (Playwright) and a master failover with real processes (`go test -tags e2e ./e2e/`).
- Nagios import (`unagikuma import nagios DIR`, or Configuration › Import): hosts, services, commands, contacts, contact groups, time periods and event handlers, with templates resolved. Common plugins become native checks, others EXEC checks running the same plugin; services depend on their host.

## v0.2.0 — 2026-10-28

- The event log and the monitor timeline read in the UI language: events record a message code and its values, and the UI writes the text. Events recorded by earlier versions keep their English text.
- Known issue: the overview and the monitor list go blank once a channel has delivered notifications; fixed in v0.3.0.

## v0.1.1 — 2026-10-28

**Upgrading from v0.1.0**
- The default port is now **8077** (8066 collided with another common tool). Pass `--listen :8066` to keep the old one; agents installed with the old address need their `--server` URL updated.
- Built with Go 1.27.3; no reachable known vulnerabilities in the dependencies.
- API: latency is reported as `latency_ms` and captured packets as `request_hex`/`response_hex`.

**Fixes**
- Secret values no longer appear in evidence, details or the packet viewer, wherever they occur in the request or response.
- A host started without network no longer sends one alert per monitor; a dead site on a working network still alerts at once.
- New, redefined or resumed PUSH monitors get a full period before they are late.
- Status page `select=` accepts the same selectors as maintenance windows.
- URLs with a query string (`?a=b`) can be saved from the UI and the API.
- Paused and maintenance states show the same everywhere; a slow monitor that fails no longer sends "degraded ended" first.
- Quorum percentages, durations and numeric options refuse NaN, infinities, negative and overflowing values.

**New**
- `/healthz` for load balancers and containers, `/metrics` in the Prometheus format.
- A new console look, dark by default: the overview shows where a failure is on the way out (node → gateway → uplink → internet gateway → targets) for every probe node, a one-line verdict, every monitor as a honeycomb (or as a vote matrix with several nodes), what needs attention and the alerts held back this week. The sidebar folds to icons. Clicking a monitor opens it in a side drawer (how the round was decided, its path, what happened, its record); the monitor list has a honeycomb view; phones get the path as a list; vote verdicts are translated.
- A check path on each monitor: this host → gateway → internet exit (8.8.8.8 by default, `localnet.exit-target`) → target, so a broken uplink is told apart from a broken site. The exit is for display only and never holds alerts.
- Registration: the audit log can be viewed with a licence; everything else stays free.
- Script checks (Nagios plugin compatible), Uptime Kuma database import, master handover with a warm standby, daily status report — all experimental in this release.

## v0.1.0 — 2026-10-23

First public preview. One binary, one machine, everything included.

**Monitoring**
- HTTP(S) checks with status, keyword, JSON path and certificate-expiry assertions; TCP with TLS and send/expect; ICMP ping (unprivileged when the system allows it); UDP checks that speak the real protocol of DNS, NTP, SNMP v1/v2c (and v3 discovery), TFTP, ONC RPC, time and syslog by keyword, plus raw send/expect.
- Every check reports its depth (L1 sent, L2 port answers, L3 service answered correctly) and a specific reason when it fails.
- Push monitors compatible with Uptime Kuma push URLs.
- Soft/hard states with retries; incidents with acknowledgement, silences, reopen windows and notification limits.
- Detection of problems on the monitoring host itself (gateway, uplink, DNS, sleep or stalls): affected checks are held instead of alerting on every target, and a summary is sent afterwards.

**Probe nodes (optional)**
- Run `unagikuma agent` on other machines to check the same targets from several places; a single machine needs none of it.
- Each round is decided by a quorum of the nodes, so one site's broken link does not page you; a failure seen from only some nodes is shown as partial and, if it lasts, becomes one low-priority regional alert.
- Agents connect out to the master over TLS on the same port, pin its certificate and use a per-node token that can be rotated or revoked. They receive only the checks they run; secrets stay in memory.
- Nodes that drop out are reported, and judgement continues with the remaining ones.

**Notifications**
- Feishu/Lark, DingTalk, WeCom, Telegram, Slack, Discord, email (SMTP) and webhooks, with presets for Bark, Gotify, ntfy, PagerDuty, Opsgenie, Pushover, Matrix, Mattermost, Microsoft Teams, Google Chat, PushPlus and ServerChan.
- Policies with fallback or broadcast chains, repeats, delays and limits; deliveries survive restarts and are logged with their result.
- External heartbeat URL so you also hear about it when the monitoring host goes down.

**Configuration and UI**
- `monitors.bind`: one readable text file, editable in the browser or by hand, validated before it is applied, with history and one-click restore.
- Web UI with overview, problems, monitors with heartbeat bars and live updates, latency charts, configuration editor, channels and policies, tunables, API tokens and encrypted secrets.
- Public status pages and SVG badges.
- 15 languages for the UI and notifications, with right-to-left layouts for Arabic and Urdu.

**Platforms**
- Static binaries for Linux (amd64, arm64, armv7, 386, riscv64), macOS (amd64, arm64), Windows (amd64, arm64) and FreeBSD (amd64).

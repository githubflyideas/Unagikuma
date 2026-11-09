# Migration report: Nagios → Unagikuma

2026-10-30 09:00, nagios.cfg.

| | |
|---|---|
| Imported | 31 monitors, 4 notification channels, 1 contact group, 4 notification policies, 2 time periods |
| Needs a look | 6 items (imported; see below) |
| Not imported | 8 items (see below for what to do) |
| Duplicates | 2 checks found twice (imported once) |

Added to `monitors.conf`.

## Monitors

| Name | Type | Checks | Group |
|---|---|---|---|
| core-rtr1 | Ping | 10.0.0.1 | network |
| core-rtr1-ntp | NTP | 10.0.0.1 | network |
| core-sw1 | Ping | 10.0.0.2 | network |
| core-sw1-passive-trap \* | Push | waits for results | network |
| core-sw1-snmp-uptime | SNMP | 10.0.0.2 | network |
| crlf-host | Ping | 10.0.9.9 | linux-servers |
| crlf-host-ssh | TCP port | 10.0.9.9:22 | linux-servers |
| db1 | Ping | 10.0.2.21 | db-servers |
| db1-disk | Script (paused) | check\_nrpe -H 10.0.2.21 -c check\_disk | db-servers |
| db1-mysql | MySQL | 10.0.2.21:3306 | db-servers |
| db1-postgresql | PostgreSQL | 10.0.2.21:5432 | db-servers |
| db2 \* | Ping | 10.0.2.22 | db-servers |
| db2-disk | Script (paused) | check\_nrpe -H 10.0.2.22 -c check\_disk | db-servers |
| db2-mysql | MySQL | 10.0.2.22:3306 | db-servers |
| dmz-noaddr \* | Ping | dmz-noaddr |  |
| mail1 | TCP port | mail1.example.com:25 | linux-servers |
| mail1-dns-resolve | DNS | 10.0.0.53 | linux-servers |
| mail1-dns-system-resolver | DNS | mail1.example.com | linux-servers |
| mail1-ssh | TCP port | mail1.example.com:22 | linux-servers |
| web1 | Ping | 10.0.1.11 | web-servers |
| web1-admin-basic-auth | HTTP | http://10.0.1.11/admin | web-servers |
| web1-api-json | HTTP | http://10.0.1.11/api/health | web-servers |
| web1-disk | Script (paused) | check\_nrpe -H 10.0.1.11 -c check\_disk | web-servers |
| web1-http | HTTP | http://10.0.1.11/ | web-servers |
| web1-https-shop | HTTP | https://shop.example.com/cart | web-servers |
| web1-restart-on-failure \* | TCP port | 10.0.1.11:8080 | web-servers |
| web2 | Ping | 10.0.1.12 | web-servers |
| web2-api-token | HTTP | http://10.0.1.12/api | web-servers |
| web2-disk | Script (paused) | check\_nrpe -H 10.0.1.12 -c check\_disk | web-servers |
| web2-http | HTTP | http://10.0.1.12/ | web-servers |
| winsrv1 | Ping | 10.0.3.5 | windows-servers |

## Also imported

| Name | What | Details |
|---|---|---|
| nights | time period | mon-fri 00:00-08:00; mon-fri 18:00-24:00; sat-sun 00:00-24:00 |
| workhours | time period | mon-fri 09:00-17:00 |
| alice \* | channel (Email) | to alice@example.com |
| bob | channel (Email) | to bob@example.com, during workhours |
| dave | channel (Email) | to dave@example.com, during nights |
| nagiosadmin | channel (Email) | to nagios@localhost |
| oncall | contact group (group) | notifies alice, dave |
| notify-bob | policy | sends to bob, repeats every 1h |
| notify-nagiosadmin | policy | sends to nagiosadmin, repeats every 1h |
| notify-nagiosadmin-30m | policy | sends to nagiosadmin, repeats every 30m |
| notify-nagiosadmin-oncall | policy | sends to nagiosadmin, oncall, repeats every 1h |

_* needs a look: see below._

## Needs a look (imported)

1. **db2 (Ping)**  
   Its active checks were turned off in Nagios, so it is imported paused.  
   To do: Resume it when you want it checked.
2. **dmz-noaddr (Ping)**  
   It has no address in Nagios; its name is checked instead.
3. **Channel "alice"**  
   The pager number (15550001111) was not carried over.  
   To do: Add an SMS or voice channel for it.
4. **core-sw1 / Passive trap (Push) → core-sw1-passive-trap**  
   It only received passive results in Nagios; here it waits for results sent to its push URL (1h without one is a failure).  
   To do: Have the sender call the push URL shown on the monitor (…/api/push/TOKEN?status=up\|down&msg=…) instead of NSCA or NRDP.
5. **web1 / Restart on failure (TCP port) → web1-restart-on-failure**  
   Its event handler runs restart-app.sh.  
   To do: Copy that script into the check-scripts directory.
6. **Script checks: check\_nrpe**  
   These checks run Nagios plugins, so they are imported paused: Unagikuma only runs plugins from its check-scripts directory.  
   To do: Copy the plugins into &lt;data>/check-scripts (for example from /usr/lib/nagios/plugins), then resume the checks in the web interface.

## Not imported

1. **Contact "carol"**  
   Why: It has no e-mail address.  
   To do: Add a channel for this person (SMS, chat or e-mail) under Alerting › Channels.
2. **ghost-host / Orphan**  
   Why: Host ghost-host is not defined.  
   To do: Nothing to do if that host is gone; otherwise add the check by hand.
3. **web2 / Uses undefined command**  
   Why: The command check\_foo is not defined.  
   To do: Add the check by hand.
4. **web2 / Unknown macro**  
   Why: It uses $\_HOSTSITE$, but host web2 has no \_SITE custom variable.  
   To do: Add the check by hand.
5. **web2 / Uses resource macro**  
   Why: It uses $USER2$, which is not set in the resource file.  
   To do: Add the check by hand.
6. **Time period "holidays-excluded"**  
   Why: No imported contact uses it.  
   To do: Nothing to do, or add a PERIOD record when a channel needs one.
7. **Definitions: 1 × define serviceescalation**  
   Why: Escalations were not converted.  
   To do: Add escalate= to the policy, for example escalate=30m:manager (Alerting › Policies).
8. **Definitions: 1 × define hostextinfo**  
   Why: Icons and extra host information are not used here.  
   To do: Nothing to do.

## Duplicates (imported once)

1. **web2 / HTTPS shop (HTTP) → web1-https-shop**  
   It checks the same thing as web1-https-shop, so it was imported once, as web1-https-shop.
2. **mail1 / SMTP (TCP port) → mail1**  
   It checks the same thing as mail1, so it was imported once, as mail1.

## Good to know

- Hosts become ping checks (or their own check command); their services depend on them, so a host that is down does not page for each service.
- Flap detection, freshness checks, performance data and acknowledgement settings have no direct equivalent and were not copied.
- Past states and history stay in Nagios: history starts fresh here.
- E-mail channels send through the local mail server (localhost:25) like Nagios usually does; change host= and from= under Alerting › Channels if needed.

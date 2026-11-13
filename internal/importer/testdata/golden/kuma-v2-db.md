# Migration report: Uptime Kuma 2.x → Unagikuma

2026-10-30 09:00, kuma.

| | |
|---|---|
| Imported | 32 monitors, 10 notification channels, 9 notification policies, 1 maintenance window, 1 status page, 1 account |
| Needs a look | 10 items (imported; see below) |
| Not imported | 12 items (see below for what to do) |

Added to `monitors.conf`.

## Monitors

| Name | Type | Checks | Group |
|---|---|---|---|
| no-maintenance-banner | HTTP keyword | https://example.com/ | Production |
| postgres-port | TCP port | db.internal.example:5432 | Production |
| website | HTTP | https://example.com | Production |
| www.example.cn | HTTP | https://www.example.cn/ | Production |
| api-health-json | HTTP JSON | https://api.example.com/health | Production / API |
| api-with-headers | HTTP | https://api.example.com/v1/ping | Production / API |
| login-page | HTTP keyword | https://example.com/login | Production / API |
| gateway | Ping | gw.example.com | Staging |
| website-2 | HTTP | https://staging.example.com | Staging |
| a-very-long-monitor-name-that-goes-on | HTTP | https://long.example.com/ |  |
| admin-basic-auth | HTTP | https://admin.example.com/ |  |
| api-v2-health | HTTP | https://api.example.com/v2/health |  |
| billing-db | PostgreSQL | postgres://pg.example.com:5432/billing?sslmode=require |  |
| cache | Redis | redis://cache.example.com:6379 |  |
| core-router | Ping | 10.0.0.1 |  |
| cron-heartbeat | Push | waits for a push |  |
| dns-default-resolver \* | DNS | example.org |  |
| dns-example.com-a | DNS | example.com |  |
| grpc-users \* | gRPC | users.example.com:443 |  |
| ipv6-web-port | TCP port | [2001:db8::10]:443 |  |
| mail-mx | DNS | example.com |  |
| mail-server-smtp \* | TCP port | mail.example.com:25 |  |
| nightly-backup | Push | waits for a push |  |
| old-site-paused \* | HTTP | https://old.example.com/ |  |
| orders-db | MySQL | mysql://db.example.com:3306/orders |  |
| private-api-returns-401 | HTTP | https://api.example.com/private |  |
| redis-container | Docker | container redis |  |
| shop-tls | HTTP | https://shop.example.com/ |  |
| ssh-bastion | TCP port | 10.0.0.5:22 |  |
| switch-uptime-snmp \* | SNMP | 10.0.0.2:161 |  |
| tailscale-node \* | Ping | node1 |  |
| webhook-receiver-post | HTTP | https://hooks.example.com/in |  |

## Also imported

| Name | What | Details |
|---|---|---|
| dingtalk-bot | channel (DingTalk) |  |
| discord-alerts | channel (Discord) |  |
| feishu-bot | channel (Feishu) |  |
| generic-webhook \* | channel (Webhook) | https://hooks.example.com/… |
| ntfy-phone | channel (ntfy) | topic kuma-fake |
| ops-email \* | channel (Email) | to ops@example.com,lead@example.com |
| ops-telegram | channel (Telegram) | chat -1001234567 |
| team-slack \* | channel (Slack) |  |
| webhook-custom-body | channel (Webhook) | https://hooks.example.com/… |
| wecom-bot | channel (WeCom) |  |
| notify-dingtalk-bot-feishu-bot | policy | sends to dingtalk-bot, feishu-bot |
| notify-discord-alerts | policy | sends to discord-alerts |
| notify-generic-webhook | policy | sends to generic-webhook |
| notify-ntfy-phone-ops-telegram | policy | sends to ntfy-phone, ops-telegram |
| notify-ops-email | policy | sends to ops-email |
| notify-ops-telegram-team-slack | policy | sends to ops-telegram, team-slack |
| notify-team-slack | policy | sends to team-slack |
| notify-webhook-custom-body | policy | sends to webhook-custom-body |
| notify-wecom-bot | policy | sends to wecom-bot |
| weekly-patch-window | maintenance (weekly) | sun, 2h |
| public \* | status page | /status/public |
| admin | account | keeps its Uptime Kuma password |

_* needs a look: see below._

## Needs a look (imported)

1. **DNS default resolver (DNS) → dns-default-resolver**  
   Uptime Kuma asked the system's resolver; this check asks 1.1.1.1.  
   To do: Change the target to your own DNS server if that is what you meant to watch.
2. **gRPC users (gRPC) → grpc-users**  
   It uses the standard gRPC health check instead of a keyword match.
3. **Mail server SMTP (TCP port) → mail-server-smtp**  
   Imported as a check that the mail port answers; the SMTP conversation is not checked.
4. **Old site (paused) (HTTP) → old-site-paused**  
   It was paused in Uptime Kuma, so it is imported paused.  
   To do: Resume it when you want it checked.
5. **Switch uptime SNMP (SNMP) → switch-uptime-snmp**  
   It checks that the device answers SNMP; the OID value comparison is not carried over.
6. **Tailscale node (Ping) → tailscale-node**  
   It is pinged directly, not through Tailscale.  
   To do: Run Unagikuma (or a probe node) on a machine in your tailnet if the name only resolves there.
7. **Channel "Team Slack" → team-slack**  
   Messages go to the webhook's own channel; the channel override (#alerts) is not used.
8. **Channel "Generic webhook" → generic-webhook**  
   The message body differs from Uptime Kuma's.  
   To do: Check that the receiving side understands it (Alerting › Channels › Test).
9. **Channel "Ops email" → ops-email**  
   Copy (CC/BCC) addresses are sent to as normal recipients.
10. **Status page "Public status" → public**  
   Its monitors are shown in one list; the sections of the Uptime Kuma page are not kept.

## Not imported

1. **Minecraft server (Game server)**  
   Why: Unagikuma has no game-server query.  
   To do: Check the server's port with a TCP or UDP monitor.
2. **Mongo (MongoDB)**  
   Why: Unagikuma has no MongoDB check.  
   To do: Check port 27017 with a TCP monitor, or run a script check (EXEC).
3. **MQTT sensors (MQTT)**  
   Why: Unagikuma has no MQTT check.  
   To do: Check the broker port with a TCP monitor, or have the device call a push URL (a Push monitor).
4. **MSSQL (SQL Server)**  
   Why: Unagikuma has no SQL Server check.  
   To do: Check port 1433 with a TCP monitor, or run a script check (EXEC).
5. **Old host must be down (HTTP)**  
   Why: It is in upside-down mode (it is up when the service is down). Unagikuma has no such mode, so it would report the opposite.  
   To do: If you still need it, add it by hand as a normal check and decide what “up” should mean.
6. **RabbitMQ (RabbitMQ)**  
   Why: Unagikuma has no RabbitMQ check.  
   To do: Check the management API with an HTTP monitor, or port 5672 with a TCP monitor.
7. **RADIUS (RADIUS)**  
   Why: Unagikuma cannot log in to RADIUS.  
   To do: Check that the server answers with a UDP monitor on port radius (1812).
8. **Steam game server (Steam game server)**  
   Why: Unagikuma has no Steam server query.  
   To do: Check the server's port with a TCP or UDP monitor.
9. **Websocket (WebSocket)**  
   Why: Unagikuma has no WebSocket check.  
   To do: Check the server with an HTTP or TCP monitor.
10. **Group "Empty group"**  
   Why: The group has no monitors that were imported.  
   To do: Nothing to do: groups appear here as soon as a monitor uses them (group=).
11. **Channels: Pushover (Pushover), Teams (Microsoft Teams), Gotify (Gotify), PagerDuty (PagerDuty), Unused Matrix (Matrix)**  
   Why: No imported monitor uses them.  
   To do: Add them under Alerting › Channels if you still need them.
12. **Maintenance "Manual maintenance"**  
   Why: It is switched on and off by hand, which has no schedule to copy. It was on when exported.  
   To do: Silence the monitors (Problems › Silence) or announce the work under Notices & status › Changes.

## Good to know

- Past checks, uptime history and incidents stay in Uptime Kuma: history starts fresh here.
- Credentials (tokens, passwords, webhook addresses) are stored as encrypted secrets, not in the configuration file.

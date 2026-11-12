// Package tunables is the registry of every behaviour threshold that is left
// to operator judgement (notification caps, detection windows, rate
// limits, retention...). Each tunable has a typed default, a valid range and a
// description, and can be overridden globally with the `$SET key=value`
// directive in monitors.conf (and from the Settings page, which writes $SET).
// Policies and monitors may override some of them with their own options.
//
// Rule: never hard-code such a threshold elsewhere — add it here.
package tunables

import (
	"fmt"
	"math"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Kind is the value type of a tunable.
type Kind string

const (
	Int       Kind = "int"
	Float     Kind = "float"
	Duration  Kind = "duration"
	Bool      Kind = "bool"
	TimeOfDay Kind = "time-of-day" // "HH:MM" local time
	Rate      Kind = "rate"        // "N/s", "N/m", "N/h"
	String    Kind = "string"
	Address   Kind = "address"   // an IP address, or "" for none
	Addresses Kind = "addresses" // comma-separated IP addresses, or "" for none
)

// Def describes one tunable.
type Def struct {
	Key     string
	Kind    Kind
	Default string
	Min     string // inclusive, "" = unbounded (numeric / duration kinds)
	Max     string
	Desc    string // en
	DescZH  string
	// Override names the record option that overrides it locally, if any
	// (e.g. POLICY max=, monitor partial-escalate=).
	Override string
}

var defs = []Def{
	// ---- notifications ----
	{Key: "probe.cert-warn-days", Kind: Int, Default: "62", Min: "0", Max: "3650", Desc: "TLS certificates expiring within this many days mark HTTPS and TLS checks DEGRADED unless the monitor sets cert-days (0 = off)", DescZH: "证书在多少天内到期时，HTTPS/TLS 检查显示为性能下降（监控项可用 cert-days 覆盖；0 关闭）"},
	{Key: "probe.cert-critical-days", Kind: Int, Default: "7", Min: "0", Max: "3650", Desc: "TLS certificates expiring within this many days fail HTTPS and TLS checks unless the monitor sets cert-critical-days (0 = off); expired ones always fail", DescZH: "证书在多少天内到期时，HTTPS/TLS 检查判为故障（监控项可用 cert-critical-days 覆盖；0 关闭）；已过期的证书一律判为故障"},
	{Key: "notify.max", Kind: Int, Default: "3", Min: "0", Max: "1000", Desc: "problem notifications per incident before going quiet (0 = unlimited)", DescZH: "每个故障最多发送几条问题通知后封顶（0 = 不限）", Override: "POLICY max="},
	{Key: "notify.repeat", Kind: Duration, Default: "30m", Min: "1m", Max: "7d", Desc: "repeat interval for open, unacknowledged incidents", DescZH: "未确认故障的重复通知间隔", Override: "POLICY repeat="},
	{Key: "notify.delay", Kind: Duration, Default: "0s", Min: "0s", Max: "1h", Desc: "delay before the first notification", DescZH: "首次通知前的延迟", Override: "POLICY delay="},
	{Key: "notify.reopen-window", Kind: Duration, Default: "30m", Min: "0s", Max: "24h", Desc: "a new HARD failure within this window reopens the previous incident", DescZH: "故障重开窗口：恢复后此时间内再次故障则重开原故障", Override: "POLICY reopen-window="},
	{Key: "notify.channel-timeout", Kind: Duration, Default: "10s", Min: "1s", Max: "2m", Desc: "per-channel delivery timeout", DescZH: "单个通道的投递超时"},
	{Key: "notify.channel-retries", Kind: Int, Default: "2", Min: "0", Max: "5", Desc: "extra retries per channel on temporary failure before falling back (0 keeps prior no-retry behaviour)", DescZH: "通道临时失败时的额外重试次数（之后走备援；显式 0 表示不重试）", Override: "CHANNEL retries="},
	{Key: "notify.retry-backoff", Kind: Duration, Default: "2s", Min: "0s", Max: "5m", Desc: "wait before the first channel retry; the second uses 5s when this stays at the default 2s", DescZH: "首次通道重试前的等待；默认 2s 时第二次为 5s"},
	{Key: "notify.channel-rate", Kind: Rate, Default: "20/m", Desc: "per-channel send rate limit", DescZH: "单个通道的发送速率上限", Override: "CHANNEL rate="},
	{Key: "notify.service-limits", Kind: String, Default: "dingtalk=20,wecom=20,telegram=20,feishu=100,discord=30,slack=60", Desc: "messages a minute each chat service takes from one robot or webhook (kind=N, comma separated); a channel never sends more, whatever its rate", DescZH: "各聊天服务对单个机器人或 webhook 每分钟接收的消息数上限（kind=N，逗号分隔）；通道发送不会超过它，无论 rate 设多大"},
	{Key: "notify.retry-after-max", Kind: Duration, Default: "30s", Min: "0s", Max: "10m", Desc: "longest wait a rate-limiting service may ask for that is waited out on the same channel; a longer one sends the message on to the next channel", DescZH: "服务方限流时要求的等待时间不超过此值，就在原通道等待后重试；超过则改走下一个通道"},
	{Key: "notify.failed-retry", Kind: Duration, Default: "2m", Min: "0s", Max: "1h", Desc: "an alert no channel delivered is sent again after this long (doubled each time, up to the repeat interval) and does not count towards notify.max; 0 leaves it to the repeat interval", DescZH: "所有通道都没送达的告警在此时长后重发（每次翻倍，不超过重复通知间隔），且不计入 notify.max；0 表示只按重复通知间隔"},
	{Key: "notify.coalesce-queue", Kind: Int, Default: "10", Min: "1", Max: "1000", Desc: "queued messages per channel before the rest is merged into one digest", DescZH: "通道排队超过多少条后，其余合并为一条摘要"},
	{Key: "notify.group-min", Kind: Int, Default: "3", Min: "2", Max: "1000", Desc: "monitors of one IP segment (or failing to resolve names) that must fail together before a summary blames the network instead of the services", DescZH: "同一网段（或同时域名解析失败）的监控至少多少个一起故障，汇总才判定为网络问题而非业务问题"},
	{Key: "notify.coalesce-wait", Kind: Duration, Default: "2m", Min: "10s", Max: "1h", Desc: "max queue wait before the rest is merged into one digest", DescZH: "排队超过多长时间后，其余合并为一条摘要"},
	{Key: "notify.backstop-max", Kind: Int, Default: "3", Min: "0", Max: "1000", Desc: "alerts one channel gets per backstop window before the rest wait for one digest (0 = off); new incidents and escalations always go through", DescZH: "每个通道在一个窗口内最多收到几条告警，超出的等窗口结束后合并成一条摘要（0 为关闭）；新故障和升级始终立即发送"},
	{Key: "notify.backstop-window", Kind: Duration, Default: "1m", Min: "10s", Max: "1h", Desc: "the window of notify.backstop-max", DescZH: "notify.backstop-max 的统计窗口"},
	{Key: "notify.intent-late", Kind: Duration, Default: "5m", Min: "0s", Max: "24h", Desc: "deliveries later than this are tagged [delayed]", DescZH: "晚于此时长补发的通知加 [延迟] 标记"},
	{Key: "notify.daily-reminder", Kind: Bool, Default: "on", Desc: "daily reminder of still-open incidents", DescZH: "每日提醒仍未解决的故障"},
	{Key: "notify.daily-reminder-at", Kind: TimeOfDay, Default: "10:00", Desc: "local time of the daily open-incident reminder", DescZH: "每日未解决故障提醒的时间"},
	{Key: "notify.daily-report", Kind: Bool, Default: "on", Desc: "daily status report: hosts down and up, services not OK", DescZH: "每日状态日报：哪些主机宕机、哪些正常，哪些服务异常"},
	{Key: "notify.daily-report-at", Kind: TimeOfDay, Default: "10:00", Desc: "local time of the daily status report", DescZH: "每日状态日报的发送时间"},
	{Key: "notify.daily-report-teams", Kind: String, Default: "admin", Desc: "teams receiving the daily report, each on its own monitors (comma-separated)", DescZH: "接收日报的团队（逗号分隔），每个团队收到自己监控的日报"},
	{Key: "notify.daily-report-policy", Kind: String, Default: "", Desc: "policy the daily report is sent through (empty = system policy)", DescZH: "日报使用的通知策略（留空 = 系统策略）"},
	{Key: "notify.daily-report-max-items", Kind: Int, Default: "30", Min: "5", Max: "500", Desc: "entries listed per report section before the rest is counted", DescZH: "日报每节最多列出的条目数，其余只计数"},
	{Key: "notify.degraded-max", Kind: Int, Default: "1", Min: "0", Max: "100", Desc: "notifications per low-priority (DEGRADED/regional) incident", DescZH: "低优先级故障（变慢、区域性故障）的通知上限"},

	// ---- decision engine ----
	{Key: "engine.recover-attempts", Kind: Int, Default: "1", Min: "1", Max: "20", Desc: "consecutive OK rounds needed to recover from HARD", DescZH: "HARD 状态下连续正常几轮才算恢复", Override: "monitor recover-attempts="},
	{Key: "engine.first-round-delay", Kind: Duration, Default: "3s", Min: "0s", Max: "1m", Desc: "first round after a monitor is created or redefined", DescZH: "新建或定义变更后，首轮检查的最长等待"},
	{Key: "engine.round-grace", Kind: Duration, Default: "2s", Min: "0s", Max: "1m", Desc: "extra time after timeout before a round is closed", DescZH: "一轮在超时之外额外等待上报的时间"},
	{Key: "engine.dependency-wait", Kind: Duration, Default: "30s", Min: "0s", Max: "10m", Desc: "how long a failed monitor waits for its parents to be re-checked before it notifies", DescZH: "监控项失败后，等待其父监控项复查的最长时间，超时后照常通知"},
	{Key: "engine.partial-escalate", Kind: Duration, Default: "30m", Min: "0s", Max: "7d", Desc: "no effect since 2.2: every site alerts on its own; kept so older settings load", DescZH: "自 2.2 起不再生效：每个站点各自告警；保留只为兼容旧设置", Override: "monitor partial-escalate="},
	{Key: "engine.pending-warn", Kind: Int, Default: "50", Min: "1", Max: "100000", Desc: "warn when one change sends more monitors back to PENDING", DescZH: "一次变更导致多少个监控项进入 PENDING 时提示"},
	{Key: "engine.degraded-attempts", Kind: Int, Default: "0", Min: "0", Max: "20", Desc: "consecutive slow rounds before DEGRADED (0 = use attempts)", DescZH: "连续几轮变慢判为 DEGRADED（0 = 与 attempts 相同）"},

	// ---- single machine: local network anomaly ----
	{Key: "localnet.enabled", Kind: Bool, Default: "on", Desc: "local network anomaly detection", DescZH: "本机网络异常检测（断网时暂停判定）", Override: "NODE local-detect="},
	{Key: "localnet.min-domains", Kind: Int, Default: "3", Min: "2", Max: "1000", Desc: "independent failure domains (IPv4 /24, IPv6 /48, registrable domain) that must fail before the uplink is judged down; fewer wan domains = detection off", DescZH: "判定监控机外网中断至少需要的独立故障域数（IPv4 /24、IPv6 /48、同一注册域名）；外网故障域少于此数时不做自动判定"},
	{Key: "localnet.domain-ttl", Kind: Duration, Default: "10m", Min: "1m", Max: "24h", Desc: "a wan failure domain counts as available evidence when it was probed within this time", DescZH: "外网故障域在此时长内被探测过才计入可用证据"},
	{Key: "localnet.dns-min-names", Kind: Int, Default: "2", Min: "1", Max: "100", Desc: "distinct names failing resolution (while the network works) before the local resolver is judged broken", DescZH: "网络正常但至少这么多个不同域名解析失败时，判定本机 DNS 异常"},
	{Key: "localnet.window", Kind: Duration, Default: "60s", Min: "10s", Max: "15m", Desc: "window in which any wan network-layer success vetoes the uplink-down judgement", DescZH: "否决窗口：窗口内任一外网目标网络层成功即否决断网判定"},
	{Key: "localnet.gate-hosts", Kind: Int, Default: "5", Min: "1", Max: "50", Desc: "recently healthy wan targets re-checked before a network-class failure turns HARD", DescZH: "转 HARD 前即时自检重测的近期正常外网目标数"},
	{Key: "localnet.gate-timeout", Kind: Duration, Default: "3s", Min: "500ms", Max: "30s", Desc: "timeout of the pre-HARD self-check probes", DescZH: "转 HARD 前即时自检的探测超时"},
	{Key: "localnet.gate-cache", Kind: Duration, Default: "30s", Min: "0s", Max: "5m", Desc: "reuse a pre-HARD self-check result for this long", DescZH: "即时自检结果的复用时长"},
	{Key: "localnet.exit-ratio", Kind: Float, Default: "0.5", Min: "0.1", Max: "1", Desc: "share of wan targets (healthy before the outage) that must recover to exit", DescZH: "退出判定：断网前正常的外网目标中恢复的占比"},
	{Key: "localnet.exit-windows", Kind: Int, Default: "2", Min: "1", Max: "20", Desc: "consecutive healthy windows required to exit", DescZH: "退出判定需要连续满足的窗口数"},
	{Key: "localnet.backdate", Kind: Duration, Default: "2m", Min: "0s", Max: "15m", Desc: "failures that began this long before a local outage was recognised, and that nobody was told about yet, are attributed to it", DescZH: "本机断网被识别前多长时间内开始、且尚未通知任何人的故障，归入这次断网"},
	{Key: "localnet.max-hold", Kind: Duration, Default: "15m", Min: "0s", Max: "24h", Desc: "a local outage longer than this stops holding monitors back, in case the judgement is wrong (0 = never)", DescZH: "本机断网超过此时长后不再压住监控告警（以防误判），0 表示不限"},
	{Key: "localnet.recheck-spread", Kind: Duration, Default: "10s", Min: "0s", Max: "5m", Desc: "after a local outage, the re-checks of the monitors it hid are spread over this time", DescZH: "断网恢复后，对被压住的监控的复检分散在这段时间内进行"},
	{Key: "localnet.notify-after", Kind: Duration, Default: "5m", Min: "0s", Max: "24h", Desc: "outages shorter than this are recorded but not notified", DescZH: "短于此时长的断网只记事件、不发通知"},
	{Key: "localnet.anchor-interval", Kind: Duration, Default: "15s", Min: "5s", Max: "5m", Desc: "anchor (gateway) check interval", DescZH: "锚点（路由器）检查间隔"},
	{Key: "localnet.anchor-fails", Kind: Int, Default: "3", Min: "1", Max: "20", Desc: "consecutive anchor failures before the path is considered down", DescZH: "锚点连续失败几次才判定该路径中断"},
	{Key: "localnet.anchor-recovers", Kind: Int, Default: "2", Min: "1", Max: "20", Desc: "consecutive anchor successes before the path is considered up again", DescZH: "锚点连续成功几次才判定恢复"},
	{Key: "localnet.anchor-enable", Kind: Duration, Default: "2m", Min: "10s", Max: "1h", Desc: "a gateway that never answered within this time is not used as an anchor", DescZH: "启动后此时长内从未应答的路由器不作锚点"},
	{Key: "engine.handler-timeout", Kind: Duration, Default: "30s", Min: "1s", Max: "10m", Desc: "how long an event handler may run before it is killed", DescZH: "事件处理脚本最长运行时间，超时即终止"},
	{Key: "localnet.references", Kind: Addresses, Default: "223.5.5.5,1.1.1.1,8.8.8.8", Desc: "internet reference points, in different networks, asked on TCP 443 whenever an internet target fails: if any answers this host's network works and the failure is the target's; if none does, together with the targets they show this host's own uplink is down (empty = off)", DescZH: "互联网参照点（不同网络中的地址）：有外网目标失败时连它们的 TCP 443；任何一个能连上就说明本机网络正常、是目标自己的故障；全部连不上时，和失败的目标一起证明本机外网断了（留空 = 关闭）"},
	{Key: "localnet.exit-target", Kind: Address, Default: "8.8.8.8", Desc: "internet address pinged, once an internet target fails, to show on the path view whether this host gets out to the internet; display only, it never holds alerts (empty = off)", DescZH: "外网网关探测地址：有外网目标失败时才 ping，在路径图上显示本机能否出外网；只用于显示，不参与告警抑制（留空 = 关闭）"},

	// ---- single machine: suspend / stall / abnormal exit ----
	{Key: "suspend.threshold", Kind: Duration, Default: "10s", Min: "2s", Max: "1h", Desc: "suspend time (boot clock minus monotonic clock) treated as a host sleep", DescZH: "挂起时长（含休眠时钟减不含休眠时钟）超过多少视为主机休眠"},
	{Key: "suspend.stall", Kind: Duration, Default: "3s", Min: "1s", Max: "1m", Desc: "scheduler tick delay treated as a stall (GC, swap, VM pause)", DescZH: "调度节拍延迟超过多少视为卡顿（GC、换页、虚拟机暂停）"},
	{Key: "suspend.tick", Kind: Duration, Default: "1s", Min: "200ms", Max: "10s", Desc: "interval of the clock watchdog that detects suspends and stalls", DescZH: "休眠/卡顿检测的时钟巡检间隔"},
	{Key: "suspend.recheck-spread", Kind: Duration, Default: "30s", Min: "0s", Max: "10m", Desc: "after a resume every monitor is re-checked once, spread over this time", DescZH: "恢复后为每个监控项补测一轮，分散在此时长内发起"},
	{Key: "suspend.overload-min-rounds", Kind: Int, Default: "20", Min: "1", Max: "100000", Desc: "rounds needed in the overload window before the ratio is judged", DescZH: "过载判定窗口内至少需要的轮次数，少于此数不判定"},
	{Key: "suspend.resume-grace", Kind: Duration, Default: "60s", Min: "0s", Max: "10m", Desc: "after resume, network-class failures abstain for this long", DescZH: "恢复后此时长内的网络类失败按弃权处理"},
	{Key: "suspend.notify-after", Kind: Duration, Default: "5m", Min: "0s", Max: "24h", Desc: "interruptions longer than this send a 'monitoring interrupted' notice", DescZH: "中断超过多久发送“监控中断”通知"},
	{Key: "suspend.notify-restart", Kind: Bool, Default: "off", Desc: "also notify on clean restarts", DescZH: "正常重启也发送通知"},
	{Key: "suspend.overload-ratio", Kind: Float, Default: "0.2", Min: "0.01", Max: "1", Desc: "share of dropped rounds in the overload window that raises a 'host overloaded' system incident", DescZH: "过载窗口内被丢弃的轮次占比超过多少，产生“监控机过载”系统故障"},
	{Key: "suspend.overload-window", Kind: Duration, Default: "10m", Min: "1m", Max: "24h", Desc: "window for the overload ratio", DescZH: "过载判定窗口"},

	{Key: "heartbeat.interval", Kind: Duration, Default: "1m", Min: "10s", Max: "1h", Desc: "how often the external heartbeat (dead man's switch) URL is called", DescZH: "外部心跳（死人开关）的调用间隔"},
	{Key: "heartbeat.timeout", Kind: Duration, Default: "10s", Min: "1s", Max: "1m", Desc: "timeout of one external heartbeat call", DescZH: "单次外部心跳调用的超时"},

	// ---- multi-node ----
	{Key: "cluster.node-timeout", Kind: Duration, Default: "60s", Min: "10s", Max: "1h", Desc: "agent disconnected this long is offline", DescZH: "Agent 断开多久判为离线", Override: "NODE node-timeout="},
	{Key: "cluster.startup-grace", Kind: Duration, Default: "15s", Min: "0s", Max: "5m", Desc: "after start, hold minority and node-down notices this long while agents reconnect", DescZH: "启动后这段时间内暂缓少数派/节点离线告警，等待 Agent 重连"},
	{Key: "cluster.standby-sync", Kind: Duration, Default: "5m", Min: "30s", Max: "24h", Desc: "how often standbys fetch a copy of the master's state (also after every configuration change)", DescZH: "备用节点拉取主节点状态副本的间隔（每次配置变更后也会拉取）"},
	{Key: "cluster.standby-retry", Kind: Duration, Default: "30s", Min: "5s", Max: "10m", Desc: "wait before a standby tries again after a failed copy of the master's state (three times at most)", DescZH: "备用节点复制主节点状态失败后，再次尝试前的等待时间（最多三次）"},
	{Key: "cluster.vote-grace", Kind: Duration, Default: "5s", Min: "0s", Max: "1m", Desc: "no effect since 2.2: there are no agents to wait for; kept so older settings load", DescZH: "自 2.2 起不再生效：已没有 Agent；保留只为兼容旧设置"},
	{Key: "cluster.peer-interval", Kind: Duration, Default: "10s", Min: "2s", Max: "5m", Desc: "how often every node checks that it can reach the other nodes (majority node status)", DescZH: "每个节点多久检查一次能否连接其他节点（多数派判断节点状态）"},
	{Key: "cluster.replica-after", Kind: Duration, Default: "1m", Min: "10s", Max: "1h", Desc: "no effect since 2.2: every site checks and alerts on its own at all times; kept so older settings load", DescZH: "自 2.2 起不再生效：每个站点始终独立检查和告警；保留只为兼容旧设置"},
	{Key: "cluster.emergency-after", Kind: Duration, Default: "3m", Min: "30s", Max: "1h", Desc: "no effect since 2.2: there are no agents; a site that loses the master says so itself; kept so older settings load", DescZH: "自 2.2 起不再生效：已没有 Agent，站点联系不上主节点时会自己通知；保留只为兼容旧设置"},
	{Key: "cluster.join-rate", Kind: Rate, Default: "30/m", Desc: "join, rejoin and retire requests, and node requests without a valid token, per client IP (kept apart from sign-ins)", DescZH: "每个来源 IP 的加入、重新加入、退位请求及未带有效令牌的节点请求速率（与登录分开计算）"},
	{Key: "cluster.join-code-ttl", Kind: Duration, Default: "1h", Min: "5m", Max: "7d", Desc: "how long a join code can be used", DescZH: "加入码的有效期"},
	{Key: "cluster.ping-interval", Kind: Duration, Default: "20s", Min: "100ms", Max: "5m", Desc: "how often the master pings each agent; three missed intervals end the session", DescZH: "Master 多久 ping 一次 Agent；连续三个周期没有响应则断开会话"},
	{Key: "cluster.max-message", Kind: Int, Default: "8388608", Min: "65536", Max: "268435456", Desc: "largest agent protocol message in bytes", DescZH: "Agent 协议单条消息的最大字节数"},
	{Key: "cluster.clock-skew", Kind: Duration, Default: "1s", Min: "100ms", Max: "1m", Desc: "clock skew beyond which time-sensitive votes abstain", DescZH: "节点时钟偏差超过多少，时间相关检查的票改为弃权"},

	// ---- probe safety ----
	{Key: "probe.ping-loss", Kind: Int, Default: "50", Min: "0", Max: "100", Desc: "PING checks fail when more than this percentage of their echo requests go unanswered, unless the monitor sets loss= (0 = off: only a total loss fails; any loss still marks the check DEGRADED)", DescZH: "PING 检查中未收到回复的请求占比超过此百分比时判为故障（监控项可用 loss= 覆盖；0 关闭：只有全部丢失才判故障，有丢包仍显示为性能下降）"},
	{Key: "probe.same-target-gap", Kind: Duration, Default: "1s", Min: "0s", Max: "1m", Desc: "minimum time between the start of two pings or weak UDP checks of the same address on a node, so that the target's limit on ICMP replies does not fake loss or open ports (0 = off)", DescZH: "同一节点上对同一地址的两次 ping 或 UDP 弱检查之间的最短间隔，避免目标对 ICMP 回复的限速造成假丢包或把关闭的端口误判为开放（0 关闭）"},
	{Key: "probe.share-window", Kind: Duration, Default: "10s", Min: "0s", Max: "10m", Desc: "a monitor with share=yes uses the result of an identical check that started on the same node within this time instead of checking again (0 = never share)", DescZH: "设置了 share=yes 的监控项，若同一节点在此时间内已开始过完全相同的检查，则直接使用其结果，不再重复探测（0 = 不共用）"},
	{Key: "probe.push-grace", Kind: Duration, Default: "5m", Min: "0s", Max: "7d", Desc: "default grace after a PUSH monitor's period before it is late", DescZH: "PUSH 监控超过周期后的默认宽限时间", Override: "PUSH grace="},
	{Key: "probe.max-concurrency", Kind: Int, Default: "256", Min: "1", Max: "10000", Desc: "concurrent probes per node", DescZH: "每个节点的并发探测上限"},
	{Key: "probe.exec-concurrency", Kind: Int, Default: "8", Min: "1", Max: "256", Desc: "EXEC plugins running at once per node", DescZH: "每个节点同时运行的 EXEC 插件数上限"},
	{Key: "probe.max-pps", Kind: Int, Default: "200", Min: "1", Max: "100000", Desc: "outbound UDP probe packets per second per node", DescZH: "每个节点每秒发出的 UDP 探测包上限"},
	{Key: "probe.reflective-rate", Kind: Rate, Default: "1/s", Desc: "average rate per (target, port, template) for reflective templates", DescZH: "反射类模板对同一目标的平均速率"},
	{Key: "probe.reflective-burst", Kind: Int, Default: "5", Min: "1", Max: "1000", Desc: "burst for reflective templates", DescZH: "反射类模板允许的突发次数"},
	{Key: "probe.stateful-min-interval", Kind: Duration, Default: "60s", Min: "10s", Max: "1h", Desc: "minimum interval (and SOFT retry) for state-creating templates", DescZH: "有状态模板的最小间隔（含 SOFT 重试）"},
	{Key: "probe.check-now-rate", Kind: Duration, Default: "10s", Min: "1s", Max: "10m", Desc: "minimum spacing of manual 'check now' per monitor", DescZH: "同一监控项“立即检查”的最小间隔"},
	{Key: "probe.evidence-keep", Kind: Int, Default: "20", Min: "0", Max: "1000", Desc: "evidence samples kept per monitor and node", DescZH: "每个“监控项 × 节点”保留的报文证据条数"},

	// ---- storage ----
	{Key: "retention.raw", Kind: Duration, Default: "2d", Min: "1d", Max: "366d", Desc: "every round's result (latency points, heartbeat detail); uptime comes from the aggregates", DescZH: "每轮检查结果的保留期（延迟明细、心跳明细）；可用率来自聚合数据"},
	{Key: "retention.agg5m", Kind: Duration, Default: "7d", Min: "1d", Max: "3660d", Desc: "5-minute aggregates", DescZH: "5 分钟聚合保留期"},
	{Key: "retention.agg1h", Kind: Duration, Default: "400d", Min: "7d", Max: "3660d", Desc: "1-hour aggregates", DescZH: "1 小时聚合保留期"},
	{Key: "retention.events", Kind: Duration, Default: "180d", Min: "1d", Max: "3660d", Desc: "events and notification log", DescZH: "事件与通知日志保留期"},
	{Key: "retention.audit", Kind: Duration, Default: "730d", Min: "30d", Max: "3660d", Desc: "audit log of permission and configuration changes: set when starting the server (--audit-retention); a value here only ever makes it longer, so the log cannot be cleared from the web", DescZH: "权限与配置变更审计日志保留期：在启动服务器时设置（--audit-retention）；此处的值只会让它变长，因此无法在网页上清除日志", Override: "--audit-retention"},
	{Key: "retention.snapshots", Kind: Int, Default: "200", Min: "10", Max: "100000", Desc: "config snapshots kept", DescZH: "保留的配置快照数"},
	{Key: "changes.reminders", Kind: String, Default: "24h,1h", Desc: "reminders before an announced change starts, comma separated (empty = none)", DescZH: "已发布的变更开始前提醒的时间点，逗号分隔（留空 = 不提醒）"},
	{Key: "changes.policy", Kind: String, Default: "", Desc: "policy change notices go through when a change names neither a policy nor channels (empty = system policy)", DescZH: "变更未指定策略或通道时，变更通知使用的策略（留空 = 系统策略）"},
	{Key: "oncall.stats-window", Kind: Duration, Default: "30d", Min: "1d", Max: "366d", Desc: "no effect: on-call rotations were removed; kept so old $SET lines stay valid", DescZH: "已无作用：值班表功能已移除；保留此项是为了让旧的 $SET 行仍然有效"},
	{Key: "oncall.shifts-shown", Kind: Int, Default: "8", Min: "1", Max: "60", Desc: "no effect: on-call rotations were removed; kept so old $SET lines stay valid", DescZH: "已无作用：值班表功能已移除；保留此项是为了让旧的 $SET 行仍然有效"},
	{Key: "contacts.bounce-threshold", Kind: Int, Default: "3", Min: "0", Max: "100", Desc: "times an address may be refused as nonexistent (unknown mailbox, invalid number) before it is no longer sent to; 0 keeps sending", DescZH: "地址被判定为不存在（邮箱不存在、号码无效）多少次后停止向它发送；0 表示继续发送"},
	{Key: "contacts.notify-admins", Kind: Bool, Default: "off", Desc: "tell a team's administrators (once a day per channel) when e-mail or SMS recipients are left out because they are not active contacts", DescZH: "收件人因不在通讯录或非在职状态被跳过时，通知团队管理员（每个渠道每天一次）"},
	{Key: "uptime.min-coverage", Kind: Float, Default: "0.1", Min: "0", Max: "1", Desc: "share of a window a monitor must have been watched before its uptime for that window is shown; below it the API gives null and the UI shows the figure so far, greyed, with the coverage", DescZH: "监控在某个时间窗内被观测的时长占比达到多少才算该窗口的可用率；不足时 API 返回 null，界面以灰色显示目前的数值和覆盖率"},
	{Key: "storage.gap-factor", Kind: Float, Default: "2.5", Min: "1.5", Max: "20", Desc: "a pause longer than this many check intervals counts as a gap (excluded from uptime) rather than the last known state", DescZH: "两次检查之间超过这么多个检查间隔的空白，按缺口处理（不计入可用率），而不是沿用上一次的状态"},
	{Key: "storage.write-alert", Kind: Duration, Default: "1m", Min: "10s", Max: "1h", Desc: "writes to the data directory failing this long (full disk, read-only file system) raise a system incident", DescZH: "向数据目录写入持续失败多久（磁盘已满、只读文件系统）后开启系统故障并通知"},
	{Key: "storage.low-disk-ratio", Kind: Float, Default: "0.10", Min: "0.01", Max: "0.5", Desc: "free-disk ratio that triggers retention shrinking", DescZH: "磁盘剩余低于此比例时自动收缩保留期"},
	{Key: "storage.low-disk-raw", Kind: Duration, Default: "1d", Min: "1d", Max: "366d", Desc: "raw retention floor while the disk is low", DescZH: "磁盘不足时原始数据保留期下限"},
	{Key: "storage.commit-interval", Kind: Duration, Default: "2s", Min: "100ms", Max: "5m", Desc: "batched history write interval (the small profile uses 30s)", DescZH: "历史数据批量写入间隔（小硬件档默认 30 秒）"},
	{Key: "storage.state-snapshot", Kind: Duration, Default: "0s", Min: "0s", Max: "1h", Desc: "persist monitor state snapshots at this interval instead of on every change (0 = on change; small profile 5m)", DescZH: "监控状态快照落盘间隔（0 = 变化即写；小硬件档 5 分钟）"},

	// ---- API protection ----
	{Key: "api.rate", Kind: Rate, Default: "20/s", Desc: "API requests per token or client IP", DescZH: "每个令牌或来源 IP 的 API 请求速率"},
	{Key: "api.login-rate", Kind: Rate, Default: "5/m", Desc: "login attempts per client IP", DescZH: "每个来源 IP 的登录尝试速率"},
	{Key: "api.push-rate", Kind: Rate, Default: "60/m", Desc: "push endpoint calls per token", DescZH: "每个 PUSH 令牌的调用速率"},
	{Key: "statuspage.subscribe-rate", Kind: Rate, Default: "5/h", Desc: "status page subscription requests per client IP", DescZH: "每个来源 IP 订阅状态页的请求速率"},
	{Key: "statuspage.max-pending", Kind: Int, Default: "500", Min: "10", Max: "100000", Desc: "unconfirmed subscriptions a status page keeps before refusing more", DescZH: "状态页未确认订阅的上限，超过后拒绝新订阅"},
	{Key: "statuspage.resolved-shown", Kind: Duration, Default: "7d", Min: "0s", Max: "90d", Desc: "how long resolved announcements stay on a status page", DescZH: "已解决的公告在状态页上保留多久"},
	{Key: "api.max-body", Kind: Int, Default: "4194304", Min: "1024", Max: "67108864", Desc: "max request body bytes", DescZH: "请求体大小上限（字节）"},
	{Key: "api.metrics-public", Kind: Bool, Default: "off", Desc: "serve /metrics without an API token (only on trusted networks)", DescZH: "无需 API 令牌即可访问 /metrics（仅限可信网络）"},
	{Key: "api.idempotency-ttl", Kind: Duration, Default: "24h", Min: "1m", Max: "7d", Desc: "how long a create sent with an Idempotency-Key is remembered, so a retried request is not applied twice (kept in memory)", DescZH: "带 Idempotency-Key 的创建请求的记忆时长，重试不会重复执行（保存在内存中）"},
	{Key: "api.page-max", Kind: Int, Default: "1000", Min: "10", Max: "100000", Desc: "largest page a list request may ask for with limit=", DescZH: "列表请求 limit= 可取的最大页大小"},
	{Key: "api.max-sse", Kind: Int, Default: "64", Min: "1", Max: "10000", Desc: "concurrent live (SSE) connections", DescZH: "实时连接（SSE）并发上限"},
	{Key: "statuspage.cache", Kind: Duration, Default: "60s", Min: "0s", Max: "1h", Desc: "status page and badge cache", DescZH: "状态页和徽章缓存时长"},
}

var index = func() map[string]*Def {
	m := make(map[string]*Def, len(defs))
	for i := range defs {
		d := &defs[i]
		if _, dup := m[d.Key]; dup {
			panic("tunables: duplicate " + d.Key)
		}
		if err := d.check(d.Default); err != nil {
			panic(fmt.Sprintf("tunables: bad default for %s: %v", d.Key, err))
		}
		m[d.Key] = d
	}
	return m
}()

// Defs returns all definitions sorted by key.
func Defs() []Def {
	out := append([]Def(nil), defs...)
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Lookup returns a definition.
func Lookup(key string) (*Def, bool) { d, ok := index[key]; return d, ok }

// Set holds overrides on top of the defaults. The zero value is all defaults.
type Set struct{ v map[string]string }

// New validates overrides (from $SET) and returns a Set.
func New(overrides map[string]string) (Set, error) {
	s := Set{v: map[string]string{}}
	for k, val := range overrides {
		if err := s.Put(k, val); err != nil {
			return Set{}, err
		}
	}
	return s, nil
}

// Put validates and stores one override.
func (s *Set) Put(key, val string) error {
	d, ok := index[key]
	if !ok {
		return fmt.Errorf("unknown tunable %q", key)
	}
	if err := d.check(val); err != nil {
		return fmt.Errorf("tunable %s: %w", key, err)
	}
	if s.v == nil {
		s.v = map[string]string{}
	}
	s.v[key] = val
	return nil
}

// Overrides returns the explicit overrides (for rendering $SET).
func (s Set) Overrides() map[string]string {
	out := make(map[string]string, len(s.v))
	for k, v := range s.v {
		out[k] = v
	}
	return out
}

func (s Set) raw(key string) string {
	if v, ok := s.v[key]; ok {
		return v
	}
	d, ok := index[key]
	if !ok {
		panic("tunables: unknown key " + key)
	}
	return d.Default
}

// Raw returns the effective textual value.
func (s Set) Raw(key string) string { return s.raw(key) }

func (s Set) Int(key string) int { n, _ := strconv.Atoi(s.raw(key)); return n }
func (s Set) Float(key string) float64 {
	f, _ := strconv.ParseFloat(s.raw(key), 64)
	return f
}
func (s Set) Duration(key string) time.Duration { d, _ := ParseDuration(s.raw(key)); return d }
func (s Set) Bool(key string) bool              { b, _ := parseBool(s.raw(key)); return b }

// TimeOfDay returns hour and minute.
func (s Set) TimeOfDay(key string) (int, int) { h, m, _ := parseTOD(s.raw(key)); return h, m }

// Rate returns events per second.
func (s Set) Rate(key string) float64 { r, _ := ParseRate(s.raw(key)); return r }

func (d *Def) check(v string) error {
	switch d.Kind {
	case Int:
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("want an integer, got %q", v)
		}
		return bound(float64(n), d.Min, d.Max, func(s string) (float64, error) { x, e := strconv.Atoi(s); return float64(x), e })
	case Float:
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return fmt.Errorf("want a number, got %q", v)
		}
		return bound(f, d.Min, d.Max, func(s string) (float64, error) { return strconv.ParseFloat(s, 64) })
	case Duration:
		x, err := ParseDuration(v)
		if err != nil {
			return err
		}
		return bound(float64(x), d.Min, d.Max, func(s string) (float64, error) { y, e := ParseDuration(s); return float64(y), e })
	case Bool:
		_, err := parseBool(v)
		return err
	case TimeOfDay:
		_, _, err := parseTOD(v)
		return err
	case Rate:
		_, err := ParseRate(v)
		return err
	case String:
		return nil
	case Address:
		if v != "" && net.ParseIP(v) == nil {
			return fmt.Errorf("want an IP address, got %q", v)
		}
		return nil
	case Addresses:
		for _, a := range strings.Split(v, ",") {
			if a = strings.TrimSpace(a); a != "" && net.ParseIP(a) == nil {
				return fmt.Errorf("want IP addresses separated by commas, got %q", a)
			}
		}
		return nil
	}
	return fmt.Errorf("unknown kind %s", d.Kind)
}

func bound(x float64, lo, hi string, parse func(string) (float64, error)) error {
	if lo != "" {
		if l, _ := parse(lo); x < l {
			return fmt.Errorf("below minimum %s", lo)
		}
	}
	if hi != "" {
		if h, _ := parse(hi); x > h {
			return fmt.Errorf("above maximum %s", hi)
		}
	}
	return nil
}

// ParseDuration accepts Go durations, a "d" day unit, combinations such as
// "1d12h", and bare integers meaning seconds.
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	if n, err := strconv.Atoi(s); err == nil {
		if n < 0 || int64(n) > math.MaxInt64/int64(time.Second) {
			return 0, fmt.Errorf("bad duration %q", s)
		}
		return time.Duration(n) * time.Second, nil
	}
	var days time.Duration
	if i := strings.IndexByte(s, 'd'); i > 0 {
		n, err := strconv.Atoi(s[:i])
		if err != nil || n < 0 || int64(n) > maxDays {
			return 0, fmt.Errorf("bad duration %q", s)
		}
		days = time.Duration(n) * 24 * time.Hour
		s = s[i+1:]
		if s == "" {
			return days, nil
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 || d > math.MaxInt64-days {
		return 0, fmt.Errorf("bad duration %q", s)
	}
	return days + d, nil
}

// maxDays keeps n days within a time.Duration (about 292 years).
const maxDays = math.MaxInt64 / int64(24*time.Hour)

// ParseRate parses "N/s", "N/m", "N/h" into events per second.
func ParseRate(s string) (float64, error) {
	n, unit, ok := strings.Cut(strings.TrimSpace(s), "/")
	if !ok {
		return 0, fmt.Errorf("bad rate %q (want N/s, N/m or N/h)", s)
	}
	f, err := strconv.ParseFloat(n, 64)
	if err != nil || !(f > 0) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("bad rate %q", s)
	}
	switch unit {
	case "s":
		return f, nil
	case "m":
		return f / 60, nil
	case "h":
		return f / 3600, nil
	}
	return 0, fmt.Errorf("bad rate unit in %q", s)
}

func parseBool(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "on", "yes", "true", "1":
		return true, nil
	case "off", "no", "false", "0":
		return false, nil
	}
	return false, fmt.Errorf("want on/off, got %q", s)
}

func parseTOD(s string) (int, int, error) {
	h, m, ok := strings.Cut(s, ":")
	hh, e1 := strconv.Atoi(h)
	mm, e2 := strconv.Atoi(m)
	if !ok || e1 != nil || e2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, 0, fmt.Errorf("want HH:MM, got %q", s)
	}
	return hh, mm, nil
}

// Command unagikuma is a self-hosted, single-binary monitoring system.
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/githubflyideas/unagikuma/internal/app"
	"github.com/githubflyideas/unagikuma/internal/cluster"
	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/directory"
	"github.com/githubflyideas/unagikuma/internal/keywords"
	"github.com/githubflyideas/unagikuma/internal/probe"
	"github.com/githubflyideas/unagikuma/internal/server"
	"github.com/githubflyideas/unagikuma/internal/store"
	"github.com/githubflyideas/unagikuma/internal/tunables"
	"github.com/githubflyideas/unagikuma/web"
)

// version is set at build time (-ldflags "-X main.version=...").
var version = "dev"

// baseCtx ends when a Windows service is told to stop.
var baseCtx = context.Background()

const usage = `unagikuma — self-hosted monitoring

Usage:
  unagikuma [server] [flags]       run the monitor and its web UI (default)
  unagikuma node list|revoke       list the machines of a cluster, or disconnect one
  unagikuma check [file]           validate a monitors.conf file (--json for tools)
  unagikuma fmt [--check] [file]   align and normalise monitors.conf (meaning unchanged)
  unagikuma serial [bump] [file]   print or increase the revision
  unagikuma probe <target> [k=v]   run one check now (e.g. "udp 10.0.0.1:ntp")
  unagikuma keywords [dialect]     list the UDP keywords
  unagikuma import kuma FILE       move from Uptime Kuma (kuma.db or a JSON backup); try --dry-run first
  unagikuma import nagios FILE|DIR move from Nagios (nagios.cfg or a folder of .cfg files); try --dry-run first
  unagikuma backup [--history] [--passphrase-file F]
                                  write a backup archive (encrypted, with keys, given a passphrase)
  unagikuma restore ARCHIVE        restore a backup into the data directory
  unagikuma service install|uninstall [--name N] [-- COMMAND ARGS]
                                  run unagikuma as a Windows service
  unagikuma reset-password [user]  set a new password for a user (default admin); --generate makes one
  unagikuma audit verify|export   check the audit log for tampering, or export it (CSV/JSON)
  unagikuma report incident ID     write an incident's report (Markdown, JSON or HTML)
  unagikuma promote [--data DIR]   make this stopped standby the master at its next start
  unagikuma version

Run "unagikuma server -h" or "unagikuma agent -h" for their flags.
`

func main() {
	args := os.Args[1:]
	cmd := "server"
	switch {
	case len(args) > 0 && !strings.HasPrefix(args[0], "-"):
		cmd, args = args[0], args[1:]
	case len(args) > 0 && topLevelFlag[args[0]]:
		// "unagikuma --version" / "--help": the program's, not the server's.
		cmd, args = args[0], args[1:]
	}
	var err error
	// Started by the Windows service manager: run the command as a service.
	if ok, serr := runAsService(func() error { return dispatch(cmd, args) }); ok || serr != nil {
		if serr != nil {
			fmt.Fprintln(os.Stderr, "unagikuma:", serr)
			os.Exit(1)
		}
		return
	}
	err = dispatch(cmd, args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "unagikuma:", err)
		os.Exit(1)
	}
}

// topLevelFlag are the flags that are commands of the program itself.
var topLevelFlag = map[string]bool{"-v": true, "-version": true, "--version": true, "-h": true, "-help": true, "--help": true}

// dispatch runs one command.
func dispatch(cmd string, args []string) error {
	var err error
	switch cmd {
	case "server":
		err = runServer(args)
	case "agent":
		err = runAgent(args)
	case "node":
		err = runNode(args, os.Stdout)
	case "check":
		err = runCheck(args)
	case "fmt":
		err = runFmt(args)
	case "serial":
		err = runSerial(args)
	case "probe":
		err = runProbe(args)
	case "keywords":
		err = runKeywords(args)
	case "backup":
		err = runBackup(args)
	case "restore":
		err = runRestore(args)
	case "service":
		err = runService(args)
	case "import":
		err = runImport(args)
	case "reset-password":
		err = runResetPassword(args)
	case "promote":
		err = runPromote(args)
	case "audit":
		err = runAudit(args)
	case "report":
		err = runReport(args)
	case "version", "-v", "-version", "--version":
		fmt.Println("unagikuma", version)
	case "help", "-h", "-help", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	return err
}

func defaultDataDir() string {
	if v := os.Getenv("UNAGIKUMA_DATA"); v != "" {
		return v
	}
	return "data"
}

// setScriptDirs chooses where EXEC checks find their scripts. Only the
// person starting the process decides this; the configuration cannot. The
// first directory is created on start with a README and an example, so
// there is an obvious place to copy scripts to.
func setScriptDirs(flagValue, def string, log *slog.Logger) {
	dirs := []string{def}
	if flagValue != "" {
		dirs = strings.Split(flagValue, ",")
	}
	probe.SetPluginDirs(dirs)
	if err := probe.InitScriptDir(strings.TrimSpace(dirs[0])); err != nil && log != nil {
		log.Warn("check-script directory", "dir", dirs[0], "err", err)
	}
}

// agentScriptDir sits next to the agent token the install commands write.
func agentScriptDir() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(os.Getenv("ProgramData"), "unagikuma", "check-scripts")
	}
	return "/etc/unagikuma/check-scripts"
}

func runServer(args []string) error {
	fs := flag.NewFlagSet("server", flag.ExitOnError)
	data := fs.String("data", defaultDataDir(), "data directory (databases, key, monitors.conf)")
	cfgPath := fs.String("config", "", "path of monitors.conf (default <data>/monitors.conf)")
	listen := fs.String("listen", envOr("UNAGIKUMA_LISTEN", ":8077"), "listen address")
	certFile := fs.String("tls-cert", "", "TLS certificate file")
	keyFile := fs.String("tls-key", "", "TLS key file")
	instance := fs.String("instance", "", "instance name shown in notifications (default: host name)")
	baseURL := fs.String("base-url", "", "public URL of the web UI, for links in notifications")
	heartbeat := fs.String("heartbeat-url", "", "external heartbeat URL called every minute (dead man's switch)")
	allowEnv := fs.String("allow-env", "", "comma-separated environment variables usable as ${env:NAME}")
	proxies := fs.String("trusted-proxy", "", "comma-separated CIDRs of reverse proxies whose X-Forwarded-For is trusted")
	auditRetention := fs.String("audit-retention", os.Getenv("UNAGIKUMA_AUDIT_RETENTION"), "keep the audit log at least this long, 30d to 3660d (default 730d); the web UI can only make it longer")
	logLevel := fs.String("log-level", "info", "debug, info, warn or error")
	logFile := fs.String("log-file", os.Getenv("UNAGIKUMA_LOG_FILE"), "append logs to this file instead of stderr (for services)")
	scripts := fs.String("script-dir", os.Getenv("UNAGIKUMA_SCRIPT_DIR"), "comma-separated directories of check scripts for EXEC checks (default <data>/check-scripts)")
	_ = fs.Parse(args)

	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(*logLevel)); err != nil {
		return err
	}
	log, err := newLogger(*logFile, lvl)
	if err != nil {
		return err
	}
	// Take the port first: when it is busy, nothing is created yet.
	raw, err := net.Listen("tcp", *listen)
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			return fmt.Errorf("port %s is already in use (is Unagikuma already running?): stop the other program or choose another address with --listen", *listen)
		}
		return err
	}
	defer raw.Close()
	setScriptDirs(*scripts, filepath.Join(*data, "check-scripts"), log)
	var env []string
	if *allowEnv != "" {
		env = strings.Split(*allowEnv, ",")
	}
	auditKeep, err := parseAuditRetention(*auditRetention)
	if err != nil {
		return err
	}
	a, err := app.New(app.Options{
		DataDir: *data, ConfigPath: *cfgPath, Instance: *instance, BaseURL: *baseURL, HeartbeatURL: *heartbeat,
		AllowEnv: env, Log: log, Version: version, Listen: *listen, AuditRetention: auditKeep,
	})
	if err != nil {
		var ie *app.InvalidError
		if errors.As(err, &ie) {
			for _, d := range ie.Diags {
				fmt.Fprintln(os.Stderr, d.Error())
			}
		}
		return err
	}
	trusted, err := parseCIDRs(*proxies)
	if err != nil {
		return err
	}
	srv := server.New(a, server.Options{Log: log, Web: web.FS(), TrustedProxy: trusted, SecureCookies: *certFile != ""})
	defer srv.Close()
	if tok := srv.SetupToken(); tok != "" {
		fmt.Fprintf(os.Stderr, "\n  First run: open the web UI to create the admin account.\n  From another machine you need this setup token: %s\n\n", tok)
	}

	ctx, stop := signal.NotifyContext(baseCtx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	// One port serves the UI over plain HTTP or TLS and agents over TLS.
	// With a certificate of its own, plain requests are sent to https.
	var uiCert *tls.Certificate
	handler := srv.Handler()
	if *certFile != "" {
		c, err := tls.LoadX509KeyPair(*certFile, *keyFile)
		if err != nil {
			return err
		}
		uiCert = &c
		handler = redirectPlain(handler)
	}
	hs := &http.Server{
		Addr: *listen, Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second,
		ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	ln := cluster.SniffListener(raw, a.PKI().TLSConfig(uiCert), 10*time.Second)
	log.Info("unagikuma started", "version", version, "listen", raw.Addr().String(), "data", *data, "config", a.ConfigPath(),
		"agent_ca", a.PKI().Fingerprint())
	if *certFile == "" {
		log.Warn("UI HTTP is plaintext on the listen address; put a TLS reverse proxy in front or pass --tls-cert/--tls-key. Agent traffic already uses pinned TLS on the same port via protocol sniffing. Prefer an internal network; do not expose :8077 to the public Internet without authentication and TLS.")
	}
	errc := make(chan error, 1)
	go func() { errc <- hs.Serve(ln) }()
	runDone := make(chan error, 1)
	go func() { runDone <- a.Run(ctx) }()
	restart := false
	select {
	case err := <-errc:
		stop()
		<-runDone
		return err
	case <-ctx.Done():
	case <-a.RestartRequested():
		restart = true
		stop()
	}
	log.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = hs.Shutdown(sctx)
	err = <-runDone
	if restart {
		srv.Close()
		log.Info("restarting to take over as master")
		return reexec()
	}
	return err
}

// redirectPlain sends plain HTTP requests to the same URL over https.
func redirectPlain(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil {
			http.Redirect(w, r, "https://"+r.Host+r.URL.RequestURI(), http.StatusPermanentRedirect)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func runAgent(args []string) error {
	fs := flag.NewFlagSet("agent", flag.ExitOnError)
	serverURL := fs.String("server", os.Getenv("UNAGIKUMA_SERVER"), "master URL, e.g. https://master:8077")
	node := fs.String("node", "", "node name (checked against the token)")
	tokenFile := fs.String("token-file", "", "file holding the agent token (or set UNAGIKUMA_AGENT_TOKEN)")
	token := fs.String("token", "", "agent token (visible in ps and shell history; prefer --token-file)")
	fingerprint := fs.String("ca-fingerprint", os.Getenv("UNAGIKUMA_CA_FINGERPRINT"), "pinned fingerprint of the master's cluster CA (sha256:...)")
	insecure := fs.Bool("insecure-skip-verify", false, "accept any master certificate (NOT recommended)")
	emergency := fs.String("emergency-webhook", os.Getenv("UNAGIKUMA_EMERGENCY_WEBHOOK"), "notify this URL (or KIND:URL, e.g. feishu:https://...) when the master stays unreachable")
	lang := fs.String("lang", "en", "language of emergency notifications")
	logLevel := fs.String("log-level", "info", "debug, info, warn or error")
	logFile := fs.String("log-file", os.Getenv("UNAGIKUMA_LOG_FILE"), "append logs to this file instead of stderr (for services)")
	scripts := fs.String("script-dir", os.Getenv("UNAGIKUMA_SCRIPT_DIR"), "comma-separated directories of check scripts for EXEC checks (default "+agentScriptDir()+")")
	_ = fs.Parse(args)
	if fs.Parsed() {
		return errors.New("probe-only agents cannot provide independent site alerts; run a full server with its own data directory, then join it through cluster setup")
	}

	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(*logLevel)); err != nil {
		return err
	}
	log, err := newLogger(*logFile, lvl)
	if err != nil {
		return err
	}
	setScriptDirs(*scripts, agentScriptDir(), log)
	tok := os.Getenv("UNAGIKUMA_AGENT_TOKEN")
	switch {
	case *tokenFile != "":
		b, err := os.ReadFile(*tokenFile)
		if err != nil {
			return err
		}
		tok = strings.TrimSpace(string(b))
	case *token != "":
		log.Warn("--token is visible to other users in the process list and stays in shell history; use --token-file")
		tok = *token
	}
	if *serverURL == "" {
		return errors.New("--server is required")
	}
	if *insecure {
		log.Warn("--insecure-skip-verify: the master's identity is NOT checked; anyone on the path can impersonate it and read the secrets it sends")
	}
	em, err := cluster.ParseEmergency(*emergency)
	if err != nil {
		return err
	}
	ag, err := cluster.NewAgent(cluster.AgentOptions{
		Server: *serverURL, Node: *node, Token: tok, Fingerprint: *fingerprint, Insecure: *insecure,
		Version: version, Log: log, Emergency: em, Lang: *lang,
	})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(baseCtx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Info("unagikuma agent started", "version", version, "server", *serverURL, "node", *node)
	return ag.Run(ctx)
}

// newLogger logs to stderr, or appends to file.
func newLogger(file string, lvl slog.Level) (*slog.Logger, error) {
	w := io.Writer(os.Stderr)
	if file != "" {
		f, err := os.OpenFile(file, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
		if err != nil {
			return nil, err
		}
		w = f
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: lvl})), nil
}

// parseAuditRetention reads --audit-retention: empty is the default.
func parseAuditRetention(v string) (time.Duration, error) {
	if v == "" {
		return 0, nil
	}
	d, err := tunables.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("--audit-retention: %v", err)
	}
	if d < 30*24*time.Hour || d > 3660*24*time.Hour {
		return 0, fmt.Errorf("--audit-retention: %s is outside 30d to 3660d", v)
	}
	return d, nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func parseCIDRs(s string) (func(net.IP) bool, error) {
	if s == "" {
		return nil, nil
	}
	var nets []*net.IPNet
	for _, c := range strings.Split(s, ",") {
		_, n, err := net.ParseCIDR(strings.TrimSpace(c))
		if err != nil {
			return nil, fmt.Errorf("--trusted-proxy: %v", err)
		}
		nets = append(nets, n)
	}
	return func(ip net.IP) bool {
		for _, n := range nets {
			if n.Contains(ip) {
				return true
			}
		}
		return false
	}, nil
}

func configFile(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	return config.PathIn(defaultDataDir())
}

func runCheck(args []string) error {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	strict := fs.Bool("strict", false, "treat warnings as errors")
	asJSON := fs.Bool("json", false, "print the diagnostics as JSON (file, line, col, severity, code, message)")
	data := fs.String("data", "", "data directory whose contact directory the channel recipients are checked against (default: the file's directory, if it has one)")
	_ = fs.Parse(args)
	path := configFile(fs.Args())
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	opts := config.Options{
		ValidateMonitor: func(m *config.Monitor) error {
			s, err := app.ProbeSpec(m, nil)
			if err != nil || strings.Contains(s.Target, "${") {
				return err
			}
			return probe.Validate(s)
		},
		ValidateChannel: app.ValidateChannel,
	}
	_, cfg, diags := config.Load(path, src, opts)
	diags = append(diags, contactDiags(cfg, path, *data)...)
	bad := false
	for _, d := range diags {
		if !*asJSON {
			fmt.Println(d.Error())
		}
		if d.Severity == config.SevError || *strict {
			bad = true
		}
	}
	if *asJSON {
		type diagJSON struct {
			File     string `json:"file"`
			Line     int    `json:"line"`
			Col      int    `json:"col"`
			Severity string `json:"severity"`
			Code     string `json:"code"`
			Message  string `json:"message"`
		}
		out := []diagJSON{}
		for _, d := range diags {
			out = append(out, diagJSON{path, d.Pos.Line, d.Pos.Col, d.Severity, d.Code, d.Msg})
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
	}
	if bad {
		return errors.New("check failed")
	}
	fmt.Printf("%s: revision %d, %d monitors, %d channels, %d policies — ok\n", path, cfg.Serial, len(cfg.Monitors), len(cfg.Channels), len(cfg.Policies))
	return nil
}

// runFmt rewrites monitors.conf in the canonical layout; --check only
// reports whether it would change (exit status 1), for CI.
func runFmt(args []string) error {
	fs := flag.NewFlagSet("fmt", flag.ExitOnError)
	check := fs.Bool("check", false, "report whether the file needs formatting, change nothing")
	_ = fs.Parse(args)
	path := configFile(fs.Args())
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if _, diags := config.Parse(path, src); config.HasErrors(diags) {
		return fmt.Errorf("%s has errors, fix them first: %v", path, diags[0])
	}
	out := config.Format(src)
	if string(out) == string(src) {
		return nil
	}
	if *check {
		return fmt.Errorf("%s is not formatted (run unagikuma fmt)", path)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "%s formatted; a running server applies it once the revision is increased (unagikuma serial bump)\n", path)
	return nil
}

func runSerial(args []string) error {
	bump := len(args) > 0 && args[0] == "bump"
	if bump {
		args = args[1:]
	}
	path := configFile(args)
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	f, diags := config.Parse(path, src)
	if config.HasErrors(diags) {
		return diags[0]
	}
	cur, err := f.Serial()
	if err != nil {
		return err
	}
	if !bump {
		fmt.Println(cur)
		return nil
	}
	next, warn, err := config.NextSerial(cur, time.Now())
	if err != nil {
		return err
	}
	if warn != "" {
		fmt.Fprintln(os.Stderr, "warning:", warn)
	}
	if err := f.SetSerial(next); err != nil {
		return err
	}
	if err := os.WriteFile(path, f.Bytes(), 0o640); err != nil {
		return err
	}
	fmt.Printf("%d → %d\n", cur, next)
	return nil
}

func runProbe(args []string) error {
	if len(args) == 0 {
		return errors.New(`usage: unagikuma probe <target> [k=v...] (e.g. "https://example.com", "udp 10.0.0.1:ntp", "tcp db:5432")`)
	}
	line := strings.Join(args, " ")
	src := "$FILE revision=1 master=probe attempts=1\n" + quickLine(line) + "\n"
	_, cfg, diags := config.Load("probe", []byte(src), config.Options{})
	for _, d := range diags {
		if d.Severity == config.SevError {
			return fmt.Errorf("%s", d.Msg)
		}
		fmt.Fprintln(os.Stderr, "warning:", d.Msg)
	}
	m := cfg.Monitors[0]
	spec, err := app.ProbeSpec(m, nil)
	if err != nil {
		return err
	}
	res := probe.Run(context.Background(), spec)
	fmt.Printf("%s %s → %s (%s) in %s, depth %s\n%s\n", m.Type, spec.Target, res.Vote, res.Reason, res.Latency.Round(time.Microsecond), res.Depth, res.Detail)
	if ev := res.Evidence; ev != nil {
		for _, f := range ev.Decoded {
			fmt.Printf("  %-12s %s\n", f.Name, f.Value)
		}
	}
	if len(res.Metrics) > 0 {
		keys := make([]string, 0, len(res.Metrics))
		for k := range res.Metrics {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Printf("  %-16s %.3f\n", k, res.Metrics[k])
		}
	}
	if res.Vote == probe.VoteFail {
		os.Exit(1)
	}
	return nil
}

// quickLine turns "udp host:ntp k=v" / "https://x" into a record line.
func quickLine(line string) string {
	f := strings.Fields(line)
	typ, rest := "", f
	switch strings.ToLower(f[0]) {
	case "http", "https", "tcp", "udp", "ping", "dns", "ntp", "snmp", "syslog":
		typ, rest = strings.ToUpper(f[0]), f[1:]
		if typ == "HTTPS" {
			typ = "HTTP"
		}
	}
	if typ == "" {
		t := f[0]
		switch {
		case strings.Contains(t, "://"):
			typ = "HTTP"
		case strings.Contains(t, ":"):
			if _, p, err := net.SplitHostPort(t); err == nil && strings.Trim(p, "0123456789") == "" {
				typ = "TCP"
			} else {
				typ = "UDP"
			}
		default:
			typ = "PING"
		}
	}
	if len(rest) > 0 && typ == "HTTP" && !strings.Contains(rest[0], "://") {
		rest[0] = "https://" + rest[0]
	}
	return "probe IN " + typ + " " + strings.Join(rest, " ")
}

func runKeywords(args []string) error {
	d := keywords.DialectAuto
	if len(args) > 0 {
		var err error
		if d, err = keywords.ParseDialect(args[0]); err != nil {
			return err
		}
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
	fmt.Fprintln(tw, "KEYWORD\tPORT\tDEPTH\tSERVICE\tNOTE")
	for _, e := range keywords.All() {
		port := fmt.Sprint(e.Port)
		if r, err := keywords.Resolve(e.Keyword, d); err == nil {
			port = fmt.Sprint(r.Port)
		} else if e.Keyword == "dnsix" {
			port = "90/195"
		}
		note := e.Note
		if e.Suggest != "" {
			note = e.Suggest
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", e.Keyword, port, e.EffectiveDepth(probe.Built(e.Template)), e.Service, note)
	}
	return tw.Flush()
}

func readLine(r io.Reader) (string, error) {
	var b strings.Builder
	buf := make([]byte, 1)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if buf[0] == '\n' {
				break
			}
			b.WriteByte(buf[0])
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return "", err
		}
	}
	return strings.TrimRight(b.String(), "\r"), nil
}

// contactDiags warns about channel recipients the contact directory flags
// (someone who left, a bouncing or unknown address), when the data
// directory can be read.
func contactDiags(cfg *config.Config, path, data string) []config.Diagnostic {
	if cfg == nil {
		return nil
	}
	if data == "" {
		data = filepath.Dir(path)
	}
	if _, err := os.Stat(filepath.Join(data, "core.db")); err != nil {
		return nil
	}
	st, err := store.Open(data, store.Options{Tool: true})
	if err != nil {
		return nil
	}
	defer st.Close()
	people, err1 := st.People()
	bounces, err2 := st.Bounces()
	if err1 != nil || err2 != nil {
		return nil
	}
	d := directory.New(people, bounces, cfg.Tunables.Int("contacts.bounce-threshold"))
	var out []config.Diagnostic
	for _, ch := range cfg.Channels {
		key, kind, ok := directory.AddressKey(ch.Kind)
		if !ok || strings.Contains(ch.Opts[key], "${") {
			continue
		}
		_, issues := d.Resolve(kind, ch.Opts[key])
		for _, i := range issues {
			msg := fmt.Sprintf("channel %s: %s", ch.Name, i.Code)
			if i.Address != "" {
				msg += " " + i.Address
			}
			if i.Person != "" {
				msg += " (@" + i.Person + ")"
			}
			out = append(out, config.Diagnostic{Pos: ch.Pos, Severity: config.SevWarning, Code: "contact_" + i.Code, Msg: msg})
		}
	}
	return out
}

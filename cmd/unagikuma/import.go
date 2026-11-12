package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/app"
	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/importer"
	"github.com/githubflyideas/unagikuma/internal/store"
)

const importUsage = `Move monitors from another tool into Unagikuma.

Usage:
  unagikuma import kuma   [flags] kuma.db|backup.json
  unagikuma import nagios [flags] nagios.cfg|DIR|FILE.cfg...
  unagikuma import checkmate [flags] monitors.json
  unagikuma import gatus [flags] config.yaml

  checkmate  bluewave-labs/Checkmate Settings export (HTTP subset).
  gatus      Gatus endpoints YAML (literal HTTP status condition subset).
             Unsupported items block apply; see docs/import-adapters.md.

  kuma     Uptime Kuma's database (data/kuma.db; stop Uptime Kuma first)
           or a backup exported from Uptime Kuma 1.x (Settings › Backup).
  nagios   Nagios's main file (nagios.cfg, its cfg_file= and cfg_dir= are
           followed), or object files and directories.

Flags:
  --dry-run        show what would be imported; change nothing
  --data DIR       Unagikuma's data directory (default ./data or $UNAGIKUMA_DATA)
  --area SECTION   put the monitors in this section, e.g. region:north
  --role ROLE      role for Uptime Kuma's accounts: admin, operator, viewer
  --report DIR     where to save the report (default <data>/import-reports)
  --config FILE    the configuration file (default <data>/monitors.conf)
  --width N        report width (default: the terminal's)

Run it with --dry-run first. Existing monitors are never changed: checks that
are already here are skipped, so running it twice is safe.

Exit status: 0 done (or nothing new), 1 failed (nothing was changed), 2 wrong usage.
`

// runImport converts another tool's data into monitors.conf records,
// secrets and accounts, and writes a report.
func runImport(args []string) error {
	j, err := parseImportArgs(args)
	if err != nil || j == nil {
		return err
	}
	return j.run()
}

// importJob is one run of "unagikuma import", step by step.
type importJob struct {
	source, data, cfgPath, area, role, reportDir string
	dry                                          bool
	width                                        int
	inputs                                       []string

	adapterData []byte
	src         []byte
	fresh       bool
	file        *config.File
	cfg         *config.Config
	st          *store.Store
	sec         *store.Secrets
	extra       []string
	firstAdmin  string
	newUsers    []importer.KumaUser
}

// parseImportArgs reads the command line; a nil job means usage was shown.
func parseImportArgs(args []string) (*importJob, error) {
	if len(args) == 0 || (args[0] != "kuma" && args[0] != "nagios" && args[0] != "gatus" && args[0] != "checkmate") {
		if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
			fmt.Print(importUsage)
			return nil, nil
		}
		fmt.Fprint(os.Stderr, importUsage)
		os.Exit(2)
	}
	j := &importJob{source: args[0]}
	fs := flag.NewFlagSet("import "+j.source, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&j.data, "data", defaultDataDir(), "")
	fs.StringVar(&j.cfgPath, "config", "", "")
	fs.BoolVar(&j.dry, "dry-run", false, "")
	fs.StringVar(&j.area, "area", "", "")
	fs.StringVar(&j.role, "role", store.RoleOperator, "")
	fs.StringVar(&j.reportDir, "report", "", "")
	fs.IntVar(&j.width, "width", 0, "")
	if err := fs.Parse(interleavedFlags(args[1:], map[string]bool{"data": true, "config": true, "area": true, "role": true, "report": true, "width": true})); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Print(importUsage)
			return nil, nil
		}
		fmt.Fprintf(os.Stderr, "unagikuma import: %v\n\n%s", err, importUsage)
		os.Exit(2)
	}
	if fs.NArg() == 0 || (j.source != "nagios" && fs.NArg() != 1) {
		fmt.Fprint(os.Stderr, importUsage)
		os.Exit(2)
	}
	if !store.ValidRole(j.role) {
		return nil, fmt.Errorf("unknown role %q: use admin, operator or viewer", j.role)
	}
	j.inputs = fs.Args()
	if j.cfgPath == "" {
		j.cfgPath = config.PathIn(j.data)
	}
	return j, nil
}

func (j *importJob) adapter() bool { return j.source == "gatus" || j.source == "checkmate" }

func (j *importJob) run() error {
	if j.adapter() {
		if err := j.precheckAdapter(); err != nil {
			return err
		}
	}
	if err := j.loadTarget(); err != nil {
		return err
	}
	if err := j.openStore(); err != nil {
		return err
	}
	if j.st != nil {
		defer j.st.Close()
	}
	res, err := j.convert()
	if err != nil {
		return err
	}
	j.planAccounts(res)
	lines := strings.Count(string(j.src), "\n") + 1
	changed := hasRecords(res)
	next, err := j.addRecords(res, changed)
	if err != nil {
		return err
	}
	if err := j.validate(res, lines); err != nil {
		return err
	}
	ro := importer.ReportOptions{Width: termWidth(j.width), DryRun: j.dry, Extra: j.extra}
	if changed {
		ro.Config = j.cfgPath
	}
	if !j.dry {
		if err := j.write(res, changed, &ro); err != nil {
			return err
		}
	}
	if len(res.PushTokens) > 0 {
		ro.Extra = append(ro.Extra, "Push monitors keep their Uptime Kuma push URLs: only the server address changes (…/api/push/TOKEN).")
	}
	j.finish(res, ro, changed, next)
	return nil
}

// precheckAdapter reads a Gatus or Checkmate file and, for a real import,
// stops on anything that would not behave the same here.
func (j *importJob) precheckAdapter() error {
	f, err := os.Open(j.inputs[0])
	if err != nil {
		return err
	}
	j.adapterData, err = io.ReadAll(io.LimitReader(f, importer.AdapterMaxBytes+1))
	f.Close()
	if err != nil {
		return err
	}
	pre, err := j.runAdapter(nil, importer.Options{})
	if err != nil {
		return err
	}
	return j.activationError(pre)
}

func (j *importJob) runAdapter(cfg *config.Config, o importer.Options) (*importer.Result, error) {
	if j.source == "gatus" {
		return importer.Gatus(j.adapterData, cfg, o)
	}
	return importer.Checkmate(j.adapterData, cfg, o)
}

func (j *importJob) activationError(res *importer.Result) error {
	if j.dry {
		return nil
	}
	if err := res.ActivationError(); err != nil {
		fmt.Print(res.Report(importer.ReportOptions{DryRun: true}))
		return err
	}
	return nil
}

// loadTarget reads monitors.conf, or starts a new installation when this
// one was never started.
func (j *importJob) loadTarget() error {
	src, err := os.ReadFile(j.cfgPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if _, cerr := os.Stat(filepath.Join(j.data, "core.db")); cerr == nil {
			return fmt.Errorf("%s does not exist: start Unagikuma once so it restores it, or pass --config FILE", j.cfgPath)
		}
		// Never started: import into a new installation, so moving over
		// is one command. The data directory is created by the real import.
		src, j.fresh = app.Starter(time.Now()), true
		if !j.dry {
			if err := os.MkdirAll(filepath.Dir(j.cfgPath), 0o700); err != nil {
				return fmt.Errorf("data directory: %w", err)
			}
			if err := os.WriteFile(j.cfgPath, src, 0o600); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "Created a new installation in %s.\n", j.data)
		}
	case err != nil:
		return err
	}
	j.src = src
	var diags []config.Diagnostic
	j.file, j.cfg, diags = config.Load(j.cfgPath, src, config.Options{})
	if config.HasErrors(diags) {
		return fmt.Errorf("%s has errors, fix them first: %v", j.cfgPath, firstError(diags))
	}
	return nil
}

// openStore opens the accounts and secrets. A dry run only reads them.
func (j *importJob) openStore() error {
	if _, err := os.Stat(filepath.Join(j.data, "core.db")); err != nil && j.dry {
		return nil
	}
	st, err := store.Open(j.data, store.Options{Tool: true})
	if err != nil {
		return err
	}
	j.st = st
	if j.fresh || st.FirstRun {
		// The first server start is still a first start (no "restarted"
		// or "exited abnormally").
		_ = st.SetSetting("never_started", "1")
	}
	kek, err := store.LoadOrCreateKey(j.data)
	if err != nil {
		return err
	}
	j.sec = st.Secrets(kek)
	return nil
}

func (j *importJob) convert() (*importer.Result, error) {
	o := importer.Options{Area: j.area, SecretExists: func(n string) bool { return j.sec != nil && j.sec.Has(n) },
		UserExists: func(n string) bool { return j.st != nil && userExists(j.st, n) }}
	switch j.source {
	case "gatus", "checkmate":
		res, err := j.runAdapter(j.cfg, o)
		if err != nil {
			return nil, err
		}
		return res, j.activationError(res)
	case "nagios":
		in, err := importer.ReadNagiosFiles(j.inputs)
		if err != nil {
			return nil, err
		}
		o.File, o.Resources = in.Name, in.Resources
		return importer.NagiosInput(in, j.cfg, o)
	}
	path := j.inputs[0]
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, friendlyReadError(path, err)
	}
	o.File = filepath.Base(path)
	var wal []byte
	if w, err := os.ReadFile(path + "-wal"); err == nil && len(w) > 0 {
		wal = w
		j.extra = append(j.extra, "Uptime Kuma looks like it is still running ("+filepath.Base(path)+"-wal exists). Its latest changes were read as well; stop Uptime Kuma before the real import to be sure nothing is missed.")
	}
	return importer.KumaWithWAL(raw, wal, j.cfg, o)
}

// planAccounts picks the accounts to add. On a new installation the first
// Uptime Kuma account becomes the administrator, so nobody is locked out.
func (j *importJob) planAccounts(res *importer.Result) {
	accounts := 0
	if j.st != nil {
		accounts, _ = j.st.UserCount()
	}
	for _, u := range res.Users {
		if !u.Active || (j.st != nil && userExists(j.st, u.Username)) {
			continue
		}
		j.newUsers = append(j.newUsers, u)
		if accounts == 0 && j.firstAdmin == "" && j.role != store.RoleAdmin {
			j.firstAdmin = u.Username
		}
	}
	for i := range res.Items {
		it := &res.Items[i]
		if it.Kind != "account" || it.Status != importer.StatusImported {
			continue
		}
		r := j.role + areaNote(j.area, j.role)
		if it.Source == j.firstAdmin {
			r = "administrator (there were no accounts yet)"
		}
		it.Target = "as " + r + ", with its Uptime Kuma password"
	}
	if j.firstAdmin != "" {
		j.extra = append(j.extra, "There were no accounts here yet, so "+j.firstAdmin+" from Uptime Kuma becomes the administrator. Log in with the Uptime Kuma user name and password.")
	}
}

// addRecords puts the records at the end of monitors.conf in one block and
// raises the revision; it returns the new revision.
func (j *importJob) addRecords(res *importer.Result, changed bool) (uint32, error) {
	cur, _ := j.file.Serial()
	if !changed {
		return cur, nil
	}
	if err := j.file.AppendBlock(res.Render(j.file.EndArea())); err != nil {
		return 0, fmt.Errorf("internal error: the imported records do not fit in %s: %v", j.cfgPath, err)
	}
	next, _, err := config.NextSerial(cur, time.Now())
	if err != nil {
		return 0, err
	}
	return next, j.file.SetSerial(next)
}

// validate checks the new file and turns warnings about the new records
// (after the first lines lines) into a hint for the report.
func (j *importJob) validate(res *importer.Result, lines int) error {
	known := func(name string) bool { _, ok := res.Secrets[name]; return ok || (j.sec != nil && j.sec.Has(name)) }
	_, _, diags := config.Load(j.cfgPath, j.file.Bytes(), config.Options{SecretKnown: known, ValidateChannel: app.ValidateChannel, ChannelWarnings: app.ChannelWarnings})
	if config.HasErrors(diags) {
		return fmt.Errorf("internal error: the imported records do not validate, nothing was changed: %v", firstError(diags))
	}
	if hint := newRecordWarnings(diags, j.file.Bytes(), lines, j.cfgPath); hint != "" {
		j.extra = append(j.extra, hint)
	}
	return nil
}

func newRecordWarnings(diags []config.Diagnostic, text []byte, lines int, cfgPath string) string {
	var warns []string
	newLines := strings.Split(string(text), "\n")
	byMsg := map[string][]string{}
	for _, d := range diags {
		if d.Severity != config.SevWarning || d.Pos.Line <= lines || d.Pos.Line > len(newLines) {
			continue
		}
		if _, ok := byMsg[d.Msg]; !ok {
			warns = append(warns, d.Msg)
		}
		name, _, _ := strings.Cut(strings.TrimSpace(newLines[d.Pos.Line-1]), " ")
		byMsg[d.Msg] = append(byMsg[d.Msg], name)
	}
	if len(warns) == 0 {
		return ""
	}
	for i, m := range warns {
		warns[i] = strings.Join(byMsg[m], ", ") + ": " + m
	}
	if len(warns) > 6 {
		warns = append(warns[:5], fmt.Sprintf("and %d more (run: unagikuma check %s)", len(warns)-5, cfgPath))
	}
	return "The configuration check has hints for the new records. " + strings.Join(warns, ". ") + "."
}

// write stores the secrets, the file, the accounts and the push tokens.
func (j *importJob) write(res *importer.Result, changed bool, ro *importer.ReportOptions) error {
	if changed {
		for name, value := range res.Secrets {
			if err := j.sec.Put(name, value, nil); err != nil {
				return err
			}
		}
		if err := replaceFile(j.cfgPath, j.file.Bytes()); err != nil {
			return err
		}
	}
	var areas []string
	if j.area != "" && j.role != store.RoleAdmin {
		areas = []string{j.area}
	}
	for _, u := range j.newUsers {
		r, a := j.role, areas
		if u.Username == j.firstAdmin {
			r, a = store.RoleAdmin, nil
		}
		if _, err := j.st.CreateUserHash(u.Username, u.Hash, r, a); err != nil {
			msg := "Account " + u.Username + " was not added: "
			if errors.Is(err, store.ErrUserExists) {
				msg += "an account with this name is already here."
			} else {
				msg += err.Error() + "."
			}
			ro.Extra = append(ro.Extra, msg)
		}
	}
	for name, tok := range res.PushTokens {
		if err := j.st.SetPushToken(name, tok); err != nil {
			ro.Extra = append(ro.Extra, "The push URL of "+name+" could not be kept ("+err.Error()+"); use the new one shown on the monitor.")
		}
	}
	return nil
}

// finish prints and saves the report, audits the import and says what
// happened.
func (j *importJob) finish(res *importer.Result, ro importer.ReportOptions, changed bool, next uint32) {
	report := res.Report(ro)
	fmt.Print(report)
	dir := j.reportDir
	if dir == "" {
		dir = filepath.Join(j.data, "import-reports")
	}
	saved, err := saveReport(dir, j.source, res, ro, report, string(j.file.Bytes()), j.dry)
	fmt.Println()
	if err != nil {
		fmt.Fprintf(os.Stderr, "The report could not be saved: %v\n", err)
	} else {
		fmt.Printf("Report saved to %s\n", saved)
	}
	if !j.dry && (changed || len(j.newUsers) > 0) {
		cliAudit(j.st, "import."+j.source, strings.Join(j.inputs, " "), map[string]any{"serial": next, "accounts": len(j.newUsers)})
	}
	switch {
	case !changed && len(j.newUsers) == 0:
		fmt.Println("Nothing new to import: everything is already here.")
	case !changed && j.dry:
		fmt.Println("This was a preview. Run the same command without --dry-run to add the accounts.")
	case !changed:
		fmt.Println("Done: the accounts were added.")
	case j.dry:
		fmt.Println("This was a preview. Run the same command without --dry-run to import.")
	default:
		fmt.Printf("Done. %s now has revision %d; a running Unagikuma picks it up by itself.\n", filepath.Base(j.cfgPath), next)
	}
}

func hasRecords(res *importer.Result) bool { return len(res.Records) > 0 }

func userExists(st *store.Store, name string) bool {
	_, err := st.UserID(name)
	return err == nil
}

func areaNote(area, role string) string {
	if area == "" || role == store.RoleAdmin {
		return ""
	}
	return " for section " + area
}

func firstError(ds []config.Diagnostic) config.Diagnostic {
	for _, d := range ds {
		if d.Severity == config.SevError {
			return d
		}
	}
	return ds[0]
}

func friendlyReadError(path string, err error) error {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("%s: no such file", path)
	case errors.Is(err, os.ErrPermission):
		return fmt.Errorf("%s: permission denied (try sudo, or copy the file somewhere readable)", path)
	}
	return err
}

// termWidth is the width of the terminal the report is printed to.
func termWidth(flagWidth int) int {
	if flagWidth > 0 {
		return flagWidth
	}
	if w, ok := ttyWidth(int(os.Stdout.Fd())); ok {
		return min(w, 120)
	}
	if n, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && n > 0 {
		return min(n, 120)
	}
	return 80
}

// saveReport writes the report as text and Markdown, the Markdown with
// the records that were (or would be) added.
func saveReport(dir, source string, res *importer.Result, ro importer.ReportOptions, text, bind string, dry bool) (string, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	base := source + "-" + time.Now().Format("20060102-150405")
	if dry {
		base += "-preview"
	}
	md := res.Markdown(ro)
	if hasRecords(res) {
		md += "\n## Added to monitors.conf\n\n```text\n" + res.Text + "```\n"
	}
	if err := os.WriteFile(filepath.Join(dir, base+".txt"), []byte(text), 0o640); err != nil {
		return "", err
	}
	p := filepath.Join(dir, base+".md")
	if err := os.WriteFile(p, []byte(md), 0o640); err != nil {
		return "", err
	}
	_ = bind
	return p, nil
}

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
	if len(args) == 0 || (args[0] != "kuma" && args[0] != "nagios" && args[0] != "gatus" && args[0] != "checkmate") {
		if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
			fmt.Print(importUsage)
			return nil
		}
		fmt.Fprint(os.Stderr, importUsage)
		os.Exit(2)
	}
	source := args[0]
	fs := flag.NewFlagSet("import "+source, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	data := fs.String("data", defaultDataDir(), "")
	cfgPath := fs.String("config", "", "")
	dry := fs.Bool("dry-run", false, "")
	area := fs.String("area", "", "")
	role := fs.String("role", store.RoleOperator, "")
	reportDir := fs.String("report", "", "")
	width := fs.Int("width", 0, "")
	if err := fs.Parse(interleavedFlags(args[1:], map[string]bool{"data": true, "config": true, "area": true, "role": true, "report": true, "width": true})); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Print(importUsage)
			return nil
		}
		fmt.Fprintf(os.Stderr, "unagikuma import: %v\n\n%s", err, importUsage)
		os.Exit(2)
	}
	if fs.NArg() == 0 || (source != "nagios" && fs.NArg() != 1) {
		fmt.Fprint(os.Stderr, importUsage)
		os.Exit(2)
	}
	if !store.ValidRole(*role) {
		return fmt.Errorf("unknown role %q: use admin, operator or viewer", *role)
	}
	var adapterData []byte
	if source == "gatus" || source == "checkmate" {
		f, err := os.Open(fs.Arg(0))
		if err != nil {
			return err
		}
		adapterData, err = io.ReadAll(io.LimitReader(f, importer.AdapterMaxBytes+1))
		f.Close()
		if err != nil {
			return err
		}
		var pre *importer.Result
		if source == "gatus" {
			pre, err = importer.Gatus(adapterData, nil, importer.Options{})
		} else {
			pre, err = importer.Checkmate(adapterData, nil, importer.Options{})
		}
		if err != nil {
			return err
		}
		if !*dry {
			if err := pre.ActivationError(); err != nil {
				fmt.Print(pre.Report(importer.ReportOptions{DryRun: true}))
				return err
			}
		}
	}
	if *cfgPath == "" {
		*cfgPath = config.PathIn(*data)
	}
	src, err := os.ReadFile(*cfgPath)
	fresh := false
	if errors.Is(err, os.ErrNotExist) {
		if _, cerr := os.Stat(filepath.Join(*data, "core.db")); cerr == nil {
			return fmt.Errorf("%s does not exist: start Unagikuma once so it restores it, or pass --config FILE", *cfgPath)
		}
		// Never started: import into a new installation, so moving over
		// is one command. The data directory is created by the real import.
		src, fresh = app.Starter(time.Now()), true
		if !*dry {
			if err := os.MkdirAll(filepath.Dir(*cfgPath), 0o700); err != nil {
				return fmt.Errorf("data directory: %w", err)
			}
			if err := os.WriteFile(*cfgPath, src, 0o600); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "Created a new installation in %s.\n", *data)
		}
	} else if err != nil {
		return err
	}
	f, cfg, diags := config.Load(*cfgPath, src, config.Options{})
	if config.HasErrors(diags) {
		return fmt.Errorf("%s has errors, fix them first: %v", *cfgPath, firstError(diags))
	}

	// The store holds secrets and accounts. A dry run only reads it.
	var st *store.Store
	if _, err := os.Stat(filepath.Join(*data, "core.db")); err == nil || !*dry {
		if st, err = store.Open(*data, store.Options{Tool: true}); err != nil {
			return err
		}
		defer st.Close()
		if fresh || st.FirstRun {
			// The first server start is still a first start (no "restarted"
			// or "exited abnormally").
			_ = st.SetSetting("never_started", "1")
		}
	}
	var sec *store.Secrets
	if st != nil {
		kek, err := store.LoadOrCreateKey(*data)
		if err != nil {
			return err
		}
		sec = st.Secrets(kek)
	}
	o := importer.Options{Area: *area, SecretExists: func(n string) bool { return sec != nil && sec.Has(n) },
		UserExists: func(n string) bool { return st != nil && userExists(st, n) }}
	var extra []string
	var res *importer.Result
	if source == "gatus" || source == "checkmate" {
		if source == "gatus" {
			res, err = importer.Gatus(adapterData, cfg, o)
		} else {
			res, err = importer.Checkmate(adapterData, cfg, o)
		}
		if err != nil {
			return err
		}
		if !*dry {
			if err := res.ActivationError(); err != nil {
				fmt.Print(res.Report(importer.ReportOptions{DryRun: true}))
				return err
			}
		}
	} else if source == "nagios" {
		in, err := importer.ReadNagiosFiles(fs.Args())
		if err != nil {
			return err
		}
		o.File, o.Resources = in.Name, in.Resources
		res, err = importer.NagiosInput(in, cfg, o)
		if err != nil {
			return err
		}
	} else {
		path := fs.Arg(0)
		raw, err := os.ReadFile(path)
		if err != nil {
			return friendlyReadError(path, err)
		}
		o.File = filepath.Base(path)
		var wal []byte
		if w, err := os.ReadFile(path + "-wal"); err == nil && len(w) > 0 {
			wal = w
			extra = append(extra, "Uptime Kuma looks like it is still running ("+filepath.Base(path)+"-wal exists). Its latest changes were read as well; stop Uptime Kuma before the real import to be sure nothing is missed.")
		}
		res, err = importer.KumaWithWAL(raw, wal, cfg, o)
		if err != nil {
			return err
		}
	}

	// Accounts: on a new installation the first Uptime Kuma account
	// becomes the administrator, so nobody is locked out.
	accounts := 0
	if st != nil {
		accounts, _ = st.UserCount()
	}
	firstAdmin := ""
	var newUsers []importer.KumaUser
	for _, u := range res.Users {
		if !u.Active || (st != nil && userExists(st, u.Username)) {
			continue
		}
		newUsers = append(newUsers, u)
		if accounts == 0 && firstAdmin == "" && *role != store.RoleAdmin {
			firstAdmin = u.Username
		}
	}
	for i := range res.Items {
		it := &res.Items[i]
		if it.Kind != "account" || it.Status != importer.StatusImported {
			continue
		}
		r := *role + areaNote(*area, *role)
		if it.Source == firstAdmin {
			r = "administrator (there were no accounts yet)"
		}
		it.Target = "as " + r + ", with its Uptime Kuma password"
	}
	if firstAdmin != "" {
		extra = append(extra, "There were no accounts here yet, so "+firstAdmin+" from Uptime Kuma becomes the administrator. Log in with the Uptime Kuma user name and password.")
	}

	// The records go at the end of monitors.conf in one block.
	lines := strings.Count(string(src), "\n") + 1
	changed := hasRecords(res)
	if changed {
		if err := f.AppendBlock(res.Render(f.EndArea())); err != nil {
			return fmt.Errorf("internal error: the imported records do not fit in %s: %v", *cfgPath, err)
		}
	}
	cur, _ := f.Serial()
	next := cur
	if changed {
		if next, _, err = config.NextSerial(cur, time.Now()); err != nil {
			return err
		}
		if err := f.SetSerial(next); err != nil {
			return err
		}
	}
	known := func(name string) bool { _, ok := res.Secrets[name]; return ok || (sec != nil && sec.Has(name)) }
	_, _, diags = config.Load(*cfgPath, f.Bytes(), config.Options{SecretKnown: known, ValidateChannel: app.ValidateChannel})
	if config.HasErrors(diags) {
		return fmt.Errorf("internal error: the imported records do not validate, nothing was changed: %v", firstError(diags))
	}
	// Warnings about the new records are worth a look.
	var warns []string
	newLines := strings.Split(string(f.Bytes()), "\n")
	byMsg := map[string][]string{}
	for _, d := range diags {
		if d.Severity == config.SevWarning && d.Pos.Line > lines && d.Pos.Line <= len(newLines) {
			if _, ok := byMsg[d.Msg]; !ok {
				warns = append(warns, d.Msg)
			}
			name, _, _ := strings.Cut(strings.TrimSpace(newLines[d.Pos.Line-1]), " ")
			byMsg[d.Msg] = append(byMsg[d.Msg], name)
		}
	}
	for i, m := range warns {
		warns[i] = strings.Join(byMsg[m], ", ") + ": " + m
	}
	if len(warns) > 0 {
		if len(warns) > 6 {
			warns = append(warns[:5], fmt.Sprintf("and %d more (run: unagikuma check %s)", len(warns)-5, *cfgPath))
		}
		extra = append(extra, "The configuration check has hints for the new records. "+strings.Join(warns, ". ")+".")
	}

	ro := importer.ReportOptions{Width: termWidth(*width), DryRun: *dry, Extra: extra}
	if changed {
		ro.Config = *cfgPath
	}
	if !*dry && changed {
		for name, value := range res.Secrets {
			if err := sec.Put(name, value, nil); err != nil {
				return err
			}
		}
		if err := replaceFile(*cfgPath, f.Bytes()); err != nil {
			return err
		}
	}
	if !*dry {
		var areas []string
		if *area != "" && *role != store.RoleAdmin {
			areas = []string{*area}
		}
		for _, u := range newUsers {
			r, a := *role, areas
			if u.Username == firstAdmin {
				r, a = store.RoleAdmin, nil
			}
			if _, err := st.CreateUserHash(u.Username, u.Hash, r, a); err != nil {
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
			if err := st.SetPushToken(name, tok); err != nil {
				ro.Extra = append(ro.Extra, "The push URL of "+name+" could not be kept ("+err.Error()+"); use the new one shown on the monitor.")
			}
		}
	}
	if len(res.PushTokens) > 0 {
		ro.Extra = append(ro.Extra, "Push monitors keep their Uptime Kuma push URLs: only the server address changes (…/api/push/TOKEN).")
	}

	report := res.Report(ro)
	fmt.Print(report)
	dir := *reportDir
	if dir == "" {
		dir = filepath.Join(*data, "import-reports")
	}
	saved, err := saveReport(dir, source, res, ro, report, string(f.Bytes()), *dry)
	fmt.Println()
	if err != nil {
		fmt.Fprintf(os.Stderr, "The report could not be saved: %v\n", err)
	} else {
		fmt.Printf("Report saved to %s\n", saved)
	}
	if !*dry && (changed || len(newUsers) > 0) {
		cliAudit(st, "import."+source, strings.Join(fs.Args(), " "), map[string]any{"serial": next, "accounts": len(newUsers)})
	}
	switch {
	case !changed && len(newUsers) == 0:
		fmt.Println("Nothing new to import: everything is already here.")
	case !changed && *dry:
		fmt.Println("This was a preview. Run the same command without --dry-run to add the accounts.")
	case !changed:
		fmt.Println("Done: the accounts were added.")
	case *dry:
		fmt.Println("This was a preview. Run the same command without --dry-run to import.")
	default:
		fmt.Printf("Done. %s now has revision %d; a running Unagikuma picks it up by itself.\n", filepath.Base(*cfgPath), next)
	}
	return nil
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

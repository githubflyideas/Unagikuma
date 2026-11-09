package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/store"
)

// cliActor names who ran a command on the server itself.
func cliActor() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return "cli:" + u.Username
	}
	return "cli"
}

// cliAudit records a command-line action in the audit log.
func cliAudit(st *store.Store, action, target string, detail map[string]any) {
	if st == nil {
		return
	}
	_ = st.Audit(store.AuditEntry{Actor: cliActor(), Action: action, Target: target, Detail: detail, Addr: "local"})
}

// cliAuditDir opens the data directory just to record an action.
func cliAuditDir(dir, action, target string, detail map[string]any) {
	if _, err := os.Stat(filepath.Join(dir, "core.db")); err != nil {
		return
	}
	st, err := store.Open(dir, store.Options{Tool: true})
	if err != nil {
		return
	}
	defer st.Close()
	cliAudit(st, action, target, detail)
}

const auditUsage = `Usage:
  unagikuma audit verify [--data DIR] [--head HASH]
  unagikuma audit export [--data DIR] [--format csv|json] [--since 30d] [--high]

verify   checks that no entry of the audit log was changed, inserted or
         removed. It prints the newest entry's hash: keep it somewhere else
         and pass it later with --head to prove nothing was cut off the end.
         Exit status 0: intact, 1: tampered with.
export   writes the log (newest first) to standard output.
`

func runAudit(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Print(auditUsage)
		return nil
	}
	sub := args[0]
	fs := flag.NewFlagSet("audit "+sub, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	data := fs.String("data", defaultDataDir(), "")
	head := fs.String("head", "", "")
	format := fs.String("format", "csv", "")
	since := fs.String("since", "", "")
	high := fs.Bool("high", false, "")
	if err := fs.Parse(interleavedFlags(args[1:], map[string]bool{"data": true, "head": true, "format": true, "since": true})); err != nil {
		fmt.Fprintf(os.Stderr, "unagikuma audit: %v\n\n%s", err, auditUsage)
		os.Exit(2)
	}
	if _, err := os.Stat(filepath.Join(*data, "core.db")); err != nil {
		return fmt.Errorf("no Unagikuma data in %s: pass the data directory with --data DIR", *data)
	}
	st, err := store.Open(*data, store.Options{Tool: true})
	if err != nil {
		return err
	}
	defer st.Close()
	switch sub {
	case "verify":
		return auditVerify(os.Stdout, st, *head)
	case "export":
		q := store.AuditQuery{Export: true}
		if *high {
			q.Risk = "high"
		}
		if *since != "" {
			d, err := parseDays(*since)
			if err != nil {
				return err
			}
			q.From = time.Now().Add(-d)
		}
		items, err := st.AuditLog(q)
		if err != nil {
			return err
		}
		return store.WriteAudit(os.Stdout, *format, items)
	}
	fmt.Fprint(os.Stderr, auditUsage)
	os.Exit(2)
	return nil
}

func auditVerify(out io.Writer, st *store.Store, head string) error {
	r, err := st.VerifyAudit()
	if err != nil {
		return err
	}
	when := func(t time.Time) string {
		if t.IsZero() {
			return "-"
		}
		return t.Local().Format("2006-01-02 15:04:05")
	}
	fmt.Fprintf(out, "Entries: %d (%s … %s)\nHead:    %s\n", r.Entries, when(r.First), when(r.Last), r.Head)
	if !r.OK {
		fmt.Fprintf(out, "TAMPERED: %s.\n", r.Problem)
		os.Exit(1)
	}
	if head != "" && !st.AuditContains(head) {
		fmt.Fprintf(out, "TAMPERED: the entry with hash %s noted earlier is gone: entries were removed from the end.\n", head)
		os.Exit(1)
	}
	fmt.Fprintln(out, "OK: the audit log is intact.")
	return nil
}

// parseDays reads "30d", "12h" or a Go duration.
func parseDays(s string) (time.Duration, error) {
	if n, ok := strings.CutSuffix(s, "d"); ok {
		var d int
		if _, err := fmt.Sscanf(n, "%d", &d); err != nil {
			return 0, fmt.Errorf("--since %s: use e.g. 30d", s)
		}
		return time.Duration(d) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

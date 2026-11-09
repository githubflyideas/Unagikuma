package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/githubflyideas/unagikuma/internal/cluster"
	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/store"
)

const nodeUsage = `usage:
  unagikuma node list              list the machines of the cluster and their tokens
  unagikuma node revoke NAME       disconnect a machine (revoke its token)

Common flags: --data DIR, --config FILE.

Add a machine with a join code: Settings › Nodes › Join code on the master,
Settings › Nodes › Join cluster on the new machine.`

// errNoAgents answers the commands that only served probe-only agents.
var errNoAgents = errors.New("probe-only agents are gone since 2.2, and with them node add and node token: " +
	"add a machine with a join code (Settings › Nodes › Join code on the master, Join cluster on the new machine)")

func runNode(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(strings.TrimSpace(nodeUsage))
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("node "+sub, flag.ExitOnError)
	data := fs.String("data", defaultDataDir(), "data directory")
	cfgPath := fs.String("config", "", "path of monitors.conf (default <data>/monitors.conf)")
	// The name may come before or after the flags.
	name := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name, args = args[0], args[1:]
	}
	_ = fs.Parse(args)
	if name == "" && fs.NArg() > 0 {
		name = fs.Arg(0)
	}
	path := *cfgPath
	if path == "" {
		path = config.PathIn(*data)
	}
	if sub == "add" || sub == "token" {
		return errNoAgents
	}
	if sub != "list" && name == "" {
		return errors.New(strings.TrimSpace(nodeUsage))
	}
	switch sub {
	case "list":
		return nodeList(out, path, *data)
	case "revoke":
		st, err := store.Open(*data, store.Options{Tool: true})
		if err != nil {
			return err
		}
		defer st.Close()
		if err := st.DeleteAgentToken(name); err != nil {
			return err
		}
		cliAudit(st, "node.token.delete", name, nil)
		fmt.Fprintf(out, "Token of node %s revoked. A running server ends its session within one ping interval.\n", name)
		return nil
	}
	return errors.New(strings.TrimSpace(nodeUsage))
}

func loadConfig(path string) (*config.Config, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	_, cfg, diags := config.Load(path, src, config.Options{})
	for _, d := range diags {
		if d.Severity == config.SevError {
			return nil, fmt.Errorf("%s has errors, fix them first: %v", path, d)
		}
	}
	return cfg, nil
}

func nodeList(out io.Writer, path, data string) error {
	cfg, err := loadConfig(path)
	if err != nil {
		return err
	}
	st, err := store.Open(data, store.Options{Tool: true})
	if err != nil {
		return err
	}
	defer st.Close()
	tokens := map[string]store.AgentToken{}
	list, err := st.AgentTokens()
	if err != nil {
		return err
	}
	for _, t := range list {
		tokens[t.Node] = t
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tTITLE\tREGION\tPROBE\tTOKEN\tLAST USED\tFROM")
	fmt.Fprintf(tw, "%s\t(this server)\t\t%v\t\t\t\n", cfg.MName, selfProbes(cfg))
	for _, n := range cfg.Nodes {
		if n.Name == cfg.MName {
			continue
		}
		tok, used, from := "none", "", ""
		if t, ok := tokens[n.Name]; ok {
			tok, from = t.Prefix+"…", t.LastAddr
			if t.LastUsed != nil {
				used = t.LastUsed.Local().Format("2006-01-02 15:04")
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%v\t%s\t%s\t%s\n", n.Name, n.Description, n.Region, n.Probe, tok, used, from)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if pki, err := cluster.LoadOrCreatePKI(data, time.Now()); err == nil {
		fmt.Fprintf(out, "\nCA fingerprint: %s\nLive connection state is shown in the web UI.\n", pki.Fingerprint())
	}
	return nil
}

func selfProbes(cfg *config.Config) bool {
	if n := cfg.FindNode(cfg.MName); n != nil {
		return n.Probe
	}
	return true
}

// replaceFile writes b over path atomically, keeping its permissions.
func replaceFile(path string, b []byte) error {
	mode := os.FileMode(0o640)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".monitors.conf.*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

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
	"github.com/githubflyideas/unagikuma/internal/postmortem"
	"github.com/githubflyideas/unagikuma/internal/store"
)

const reportUsage = `Usage:
  unagikuma report incident ID [--data DIR] [--config FILE] [--format md|json|html] [--lang CODE] [-o FILE]

Writes the report of one incident: what changed in the day before, how
it was detected, who was told and when, how it ended, whether monitoring
itself ran the whole time, and what the check covers. It reads the
recorded history, so it works with the server running or stopped.
The incident number is shown on the incident's page.
`

func runReport(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Print(reportUsage)
		return nil
	}
	if args[0] != "incident" {
		fmt.Fprintf(os.Stderr, "unagikuma report: unknown report %q\n\n%s", args[0], reportUsage)
		os.Exit(2)
	}
	fs := flag.NewFlagSet("report incident", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	data := fs.String("data", defaultDataDir(), "")
	cfgPath := fs.String("config", "", "")
	format := fs.String("format", "md", "")
	lang := fs.String("lang", "", "")
	out := fs.String("o", "", "")
	if err := fs.Parse(interleavedFlags(args[1:], map[string]bool{"data": true, "config": true, "format": true, "lang": true, "o": true})); err != nil {
		fmt.Fprintf(os.Stderr, "unagikuma report: %v\n\n%s", err, reportUsage)
		os.Exit(2)
	}
	if fs.NArg() != 1 {
		fmt.Fprint(os.Stderr, reportUsage)
		os.Exit(2)
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(fs.Arg(0), "#"), 10, 64)
	if err != nil || id <= 0 {
		return fmt.Errorf("%q is not an incident number", fs.Arg(0))
	}
	if _, err := os.Stat(filepath.Join(*data, "core.db")); err != nil {
		return fmt.Errorf("no Unagikuma data in %s: pass the data directory with --data DIR", *data)
	}
	st, err := store.Open(*data, store.Options{Tool: true})
	if err != nil {
		return err
	}
	defer st.Close()
	path := *cfgPath
	if path == "" {
		path = config.PathIn(*data)
	}
	var cfg *config.Config
	if src, err := os.ReadFile(path); err == nil {
		_, cfg, _ = config.Load(path, src, config.Options{})
	}
	rep, err := app.PostmortemFrom(st, cfg, id, *lang, time.Local)
	if errors.Is(err, postmortem.ErrNotFound) {
		return fmt.Errorf("incident %d not found in %s", id, *data)
	}
	if err != nil {
		return err
	}
	w := io.Writer(os.Stdout)
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			return err
		}
		defer f.Close()
		w = f
	}
	return rep.Write(w, *format)
}

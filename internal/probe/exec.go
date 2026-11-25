package probe

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/githubflyideas/unagikuma/internal/keywords"
)

// EXEC runs a check plugin following the Nagios plugin conventions: exit
// status 0 OK, 1 WARNING, 2 CRITICAL, 3 UNKNOWN; the first output line is
// the status text and anything after "|" is performance data.
//
// Only plugins inside the plugin directories chosen by whoever runs the
// process can be run, without a shell and with a minimal environment: the
// configuration (editable from the web) names a plugin and its arguments,
// it cannot bring its own program.

// Exec reasons.
const (
	ReasonPluginWarning  = "plugin_warning"  // ok + degraded: exit 1
	ReasonPluginCritical = "plugin_critical" // fail: exit 2
	ReasonPluginUnknown  = "plugin_unknown"  // abstain: exit 3 or another status
	ReasonPluginTimeout  = "plugin_timeout"  // fail: killed at the timeout
)

const execOutputCap = 64 << 10

var execOpts = map[string]bool{"args": true, "address": true}

var (
	execMu   sync.RWMutex
	execDirs []string
	execSem  = make(chan struct{}, 8)
)

// SetPluginDirs sets the directories EXEC plugins are looked up in.
func SetPluginDirs(dirs []string) {
	execMu.Lock()
	defer execMu.Unlock()
	execDirs = nil
	for _, d := range dirs {
		if d = strings.TrimSpace(d); d != "" {
			// Absolute: plugins run in their own directory, where a path
			// relative to the server's working directory (--data ./data)
			// would point elsewhere.
			if abs, err := filepath.Abs(d); err == nil {
				d = abs
			}
			execDirs = append(execDirs, filepath.Clean(d))
		}
	}
	scriptCache.list = nil
}

// PluginDirs returns the configured plugin directories.
func PluginDirs() []string {
	execMu.RLock()
	defer execMu.RUnlock()
	return append([]string(nil), execDirs...)
}

// SetExecLimit bounds the plugins running at once on this node.
func SetExecLimit(n int) {
	if n < 1 {
		n = 1
	}
	execMu.Lock()
	defer execMu.Unlock()
	if cap(execSem) != n {
		execSem = make(chan struct{}, n)
	}
}

var pluginName = regexp.MustCompile(`^[A-Za-z0-9_+][A-Za-z0-9._+-]*$`)

// execCommand splits the target into the plugin name and its arguments:
// either "check_disk" with args=, or the whole command line as target.
func execCommand(target string, o opts) (string, []string, error) {
	words, err := splitArgs(target)
	if err != nil {
		return "", nil, err
	}
	if len(words) == 0 {
		return "", nil, errors.New("target must name a plugin")
	}
	name, args := words[0], words[1:]
	if !pluginName.MatchString(name) {
		return "", nil, fmt.Errorf("plugin %q must be a file name inside the plugin directory, without a path", name)
	}
	if v := o.str("args", ""); v != "" {
		more, err := splitArgs(v)
		if err != nil {
			return "", nil, fmt.Errorf("args: %v", err)
		}
		args = append(args, more...)
	}
	return name, args, nil
}

// splitArgs splits a command line like a POSIX shell without expanding
// anything: whitespace separates words, single quotes are literal, double
// quotes and backslashes escape.
func splitArgs(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	in := false // inside a word
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'':
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return nil, errors.New("unterminated single quote")
			}
			cur.WriteString(s[i+1 : i+1+j])
			i += j + 1
			in = true
		case c == '"':
			i++
			for ; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' && i+1 < len(s) && (s[i+1] == '"' || s[i+1] == '\\') {
					i++
				}
				cur.WriteByte(s[i])
			}
			if i >= len(s) {
				return nil, errors.New("unterminated double quote")
			}
			in = true
		case c == '\\' && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
			in = true
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteByte(c)
			in = true
		}
	}
	if in {
		out = append(out, cur.String())
	}
	return out, nil
}

func validateExec(target string, o opts) error {
	if err := rejectUnknown(o, execOpts); err != nil {
		return err
	}
	_, _, err := execCommand(target, o)
	return err
}

// findPlugin resolves a plugin name in the plugin directories.
func findPlugin(name string) (string, error) {
	dirs := PluginDirs()
	if len(dirs) == 0 {
		return "", errors.New("no plugin directory is configured on this node")
	}
	for _, d := range dirs {
		p := filepath.Join(d, name)
		st, err := os.Stat(p)
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		if !executable(p, st) {
			return "", fmt.Errorf("plugin %s is not executable", p)
		}
		return p, nil
	}
	return "", fmt.Errorf("plugin %s not found in %s", name, strings.Join(dirs, ", "))
}

// macros substitutes the Nagios macros plugins are usually called with.
func macros(s string, sp Spec, o opts) string {
	if !strings.Contains(s, "$") {
		return s
	}
	return strings.NewReplacer(
		"$HOSTADDRESS$", o.str("address", ""),
		"$SERVICEDESC$", sp.Name,
		"$TIMEOUT$", strconv.Itoa(int(effTimeout(sp).Seconds())),
	).Replace(s)
}

// capBuffer keeps the first execOutputCap bytes and drops the rest.
type capBuffer struct{ bytes.Buffer }

func (c *capBuffer) Write(p []byte) (int, error) {
	if room := execOutputCap - c.Len(); room > 0 {
		if len(p) > room {
			c.Buffer.Write(p[:room])
		} else {
			c.Buffer.Write(p)
		}
	}
	return len(p), nil
}

func runExec(ctx context.Context, s Spec) Result {
	o := opts(s.Opts)
	name, args, err := execCommand(s.Target, o)
	if err != nil {
		return abstain(ReasonConfig, "%v", err)
	}
	path, err := findPlugin(name)
	if err != nil {
		return abstain(ReasonConfig, "%v", err)
	}
	for i := range args {
		args[i] = macros(args[i], s, o)
	}
	execMu.RLock()
	sem := execSem
	execMu.RUnlock()
	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-ctx.Done():
		return fail(ReasonPluginTimeout, "no free plugin slot within the timeout")
	}

	cmd := pluginCmd(ctx, path, args)
	cmd.Dir = filepath.Dir(path)
	cmd.Env = pluginEnv(s, o)
	var out capBuffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.WaitDelay = time.Second
	start := time.Now()
	runErr := cmd.Run()
	lat := time.Since(start)
	ev := &Evidence{Request: []byte(strings.Join(append([]string{name}, args...), " ")), Response: head(out.Bytes(), 512)}
	text, perf := parsePluginOutput(out.String())
	if text == "" {
		text = "(no output)"
	}
	var r Result
	code := -1
	var exitErr *exec.ExitError
	switch {
	case ctx.Err() != nil:
		r = fail(ReasonPluginTimeout, "plugin killed after %s", effTimeout(s))
	case runErr == nil:
		code = 0
	case errors.As(runErr, &exitErr):
		code = exitErr.ExitCode()
	default:
		r = abstain(ReasonInternal, "cannot run plugin: %v", runErr)
	}
	switch code {
	case 0:
		r = ok("%s", text)
	case 1:
		r = ok("%s", text)
		r.Reason, r.Degraded = ReasonPluginWarning, true
	case 2:
		r = fail(ReasonPluginCritical, "%s", text)
	case -1:
	default:
		r = abstain(ReasonPluginUnknown, "exit %d: %s", code, text)
		if code == 3 {
			r.Detail = oneLine(text)
		}
	}
	r.Latency, r.Evidence, r.Depth = lat, ev, keywords.L3
	if len(perf) > 0 {
		r.Metrics = perf
	}
	return r
}

func head(b []byte, n int) []byte {
	if len(b) > n {
		b = b[:n]
	}
	return append([]byte(nil), b...)
}

// parsePluginOutput returns the status text (first line, before "|") and
// the performance data of every line as perf.<label> metrics.
func parsePluginOutput(s string) (string, map[string]float64) {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	text := ""
	perf := map[string]float64{}
	for i, l := range lines {
		data := ""
		if j := strings.IndexByte(l, '|'); j >= 0 {
			l, data = l[:j], l[j+1:]
		}
		if i == 0 {
			text = strings.TrimSpace(l)
		}
		parsePerf(data, perf)
	}
	return text, perf
}

// parsePerf reads "'label'=value[UOM];warn;crit;min;max ..." items.
func parsePerf(s string, into map[string]float64) {
	s = strings.TrimSpace(s)
	for s != "" {
		var label string
		if s[0] == '\'' {
			j := strings.Index(s[1:], "'=")
			if j < 0 {
				return
			}
			label, s = s[1:1+j], s[j+3:]
		} else {
			j := strings.IndexByte(s, '=')
			if j < 0 {
				return
			}
			label, s = s[:j], s[j+1:]
		}
		end := strings.IndexFunc(s, unicode.IsSpace)
		if end < 0 {
			end = len(s)
		}
		val := s[:end]
		s = strings.TrimSpace(s[end:])
		if k := strings.IndexByte(val, ';'); k >= 0 {
			val = val[:k]
		}
		num := strings.TrimRightFunc(val, func(r rune) bool { return unicode.IsLetter(r) || r == '%' })
		f, err := strconv.ParseFloat(num, 64)
		label = strings.Join(strings.Fields(label), "_")
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || label == "" || len(into) >= 64 {
			continue
		}
		into["perf."+label] = f
	}
}

// pluginEnv is the minimal environment plugins run with: the server's own
// environment may hold credentials and is not passed on.
func pluginEnv(s Spec, o opts) []string {
	env := baseEnv()
	env = append(env,
		"UNAGIKUMA_MONITOR="+s.Name,
		"UNAGIKUMA_ADDRESS="+o.str("address", ""),
		"UNAGIKUMA_TIMEOUT="+strconv.Itoa(int(effTimeout(s).Seconds())),
	)
	return env
}

// ValidateHandler checks an event handler command line: a script name
// from the check-scripts directories and its arguments.
func ValidateHandler(cmdline string) error {
	_, _, err := execCommand(cmdline, opts{})
	return err
}

// RunHandler runs an event handler with the given environment added to the
// minimal one scripts get, and returns its exit code and first output.
func RunHandler(ctx context.Context, cmdline string, env []string) (int, string, error) {
	name, args, err := execCommand(cmdline, opts{})
	if err != nil {
		return -1, "", err
	}
	path, err := findPlugin(name)
	if err != nil {
		return -1, "", err
	}
	cmd := pluginCmd(ctx, path, args)
	cmd.Dir = filepath.Dir(path)
	cmd.Env = append(baseEnv(), env...)
	var out capBuffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.WaitDelay = time.Second
	runErr := cmd.Run()
	text := oneLine(string(head(out.Bytes(), 512)))
	var exitErr *exec.ExitError
	switch {
	case ctx.Err() != nil:
		return -1, text, errors.New("killed after the handler timeout")
	case runErr == nil:
		return 0, text, nil
	case errors.As(runErr, &exitErr):
		return exitErr.ExitCode(), text, nil
	}
	return -1, text, runErr
}

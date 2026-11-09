package probe

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// Script is one file in a check-script directory, as a node reports it so
// the master can tell which nodes lack a script or carry another version.
type Script struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	Exec bool   `json:"exec"`
	Hash string `json:"hash,omitempty"` // start of the SHA-256, for comparing nodes
}

const (
	scriptsTTL     = 30 * time.Second
	scriptsMax     = 1000
	scriptsHashCap = 16 << 20
)

var scriptCache struct {
	sync.Mutex
	at   time.Time
	list []Script
}

// Scripts lists the check scripts of this node, the first directory
// winning for a name, as findPlugin resolves them. The list is cached
// briefly: agents send it with every status message.
func Scripts() []Script {
	scriptCache.Lock()
	defer scriptCache.Unlock()
	if time.Since(scriptCache.at) < scriptsTTL && scriptCache.list != nil {
		return scriptCache.list
	}
	seen := map[string]bool{}
	out := []Script{}
	for _, d := range PluginDirs() {
		ents, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range ents {
			name := e.Name()
			if seen[name] || !pluginName.MatchString(name) || len(out) >= scriptsMax || docFile(name) {
				continue
			}
			p := filepath.Join(d, name)
			st, err := os.Stat(p)
			if err != nil || !st.Mode().IsRegular() {
				continue
			}
			seen[name] = true
			s := Script{Name: name, Size: st.Size(), Exec: executable(p, st)}
			if st.Size() <= scriptsHashCap {
				if f, err := os.Open(p); err == nil {
					h := sha256.New()
					if _, err := io.Copy(h, f); err == nil {
						s.Hash = hex.EncodeToString(h.Sum(nil)[:6])
					}
					f.Close()
				}
			}
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	scriptCache.at, scriptCache.list = time.Now(), out
	return out
}

// PluginOf is the script an EXEC target runs ("" when it is not valid).
func PluginOf(target string, o map[string]string) string {
	name, _, err := execCommand(target, opts(o))
	if err != nil {
		return ""
	}
	return name
}

// ResetScripts drops the cached list (after the directories change).
func ResetScripts() {
	scriptCache.Lock()
	scriptCache.list = nil
	scriptCache.Unlock()
}

const scriptReadme = `Check scripts for unagikuma EXEC checks
======================================

Put scripts here and make them executable. Each node runs its own copy:
copy the same script to every node that runs the check.

A script follows the Nagios plugin conventions, so existing plugins work
unchanged:

  exit 0  OK
  exit 1  WARNING   (shown as DEGRADED)
  exit 2  CRITICAL
  exit 3  UNKNOWN

The first output line is the status text; anything after "|" is
performance data (label=value[unit];warn;crit;min;max), kept as metrics.

In monitors.conf:

  disk  IN EXEC check_example args="-w 80 -c 90" device=db1

New script checks start paused. Enable them once the script is on every
node; Settings > Check scripts shows which node lacks it.
`

const exampleScript = `#!/bin/sh
# Example check: disk usage of / against -w and -c percentages.
warn=80 crit=90
while [ $# -gt 0 ]; do
  case "$1" in
    -w) warn=$2; shift ;;
    -c) crit=$2; shift ;;
  esac
  shift
done
used=$(df -P / | awk 'NR==2 {gsub("%","",$5); print $5}')
if [ -z "$used" ]; then echo "DISK UNKNOWN - cannot read usage"; exit 3; fi
msg="/ ${used}% used|used=${used}%;${warn};${crit};0;100"
if [ "$used" -ge "$crit" ]; then echo "DISK CRITICAL - $msg"; exit 2; fi
if [ "$used" -ge "$warn" ]; then echo "DISK WARNING - $msg"; exit 1; fi
echo "DISK OK - $msg"
exit 0
`

// InitScriptDir creates a check-script directory with a README and an
// example the first time; an existing directory is left as it is.
func InitScriptDir(dir string) error {
	if dir == "" {
		return nil
	}
	if _, err := os.Stat(dir); err == nil {
		return nil
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "README.txt"), []byte(scriptReadme), 0o644); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		return os.WriteFile(filepath.Join(dir, "check_example"), []byte(exampleScript), 0o755)
	}
	return nil
}

// docFile is documentation kept beside the scripts, not a script.
func docFile(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".txt", ".md":
		return true
	}
	return false
}

//go:build windows

package probe

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func executable(path string, _ os.FileInfo) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".exe", ".bat", ".cmd", ".ps1":
		return true
	}
	return false
}

// pluginCmd runs .ps1 plugins through PowerShell and anything else
// directly.
func pluginCmd(ctx context.Context, path string, args []string) *exec.Cmd {
	if strings.EqualFold(filepath.Ext(path), ".ps1") {
		ps := append([]string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", path}, args...)
		return exec.CommandContext(ctx, "powershell.exe", ps...)
	}
	return exec.CommandContext(ctx, path, args...)
}

// baseEnv keeps what Windows programs need to start.
func baseEnv() []string {
	var env []string
	for _, k := range []string{"SystemRoot", "SystemDrive", "windir", "ComSpec", "PATHEXT", "TEMP", "TMP", "Path", "ProgramData", "ProgramFiles"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

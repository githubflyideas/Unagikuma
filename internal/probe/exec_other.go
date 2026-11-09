//go:build !windows

package probe

import (
	"context"
	"os"
	"os/exec"
	"syscall"
)

func executable(_ string, st os.FileInfo) bool { return st.Mode().Perm()&0o111 != 0 }

// pluginCmd runs the plugin in its own process group so a timeout also
// kills whatever it started.
func pluginCmd(ctx context.Context, path string, args []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	return cmd
}

func baseEnv() []string {
	return []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C"}
}

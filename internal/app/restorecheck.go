package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/store"
)

// backupConfigName names the configuration of a backup in messages, so a
// refusal does not point at the running installation's file.
const backupConfigName = "monitors.conf in the backup"

// CheckRestore is the check a backup unpacked into dir must pass before it
// replaces an installation, from the web UI or the restore command alike:
// its databases must be ones this version can open, and its configuration
// must have no errors (references to secrets are fine: they come with it).
func CheckRestore(dir string, opts config.Options) error {
	if err := store.CheckSchema(dir); err != nil {
		return fmt.Errorf("this backup cannot be used with this version, so nothing was changed: %v", err)
	}
	src, err := os.ReadFile(filepath.Join(dir, "monitors.conf"))
	if err != nil {
		return nil // a backup without a configuration keeps the current one
	}
	opts.SecretKnown = nil
	_, _, diags := config.Load(backupConfigName, src, opts)
	var msgs []string
	for _, d := range diags {
		if d.Severity == config.SevError && d.Code != "unknown_secret" {
			msgs = append(msgs, d.Error())
		}
	}
	if len(msgs) > 0 {
		if len(msgs) > 3 {
			msgs = append(msgs[:3], fmt.Sprintf("… and %d more", len(msgs)-3))
		}
		return fmt.Errorf("the configuration in this backup does not pass the checks of this version, so nothing was changed: %s", strings.Join(msgs, "; "))
	}
	return nil
}

// CheckRestore is the package CheckRestore with this installation's checks.
func (a *App) CheckRestore(dir string) error { return CheckRestore(dir, a.validators()) }

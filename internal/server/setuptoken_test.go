package server

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSetupTokenInAFileNotTheLog: the one-time setup token is written to
// a file only its owner can read, the log names the file and not the
// token, and the file goes once the first account exists.
func TestSetupTokenInAFileNotTheLog(t *testing.T) {
	e := newEnv(t, testBind, nil)
	var logBuf bytes.Buffer
	srv := New(e.app, Options{Log: slog.New(slog.NewTextHandler(&logBuf, nil))})
	defer srv.Close()
	tok := srv.SetupToken()
	path := filepath.Join(e.app.DataDir(), setupTokenFile)
	if tok == "" || srv.SetupTokenFile() != path {
		t.Fatalf("token %q file %q", tok, srv.SetupTokenFile())
	}
	b, err := os.ReadFile(path)
	if err != nil || strings.TrimSpace(string(b)) != tok {
		t.Fatalf("file: %q %v", b, err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %v", fi.Mode().Perm())
	}
	if strings.Contains(logBuf.String(), tok) || !strings.Contains(logBuf.String(), path) {
		t.Fatalf("log: %s", logBuf.String())
	}
	e.setup() // through the test server, which wrote the file too
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("setup token file kept after setup: %v", err)
	}
}

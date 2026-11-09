package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/githubflyideas/unagikuma/internal/config"
)

func TestAdapterImportCLI(t *testing.T) {
	for _, source := range []string{"gatus", "checkmate"} {
		t.Run(source, func(t *testing.T) {
			dir := t.TempDir()
			data := filepath.Join(dir, "data")
			report := filepath.Join(dir, "reports")
			ext := ".json"
			if source == "gatus" {
				ext = ".yaml"
			}
			input := filepath.Join("..", "..", "internal", "importer", "testdata", source+ext)
			if err := runImport([]string{source, "--dry-run", "--data", data, "--report", report, input}); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(data, "monitors.conf")); !os.IsNotExist(err) {
				t.Fatal("dry-run wrote configuration")
			}
			if err := runImport([]string{source, "--data", data, "--report", report, input}); err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(data, "monitors.conf")
			raw, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			_, cfg, ds := config.Load(p, raw, config.Options{})
			if config.HasErrors(ds) {
				t.Fatal(ds)
			}
			m := cfg.FindMonitor(source + "-example")
			if m == nil || !m.Disabled {
				t.Fatalf("monitor %+v", m)
			}
			if err := runImport([]string{source, "--data", data, "--report", report, input}); err == nil || !strings.Contains(err.Error(), "blocked") {
				t.Fatalf("collision: %v", err)
			}
			again, _ := os.ReadFile(p)
			if string(again) != string(raw) {
				t.Fatal("collision changed config")
			}
		})
	}
}

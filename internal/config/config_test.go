package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile is a helper creating a config file in a temp dir.
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadDefaultsWhenNoFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ONEGATE_HOST", "")
	t.Setenv("ONEGATE_PORT", "")
	t.Setenv("ONEGATE_DATA_DIR", "")
	t.Setenv("ONEGATE_LOG_LEVEL", "")
	t.Setenv("ONEGATE_CONFIG", "")

	cfg, err := Load(LoadOptions{HomeDir: dir})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	d := Default()
	if cfg.Host != d.Host || cfg.Port != d.Port || cfg.LogLevel != d.LogLevel {
		t.Fatalf("expected defaults, got %+v", cfg)
	}
}

func TestLoadFileOverridesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "onegate.json",
		`{"host": "0.0.0.0", "port": 9000, "log_level": "debug"}`)

	cfg, err := Load(LoadOptions{Path: path})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Host != "0.0.0.0" || cfg.Port != 9000 || cfg.LogLevel != "debug" {
		t.Fatalf("file values not applied: %+v", cfg)
	}
	// untouched fields keep defaults
	if cfg.DataDir != Default().DataDir {
		t.Fatalf("data_dir should keep default, got %q", cfg.DataDir)
	}
}

func TestLoadEnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "onegate.json", `{"port": 9000}`)
	t.Setenv("ONEGATE_PORT", "9111")
	t.Setenv("ONEGATE_HOST", "")
	t.Setenv("ONEGATE_DATA_DIR", "")
	t.Setenv("ONEGATE_LOG_LEVEL", "")

	cfg, err := Load(LoadOptions{Path: path})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != 9111 {
		t.Fatalf("env should override file: want 9111, got %d", cfg.Port)
	}
}

func TestLoadDiscoveryOrder(t *testing.T) {
	dir := t.TempDir()
	// cwd-style file first
	cwdFile := writeFile(t, dir, "onegate.json", `{"port": 1111}`)
	t.Chdir(dir)
	t.Setenv("ONEGATE_CONFIG", "")
	t.Setenv("ONEGATE_HOST", "")
	t.Setenv("ONEGATE_PORT", "")
	t.Setenv("ONEGATE_DATA_DIR", "")
	t.Setenv("ONEGATE_LOG_LEVEL", "")

	cfg, err := Load(LoadOptions{HomeDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != 1111 {
		t.Fatalf("expected cwd discovery, got port %d", cfg.Port)
	}
	_ = cwdFile

	// ONEGATE_CONFIG beats cwd
	envPath := writeFile(t, dir, "other.json", `{"port": 2222}`)
	t.Setenv("ONEGATE_CONFIG", envPath)
	cfg, err = Load(LoadOptions{HomeDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != 2222 {
		t.Fatalf("ONEGATE_CONFIG should win, got port %d", cfg.Port)
	}
}

func TestLoadExplicitPathMissingFileIsError(t *testing.T) {
	_, err := Load(LoadOptions{Path: "/nonexistent/onegate.json"})
	if err == nil {
		t.Fatal("expected error for missing explicit path")
	}
}

func TestLoadSyntaxErrorCarriesLine(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "onegate.json",
		"{\n  \"port\": not-a-number\n}\n")
	_, err := Load(LoadOptions{Path: path})
	if err == nil {
		t.Fatal("expected syntax error")
	}
	msg := err.Error()
	if !strings.Contains(msg, ":2:") {
		t.Fatalf("error should carry line info, got: %s", msg)
	}
}

func TestLoadTypeErrorNamesField(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "onegate.json", `{"port": "abc"}`)
	_, err := Load(LoadOptions{Path: path})
	if err == nil {
		t.Fatal("expected type error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "port") {
		t.Fatalf("error should name the field, got: %s", msg)
	}
}

func TestValidateFieldErrors(t *testing.T) {
	cases := []struct {
		name  string
		mut   func(*Config)
		field string
	}{
		{"bad port", func(c *Config) { c.Port = 0 }, "port"},
		{"bad log level", func(c *Config) { c.LogLevel = "loud" }, "log_level"},
		{"empty data dir", func(c *Config) { c.DataDir = " " }, "data_dir"},
		{"fast poll", func(c *Config) { c.Reload.PollMS = 10 }, "reload.poll_ms"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			tc.mut(&cfg)
			err := Validate(cfg)
			ve, ok := err.(*ValidationError)
			if !ok {
				t.Fatalf("expected ValidationError, got %v", err)
			}
			if ve.Path != tc.field {
				t.Fatalf("expected field %q, got %q", tc.field, ve.Path)
			}
		})
	}
}

func TestDerivedPaths(t *testing.T) {
	cfg := Default()
	cfg.DataDir = "/data"
	if got, want := cfg.DBPath(), "/data/onegate.db"; got != want {
		t.Fatalf("DBPath: want %q got %q", want, got)
	}
	if got, want := cfg.MasterKeyPath(), "/data/master.key"; got != want {
		t.Fatalf("MasterKeyPath: want %q got %q", want, got)
	}
}

// Package config defines OneGate runtime configuration and its loading
// pipeline: defaults <- file <- environment <- flags (highest wins).
//
// The on-disk format is JSON ("onegate.json"), matching the OmniRoute
// v3.8.52 configuration format so import is a copy rather than a
// conversion. See docs/adr/002-config-format.md.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config holds the resolved runtime configuration for the gateway.
type Config struct {
	// Host is the address the HTTP server binds to.
	Host string `json:"host"`

	// Port is the TCP port the HTTP server binds to.
	Port int `json:"port"`

	// DataDir is the directory for the SQLite database and runtime state.
	DataDir string `json:"data_dir"`

	// LogLevel is one of: debug | info | warn | error.
	LogLevel string `json:"log_level"`

	// Reload controls the hot-reload watcher (p1.config-hotreload).
	Reload ReloadConfig `json:"reload"`
}

// ReloadConfig tunes the config watcher.
type ReloadConfig struct {
	// Enabled turns on mtime polling + SIGHUP reload.
	Enabled bool `json:"enabled"`

	// PollMS is the file poll interval in milliseconds.
	PollMS int `json:"poll_ms"`
}

// Default returns the built-in defaults used when no configuration is given.
func Default() Config {
	return Config{
		Host:     "127.0.0.1",
		Port:     7420,
		DataDir:  ".onegate",
		LogLevel: "info",
		Reload: ReloadConfig{
			Enabled: true,
			PollMS:  2000,
		},
	}
}

// DBPath returns the absolute path of the SQLite database inside DataDir.
func (c Config) DBPath() string {
	return filepath.Join(c.DataDir, "onegate.db")
}

// MasterKeyPath returns the path of the master-secret file inside DataDir.
func (c Config) MasterKeyPath() string {
	return filepath.Join(c.DataDir, "master.key")
}

// ConfigPath returns the path of the config file inside DataDir (the
// canonical location once the gateway owns a data directory).
func (c Config) ConfigPath() string {
	return filepath.Join(c.DataDir, "onegate.json")
}

// LoadOptions parameterize Load.
type LoadOptions struct {
	// Path is an explicit config file path (flag). When set it is the
	// only file consulted; missing file is an error.
	Path string

	// HomeDir overrides the user home directory in discovery (tests).
	HomeDir string
}

// Load resolves configuration with precedence: defaults <- file <- env.
// Flags are applied by the caller (cmd/onegate) on top of the result.
//
// File discovery when no explicit path is given: $ONEGATE_CONFIG, then
// ./onegate.json, then <home>/.onegate/onegate.json. The first existing
// file wins; having none is not an error (defaults + env apply).
func Load(opts LoadOptions) (Config, error) {
	cfg := Default()

	path, err := discoverPath(opts)
	if err != nil {
		return Config{}, err
	}
	if path != "" {
		fileCfg, err := loadFile(path)
		if err != nil {
			return Config{}, err
		}
		mergeFile(&cfg, fileCfg)
	}

	applyEnv(&cfg)

	if err := Validate(cfg); err != nil {
		return Config{}, fmt.Errorf("config %s: %w", displayPath(path), err)
	}
	return cfg, nil
}

// discoverPath returns the config file to load ("" = none).
func discoverPath(opts LoadOptions) (string, error) {
	if opts.Path != "" {
		if _, err := os.Stat(opts.Path); err != nil {
			return "", fmt.Errorf("config file %s: %w", opts.Path, err)
		}
		return opts.Path, nil
	}
	if p := os.Getenv("ONEGATE_CONFIG"); p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("ONEGATE_CONFIG=%s: %w", p, err)
		}
		return p, nil
	}
	candidates := []string{"onegate.json"}
	if home := opts.HomeDir; home != "" {
		candidates = append(candidates, filepath.Join(home, ".onegate", "onegate.json"))
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", nil
}

// fileConfig is the on-disk shape: every field optional (pointers), so a
// partial file overrides only what it mentions.
type fileConfig struct {
	Host     *string        `json:"host"`
	Port     *int           `json:"port"`
	DataDir  *string        `json:"data_dir"`
	LogLevel *string        `json:"log_level"`
	Reload   *reloadSection `json:"reload"`
}

type reloadSection struct {
	Enabled *bool `json:"enabled"`
	PollMS  *int  `json:"poll_ms"`
}

// loadFile reads and parses a JSON config file. Syntax errors carry the
// offending line and column.
func loadFile(path string) (fileConfig, error) {
	var fc fileConfig
	data, err := os.ReadFile(path)
	if err != nil {
		return fc, fmt.Errorf("config file %s: %w", path, err)
	}
	if err := jsonUnmarshalStrict(path, data, &fc); err != nil {
		return fc, err
	}
	return fc, nil
}

// mergeFile overlays file values onto cfg.
func mergeFile(cfg *Config, fc fileConfig) {
	if fc.Host != nil {
		cfg.Host = *fc.Host
	}
	if fc.Port != nil {
		cfg.Port = *fc.Port
	}
	if fc.DataDir != nil {
		cfg.DataDir = *fc.DataDir
	}
	if fc.LogLevel != nil {
		cfg.LogLevel = *fc.LogLevel
	}
	if fc.Reload != nil {
		if fc.Reload.Enabled != nil {
			cfg.Reload.Enabled = *fc.Reload.Enabled
		}
		if fc.Reload.PollMS != nil {
			cfg.Reload.PollMS = *fc.Reload.PollMS
		}
	}
}

// applyEnv overlays ONEGATE_* environment variables onto cfg.
func applyEnv(cfg *Config) {
	if v := os.Getenv("ONEGATE_HOST"); v != "" {
		cfg.Host = v
	}
	if v := os.Getenv("ONEGATE_PORT"); v != "" {
		if p, err := parsePort(v); err == nil {
			cfg.Port = p
		}
	}
	if v := os.Getenv("ONEGATE_DATA_DIR"); v != "" {
		cfg.DataDir = v
	}
	if v := os.Getenv("ONEGATE_LOG_LEVEL"); v != "" {
		cfg.LogLevel = strings.ToLower(v)
	}
}

func displayPath(path string) string {
	if path == "" {
		return "(defaults)"
	}
	return path
}

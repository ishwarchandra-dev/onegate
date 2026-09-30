// Package config defines OneGate runtime configuration.
//
// Phase 0 defines the shape only. Loading from file + environment, schema
// validation and hot reload land in Phase 1 (graph node p1.config-load).
package config

// Config holds the resolved runtime configuration for the gateway.
type Config struct {
	// Host is the address the HTTP server binds to.
	Host string

	// Port is the TCP port the HTTP server binds to.
	Port int

	// DataDir is the directory for the SQLite database and runtime state.
	DataDir string

	// LogLevel is one of: debug | info | warn | error.
	LogLevel string
}

// Default returns the built-in defaults used when no configuration is given.
func Default() Config {
	return Config{
		Host:     "127.0.0.1",
		Port:     7420,
		DataDir:  ".onegate",
		LogLevel: "info",
	}
}

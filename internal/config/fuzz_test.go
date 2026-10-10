package config

// p8.fuzzing: config-file parsing fuzz target. The full file pipeline
// (strict JSON decode with error enrichment -> merge -> validation)
// must survive arbitrary bytes without panic, and a config that passes
// Validate must satisfy its own invariants.
import "testing"

func FuzzConfigFile(f *testing.F) {
	seeds := []string{
		`{"host":"127.0.0.1","port":8080,"data_dir":"~/.onegate","log_level":"info"}`,
		`{"http":{"read_header_timeout_ms":10000,"read_timeout_ms":0,"write_timeout_ms":0,"idle_timeout_ms":120000}}`,
		`{"reload":{"enabled":true,"poll_ms":2000}}`,
		`{"port":"not-a-number"}`,
		`{"http":{"read_header_timeout_ms":-5}}`,
		`{"log_level":"loud"}`,
		`{"unknown_field":{"nested":[1,2,3]}}`,
		`{`,
		``,
		`null`,
		`[]`,
		`{"port":99999,"data_dir":"x","host":"h","log_level":"debug","reload":{"poll_ms":100},"http":{"read_header_timeout_ms":1}}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var fc fileConfig
		if err := jsonUnmarshalStrict("fuzz.json", data, &fc); err != nil {
			return // parse rejection is fine
		}
		cfg := Default()
		mergeFile(&cfg, fc)
		if err := Validate(cfg); err != nil {
			return // validation rejection is fine
		}
		// A config that passed Validate must honor its own contract.
		if cfg.Port < 1 || cfg.Port > 65535 {
			t.Fatalf("Validate passed but port %d out of range", cfg.Port)
		}
		switch cfg.LogLevel {
		case "debug", "info", "warn", "error":
		default:
			t.Fatalf("Validate passed but log_level %q invalid", cfg.LogLevel)
		}
		if cfg.HTTP.ReadHeaderTimeoutMS <= 0 {
			t.Fatalf("Validate passed but slowloris guard disabled (%d)", cfg.HTTP.ReadHeaderTimeoutMS)
		}
	})
}

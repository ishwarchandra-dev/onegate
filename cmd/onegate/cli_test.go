package main

// p9.cli-polish acceptance tests: help screens, usage-error exit codes,
// the config subcommand, and the exit-code contract (0/1/2).
//
// Two layers:
//   - unit: run()/runConfigCmd called in-process (fast, precise)
//   - subprocess: the real binary is built once (TestMain) and invoked
//     with argument matrices, asserting actual process exit codes —
//     that is the contract scripts depend on.
import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// --- unit: plumbing ------------------------------------------------------

func TestHelpRequested(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"short", []string{"-h"}, true},
		{"long", []string{"--help"}, true},
		{"single-dash-help", []string{"-help"}, true},
		{"after flags", []string{"-port", "1", "-h"}, true},
		{"terminator stops scan", []string{"--", "-h"}, false},
		{"plain args", []string{"serve", "x"}, false},
		{"none", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := helpRequested(tc.args); got != tc.want {
				t.Fatalf("helpRequested(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

func TestUsageErrorMapping(t *testing.T) {
	err := newUsageErrorf("boom %d", 42)
	if !isUsageError(err) {
		t.Fatal("expected usage error")
	}
	if err.Error() != "boom 42" {
		t.Fatalf("message: %q", err.Error())
	}
	if isUsageError(errors.New("no")) {
		t.Fatal("plain error must not map to exit 2")
	}
	if !isFlagHelp(flag.ErrHelp) {
		t.Fatal("flag.ErrHelp should be recognized")
	}
}

func TestRunDispatchUnknownCommand(t *testing.T) {
	err := run([]string{"frobnicate"})
	if !isUsageError(err) {
		t.Fatalf("unknown command must be a usage error (exit 2), got %v", err)
	}
	if !strings.Contains(err.Error(), "frobnicate") || !strings.Contains(err.Error(), "onegate help") {
		t.Fatalf("message should name the command and the help pointer: %q", err.Error())
	}
}

func TestRunDispatchFlagsStayServe(t *testing.T) {
	// A leading token starting with "-" must NOT be treated as an
	// unknown command: it belongs to the implicit serve path. -version
	// proves the path (no server boot needed).
	if err := run([]string{"-version"}); err != nil {
		t.Fatalf("-version through implicit serve: %v", err)
	}
}

// --- unit: config subcommand ----------------------------------------------

func TestRunConfigCmdJSON(t *testing.T) {
	var out bytes.Buffer
	err := runConfigCmd([]string{"-port", "9876", "-log-level", "warn"}, &out)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out.String())
	}
	if got["port"] != float64(9876) {
		t.Fatalf("port: got %v, want 9876 (flag must win)", got["port"])
	}
	if got["log_level"] != "warn" {
		t.Fatalf("log_level: %v", got["log_level"])
	}
	httpSec, ok := got["http"].(map[string]any)
	if !ok {
		t.Fatalf("http section missing: %s", out.String())
	}
	if httpSec["read_header_timeout_ms"] == nil {
		t.Fatal("http.read_header_timeout_ms missing")
	}
	// no secrets by construction: the set of top-level keys is fixed
	for _, k := range []string{"host", "port", "data_dir", "log_level", "http", "reload"} {
		if _, ok := got[k]; !ok {
			t.Fatalf("expected key %q in %v", k, keysOf(got))
		}
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestRunConfigCmdPositionalRefused(t *testing.T) {
	err := runConfigCmd([]string{"extra"}, os.Stdout)
	if !isUsageError(err) {
		t.Fatalf("positional arg must be usage error, got %v", err)
	}
}

// --- subprocess: the real exit-code contract -------------------------------

var testBin string // built once by TestMain

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "ogcli-*")
	if err != nil {
		panic(err)
	}
	testBin = filepath.Join(dir, "onegate")
	build := exec.Command("go", "build", "-o", testBin, "github.com/ishwarchandra-dev/onegate/cmd/onegate")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		panic("building test binary: " + err.Error())
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// runCLI executes the built binary with a scrubbed environment (no
// ONEGATE_* leakage from the dev box) plus opt extra env; returns
// exit code, stdout, stderr.
func runCLI(t *testing.T, args []string, extraEnv map[string]string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(testBin, args...)
	env := []string{}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "ONEGATE_") {
			continue
		}
		if strings.HasPrefix(kv, "HOME=") {
			continue // isolate ~/.onegate discovery noise
		}
		env = append(env, kv)
	}
	env = append(env, "HOME="+t.TempDir())
	for k, v := range extraEnv {
		env = append(env, k+"="+v)
	}
	cmd.Env = env
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run %v: %v", args, err)
	}
	return code, out.String(), errb.String()
}

func TestCLIExitCodesAndHelp(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		env      map[string]string
		wantCode int
		stdout   []string // substrings that must appear
		stderr   []string
	}{
		{
			name: "top help via -h", args: []string{"-h"}, wantCode: 0,
			stdout: []string{"Usage:", "serve", "import-keys", "config", "docs/cli.md"},
		},
		{
			name: "top help via help", args: []string{"help"}, wantCode: 0,
			stdout: []string{"Commands:", "-version"},
		},
		{
			name: "serve help", args: []string{"serve", "-h"}, wantCode: 0,
			stdout: []string{"onegate serve", "-data-dir", "-log-level"},
		},
		{
			name: "import help", args: []string{"import", "-h"}, wantCode: 0,
			stdout: []string{"onegate import", "--apply", "omniroute.json"},
		},
		{
			name: "import-keys help", args: []string{"import-keys", "-h"}, wantCode: 0,
			stdout: []string{"--usage-db", "--keys-out", "0600"},
		},
		{
			name: "config help", args: []string{"config", "-h"}, wantCode: 0,
			stdout: []string{"onegate config", "fully resolved"},
		},
		{
			name: "unknown command is exit 2", args: []string{"nope"}, wantCode: 2,
			stderr: []string{"unknown command", "onegate help"},
		},
		{
			name: "bad flag is exit 2", args: []string{"serve", "--nope"}, wantCode: 2,
			stderr: []string{"flag provided but not defined"},
		},
		{
			name: "import missing file arg is exit 2", args: []string{"import"}, wantCode: 2,
			stderr: []string{"exactly one"},
		},
		{
			name: "config positional is exit 2", args: []string{"config", "x"}, wantCode: 2,
			stderr: []string{"no positional"},
		},
		{
			name: "version flag exits 0", args: []string{"-version"}, wantCode: 0,
			stdout: []string{"(commit"},
		},
		{
			name:     "config prints resolved JSON honoring env then flags",
			args:     []string{"config", "-port", "9876"},
			env:      map[string]string{"ONEGATE_PORT": "9123", "ONEGATE_LOG_LEVEL": "debug"},
			wantCode: 0,
			stdout:   []string{`"port": 9876`, `"log_level": "debug"`},
		},
		{
			name: "config env beats defaults", args: []string{"config"},
			env:      map[string]string{"ONEGATE_DATA_DIR": "/tmp/og-x"},
			wantCode: 0,
			stdout:   []string{`"data_dir": "/tmp/og-x"`, `"read_header_timeout_ms": 10000`},
		},
		{
			name: "config invalid log-level is runtime exit 1", args: []string{"config", "-log-level", "loud"},
			wantCode: 1,
			stderr:   []string{"onegate:"},
		},
		{
			name: "config bad file is runtime exit 1", args: []string{"config", "-config", "/nonexistent/onegate.json"},
			wantCode: 1,
			stderr:   []string{"config file"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errb := runCLI(t, tc.args, tc.env)
			if code != tc.wantCode {
				t.Fatalf("exit code = %d, want %d\nstdout:\n%s\nstderr:\n%s", code, tc.wantCode, out, errb)
			}
			for _, s := range tc.stdout {
				if !strings.Contains(out, s) {
					t.Errorf("stdout missing %q\nstdout:\n%s", s, out)
				}
			}
			for _, s := range tc.stderr {
				if s == "" {
					continue
				}
				if !strings.Contains(errb, s) {
					t.Errorf("stderr missing %q\nstderr:\n%s", s, errb)
				}
			}
		})
	}
}

func TestCLIHelpScreensListEveryFlag(t *testing.T) {
	// Every flag registered in code must appear in its command's -h
	// output (the docs cross-check is scripts/check_cli_docs.py).
	for _, tc := range []struct{ args []string }{
		{[]string{"serve", "-h"}},
		{[]string{"import", "-h"}},
		{[]string{"import-keys", "-h"}},
		{[]string{"config", "-h"}},
	} {
		code, out, _ := runCLI(t, tc.args, nil)
		if code != 0 {
			t.Fatalf("%v: exit %d", tc.args, code)
		}
		for _, name := range flagNamesOf(t, tc.args[0]) {
			if !strings.Contains(out, "-"+name) {
				t.Errorf("%v help does not mention flag -%s", tc.args, name)
			}
		}
	}
}

// flagNamesOf returns the registered flag names for a subcommand by
// inspecting the in-process flag sets (single source with the CLI).
func flagNamesOf(t *testing.T, command string) []string {
	t.Helper()
	var fs *flag.FlagSet
	switch command {
	case "serve":
		fs, _ = serveFlags()
	case "import":
		fs, _ = importFlags()
	case "import-keys":
		fs, _ = importKeysFlags()
	case "config":
		fs, _ = configFlags()
	default:
		t.Fatalf("unknown command %q", command)
	}
	names := []string{}
	fs.VisitAll(func(f *flag.Flag) { names = append(names, f.Name) })
	if len(names) == 0 {
		t.Fatalf("no flags registered for %q", command)
	}
	return names
}

func TestCLIUsageErrorGoesToStderrNotStdout(t *testing.T) {
	code, out, errb := runCLI(t, []string{"nope"}, nil)
	if code != 2 {
		t.Fatalf("code %d", code)
	}
	if out != "" {
		t.Fatalf("usage errors must not print to stdout: %q", out)
	}
	if !strings.Contains(errb, "onegate:") {
		t.Fatalf("stderr: %q", errb)
	}
}

func TestConfigPortFlagRange(t *testing.T) {
	// port must be 1..65535 via config validation; 0 keeps the default
	var out bytes.Buffer
	if err := runConfigCmd([]string{"-port", strconv.Itoa(65535)}, &out); err != nil {
		t.Fatalf("65535 should be valid: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(out.Bytes(), &got)
	if got["port"] != float64(65535) {
		t.Fatalf("port = %v", got["port"])
	}
}

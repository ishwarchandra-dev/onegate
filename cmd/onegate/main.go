// Command onegate is the entrypoint for the OneGate LLM gateway.
//
// OneGate is the Go rewrite of OmniRoute v3.8.52: a single-binary LLM
// gateway with an embedded management dashboard, provider routing,
// fallback chains and usage analytics.
//
// CLI surface (p9.cli-polish; full reference in docs/cli.md):
//
//	onegate serve [flags]        run the gateway (default when no
//	                             subcommand is given — `onegate [flags]`
//	                             keeps working)
//	onegate import <file>        migrate an OmniRoute install (dry-run
//	                             by default, --apply to write)
//	onegate import-keys <file>   migrate virtual keys + usage history
//	onegate config [flags]       print the fully resolved runtime
//	                             configuration as JSON and exit
//	onegate -version             print version and exit
//	onegate help | -h            help
//
// Exit codes: 0 success (incl. -h), 1 runtime error, 2 usage error.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ishwarchandra-dev/onegate/internal/api"
	"github.com/ishwarchandra-dev/onegate/internal/auth"
	"github.com/ishwarchandra-dev/onegate/internal/config"
	"github.com/ishwarchandra-dev/onegate/internal/observability"
	"github.com/ishwarchandra-dev/onegate/internal/proxy/client"
	"github.com/ishwarchandra-dev/onegate/internal/proxy/fallback"
	"github.com/ishwarchandra-dev/onegate/internal/proxy/ingest"
	"github.com/ishwarchandra-dev/onegate/internal/ratelimit"
	"github.com/ishwarchandra-dev/onegate/internal/routing"
	"github.com/ishwarchandra-dev/onegate/internal/server"
	"github.com/ishwarchandra-dev/onegate/internal/storage"
	"github.com/ishwarchandra-dev/onegate/internal/version"
	"github.com/ishwarchandra-dev/onegate/internal/webfs"
	web "github.com/ishwarchandra-dev/onegate/web"
)

func main() {
	err := run(os.Args[1:])
	if err == nil {
		return
	}
	code := 1
	var ue *usageError
	if errors.As(err, &ue) {
		code = 2
	}
	fmt.Fprintf(os.Stderr, "onegate: %v\n", err)
	os.Exit(code)
}

// run dispatches subcommands. A first token starting with "-" is a flag
// for the implicit serve path (`onegate -port 8000` still serves);
// anything else that is not a known subcommand is a usage error.
func run(args []string) error {
	if len(args) > 0 {
		cmd, rest := args[0], args[1:]
		switch cmd {
		case "import":
			return runImport(rest)
		case "import-keys":
			return runImportKeys(rest)
		case "serve":
			return runServe(rest)
		case "config":
			return runConfigCmd(rest, os.Stdout)
		case "help", "-h", "--help":
			printTopHelp(os.Stdout)
			return nil
		}
		if !strings.HasPrefix(cmd, "-") {
			return newUsageErrorf("unknown command %q — run `onegate help` for the command list", cmd)
		}
	}
	return runServe(args)
}

// printTopHelp renders the command list (onegate help / onegate -h).
func printTopHelp(w io.Writer) {
	fmt.Fprintf(w, `onegate — single-binary LLM gateway (OpenAI / Anthropic / Gemini, one endpoint)

Usage:
  onegate <command> [flags]
  onegate [flags]                 same as "onegate serve" (back-compat)

Commands:
  serve           run the gateway (dashboard + API + proxy on one port)
  import          migrate a legacy OmniRoute install into this data dir
  import-keys     migrate legacy virtual keys and usage history
  config          print the fully resolved runtime configuration
  help            show this list; "<command> -h" shows command help

Flags:
  -version        print version and exit

Examples:
  onegate serve -port 8000 -log-level debug
  onegate config | jq .http
  onegate import omniroute.json            # dry-run plan
  onegate import omniroute.json --apply    # write it

Full reference: docs/cli.md
`)
}

// runServe wires and runs the gateway (the composition root).
func runServe(args []string) error {
	if helpRequested(args) {
		fs, _ := serveFlags()
		printHelp(fs, os.Stdout)
		return nil
	}
	fs, opts := serveFlags()
	if err := fs.Parse(args); err != nil {
		if isFlagHelp(err) {
			printHelp(fs, os.Stdout)
			return nil
		}
		return newUsageErrorf("%v", err)
	}
	if opts.version {
		fmt.Println(version.String())
		return nil
	}

	// --- config: defaults <- file <- env, then flag overrides ---------
	cfg, err := config.Load(config.LoadOptions{Path: opts.config})
	if err != nil {
		return err
	}
	if opts.host != "" {
		cfg.Host = opts.host
	}
	if opts.port != 0 {
		cfg.Port = opts.port
	}
	if opts.dataDir != "" {
		cfg.DataDir = opts.dataDir
	}
	if opts.logLevel != "" {
		cfg.LogLevel = opts.logLevel
	}
	if err := config.Validate(cfg); err != nil {
		return err
	}

	return serve(cfg, opts.config)
}

// serveOpts holds the parsed serve/config flag values (bound by
// serveFlags so -h and parsing share one definition).
type serveOpts struct {
	host     string
	port     int
	dataDir  string
	logLevel string
	config   string
	version  bool
}

// serveFlags builds the serve flag set (shared by -h and parse).
func serveFlags() (*flag.FlagSet, *serveOpts) {
	fs := flag.NewFlagSet("onegate serve", flag.ContinueOnError)
	o := &serveOpts{}
	fs.StringVar(&o.host, "host", "", "address to listen on (default 127.0.0.1; overrides config/env)")
	fs.IntVar(&o.port, "port", 0, "port to listen on (default 7420; overrides config/env)")
	fs.StringVar(&o.dataDir, "data-dir", "", "data directory for onegate.db + master.key (default .onegate; overrides config/env)")
	fs.StringVar(&o.logLevel, "log-level", "", "debug | info | warn | error (default info; overrides config/env)")
	fs.StringVar(&o.config, "config", "", "explicit config file path (default: $ONEGATE_CONFIG, ./onegate.json, ~/.onegate/onegate.json)")
	fs.BoolVar(&o.version, "version", false, "print version and exit")
	fs.Usage = helpScreen(fs, "serve", "run the gateway: dashboard, API and proxy on one port",
		"onegate [serve] [-host H] [-port P] [-data-dir DIR] [-log-level L] [-config FILE]",
		[]string{
			"onegate serve -port 8000 -log-level debug",
			"onegate -data-dir /var/lib/onegate   # flags work without the subcommand too",
		})
	return fs, o
}

// serve boots storage, auth, routing, the proxy and the HTTP server,
// then blocks until SIGINT/SIGTERM. This is the Phase 1-9 composition
// root; it is the only place allowed to import everything.
func serve(cfg config.Config, flagConfig string) error {
	logHub := observability.NewLogHub(1000)
	logger := observability.NewLoggerWithHub(cfg.LogLevel, os.Stdout, logHub)

	if err := config.EnsureDataDir(cfg); err != nil {
		return err
	}

	// --- storage: open + migrate --------------------------------------
	store, err := storage.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		return err
	}
	schemaVer, _ := store.SchemaVersion()

	// --- hot reload -----------------------------------------------------
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var watcher *config.Watcher
	if cfg.Reload.Enabled {
		watcher = config.NewWatcher(flagConfig, cfg, cfg.Reload.PollMS, logger)
		watcher.Start(ctx)
		defer watcher.Stop()
	}

	// --- auth: master secret -> verifier ------------------------------
	master, err := auth.MasterSecret(cfg.MasterKeyPath())
	if err != nil {
		return err
	}
	pepper, err := auth.Pepper(master)
	if err != nil {
		return err
	}
	verifier := auth.NewVerifier(store, pepper)

	// --- auth: provider-key cipher + virtual-key manager (p6) ---------
	providerCipher, err := auth.NewCipher(master, auth.PurposeProviderKeys)
	if err != nil {
		return err
	}
	keyManager := auth.NewManager(store, pepper)

	// --- routing: registry + storage watcher + health (p4, wired p6) --
	// The watcher polls storage and hot-swaps the registry snapshot, so
	// dashboard writes (providers/models/rules) propagate to routing
	// without a restart.
	routingRegistry := routing.NewRegistry()
	routingWatcher := routing.NewWatcher(routing.WatcherConfig{
		Registry:     routingRegistry,
		Source:       store.RoutingSource(),
		PollInterval: time.Second,
		Logger:       logger,
	})
	routingWatcher.Start(ctx)
	defer routingWatcher.Stop()
	healthTracker := routing.NewHealthTracker(routing.DefaultHealthConfig())

	// --- usage pipeline: bounded queue + background writer (Phase 5) --
	prices := ratelimit.NewPriceTable()
	usagePipeline := observability.NewUsagePipeline(observability.UsagePipelineConfig{
		QueueSize:     10_000,
		BatchSize:     100,
		FlushInterval: 100 * time.Millisecond,
		Writer:        store.Requests(),
		Prices:        prices,
		Logger:        logger,
	})
	usagePipeline.Start(ctx)
	defer usagePipeline.Stop()

	// --- metrics: Prometheus registry (p5.metrics, admin-gated) -------
	metricsRegistry := observability.NewRegistry(observability.MetricsConfig{
		AdminToken: os.Getenv("ONEGATE_ADMIN_TOKEN"),
	})

	// --- proxy: upstream client + routing data plane (p7.parity-fixes) --
	upstreamClient := client.New(client.TransportConfig{}, nil)
	defer upstreamClient.CloseIdleConnections()

	// The data plane routes through the Phase 4 engine: registry snapshot
	// (models, scopes, policies, circuit health) + per-provider
	// credentials from storage (TTL-cached, hot-path safe).
	credentials := newProviderCredentials(store, providerCipher, 15*time.Second)
	resolver := &routingResolver{
		registry:    routingRegistry,
		health:      healthTracker,
		credentials: credentials,
	}

	quotaManager := ratelimit.NewQuotaManager(prices)

	// qp is declared before the engine so the engine's OnUsage chain can
	// debit quota through it (assigned right after the engine).
	var qp *quotaProxy

	fallbackEngine := fallback.NewEngine(fallback.Config{
		Resolver: resolver,
		Client:   upstreamClient,
		Logger:   logger,
		OnUsage: func(ue fallback.UsageEvent) {
			usagePipeline.EnqueueEvent(ue.ToObservabilityEvent())
			if qp != nil {
				qp.onUsage(ue)
			}
			metricsRegistry.ObserveProxyRequest(
				string(ue.Protocol),
				ue.ProviderID,
				ue.ModelServed,
				string(ue.Status),
				ue.Duration,
			)
			if ue.Stream && ue.TTFT > 0 {
				metricsRegistry.ObserveTTFT(ue.ProviderID, ue.ModelServed, ue.TTFT)
			}
			metricsRegistry.SetUsagePipelineStats(usagePipeline.Stats())
		},
		OnTrace: func(tr fallback.Trace) {
			// Feed the circuit breaker: every failed attempt trips a
			// failure; the winning target confirms recovery.
			for _, at := range tr.Attempts {
				if at.Error != nil {
					healthTracker.RecordFailure(at.ProviderID, at.Model, string(at.Error.Type))
					continue
				}
				if tr.Completed {
					healthTracker.RecordSuccess(at.ProviderID, at.Model)
				}
			}
		},
	})

	// Quota enforcement wraps the engine (checklist C-6..C-9): RPM/TPM/
	// concurrency/spend gates render 429 + Retry-After before any
	// upstream work; actual usage debits after completion.
	qp = newQuotaProxy(fallbackEngine, quotaManager, metricsRegistry)

	// --- embedded dashboard (p9.embed-pipeline, ADR 006) ---------------
	// Built before the router so it can own the exact root via the
	// Options.Root seam (replacing the pre-dashboard landing page);
	// the method-less "/" catch-all registered below serves every other
	// unmatched path with the SPA fallback (index.html). /healthz,
	// /metrics, /api/*, /v1/* and the gemini paths are more specific
	// mux patterns and win — one port serves dashboard + API + proxy.
	// The catch-all is method-less (not "GET /") and webfs answers
	// non-GET/HEAD with 404, so would-be-404s stay 404s (parity
	// A-12/CC-14: POST /v1/messages/ must not silently match).
	dashboard := webfs.New(web.Dist())
	if dashboard.IsPlaceholder() {
		logger.Warn("dashboard not embedded: serving placeholder (run: make web && make build)")
	}

	router := server.New(server.Options{
		Logger:  logger,
		Metrics: metricsRegistry,
		Root:    dashboard,
		Timeouts: server.Timeouts{
			ReadHeader: time.Duration(cfg.HTTP.ReadHeaderTimeoutMS) * time.Millisecond,
			Read:       time.Duration(cfg.HTTP.ReadTimeoutMS) * time.Millisecond,
			Write:      time.Duration(cfg.HTTP.WriteTimeoutMS) * time.Millisecond,
			Idle:       time.Duration(cfg.HTTP.IdleTimeoutMS) * time.Millisecond,
		},
		Version:       version.Version,
		SchemaVersion: schemaVer,
	})
	router.Mux().Handle("/", dashboard)
	ingest.Register(router.Mux(), ingest.Deps{
		Auth:   verifier,
		Proxy:  qp,
		Models: &registryModelLister{registry: routingRegistry},
	})

	// --- management API (p6.api-impl + p6.auth-sessions) ---------------
	// Registered after the proxy endpoints; owns the whole /api/* subtree
	// including the SSE log feed. Dashboard sessions are in-memory (a
	// restart logs the dashboard out; the admin account is durable).
	startedMS := time.Now().UnixMilli()
	sessionStore := api.NewSessionManager(api.SessionTTLMS, nil)
	api.Register(router.Mux(), api.Options{
		Logger:             logger,
		Store:              store,
		Keys:               keyManager,
		ProviderCipher:     providerCipher,
		AdminToken:         os.Getenv("ONEGATE_ADMIN_TOKEN"),
		Sessions:           sessionStore,
		PasswordHasher:     nil, // production PBKDF2 parameters
		PasswordIterations: 0,   // default (auth.DefaultPasswordIterations)
		LogHub:             logHub,
		Health:             healthTrackerView{tracker: healthTracker},
		Config: func() api.SystemConfig {
			return api.SystemConfig{
				Host:     cfg.Host,
				Port:     cfg.Port,
				DataDir:  cfg.DataDir,
				LogLevel: cfg.LogLevel,
				HTTP: api.HTTPTimeouts{
					ReadHeaderTimeoutMS: cfg.HTTP.ReadHeaderTimeoutMS,
					ReadTimeoutMS:       cfg.HTTP.ReadTimeoutMS,
					WriteTimeoutMS:      cfg.HTTP.WriteTimeoutMS,
					IdleTimeoutMS:       cfg.HTTP.IdleTimeoutMS,
				},
				Reload: api.ReloadInfo{Enabled: cfg.Reload.Enabled, PollMS: cfg.Reload.PollMS},
			}
		},
		StartedMS:     startedMS,
		SchemaVersion: schemaVer,
		Prober:        upstreamClient,
	})

	srv := router.Server(fmt.Sprintf("%s:%d", cfg.Host, cfg.Port))

	errCh := make(chan error, 1)
	go func() {
		logger.Info("onegate listening",
			"addr", srv.Addr, "version", version.String(),
			"data_dir", cfg.DataDir, "schema_version", schemaVer)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		logger.Info("shutting down gracefully")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// healthTrackerView adapts routing.HealthTracker onto the management
// API's HealthView interface (composition-level glue; the api package
// stays decoupled from routing internals beyond the view types).
type healthTrackerView struct{ tracker *routing.HealthTracker }

// State reports the circuit state for one (provider, model) target.
func (v healthTrackerView) State(providerID, model string) routing.CircuitState {
	return v.tracker.GetState(providerID, model)
}

// Events returns the most recent health transitions (newest last).
func (v healthTrackerView) Events(limit int) []routing.HealthEvent {
	return v.tracker.Events(limit)
}

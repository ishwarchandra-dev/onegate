// Command onegate is the entrypoint for the OneGate LLM gateway.
//
// OneGate is the Go rewrite of OmniRoute v3.8.52: a single-binary LLM
// gateway with an embedded management dashboard, provider routing,
// fallback chains and usage analytics.
//
// Phase 1 wires the foundation: config resolution (flags > env > file >
// defaults) with hot reload, structured redacting logs, and the embedded
// SQLite store with schema migrations. The proxy core lands in Phase 3
// (see tasks/phase-3.proxy-core.graph.yaml).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
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
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "onegate: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// --- subcommands ----------------------------------------------------
	// `onegate import <path>` migrates a legacy OmniRoute install
	// (p7.config-import); everything else serves.
	switch {
	case len(os.Args) > 1 && os.Args[1] == "import":
		return runImport(os.Args[2:])
	case len(os.Args) > 1 && os.Args[1] == "import-keys":
		return runImportKeys(os.Args[2:])
	}

	// --- flags (highest precedence) -----------------------------------
	fs := flag.NewFlagSet("onegate", flag.ContinueOnError)
	var (
		flagHost     = fs.String("host", "", "address to listen on (overrides config/env)")
		flagPort     = fs.Int("port", 0, "port to listen on (overrides config/env)")
		flagDataDir  = fs.String("data-dir", "", "data directory (overrides config/env)")
		flagLogLevel = fs.String("log-level", "", "debug|info|warn|error (overrides config/env)")
		flagConfig   = fs.String("config", "", "explicit config file path (default: discovery)")
		showVersion  = fs.Bool("version", false, "print version and exit")
	)
	if err := fs.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *showVersion {
		fmt.Println(version.String())
		return nil
	}

	// --- config: defaults <- file <- env, then flag overrides ---------
	cfg, err := config.Load(config.LoadOptions{Path: *flagConfig})
	if err != nil {
		return err
	}
	if *flagHost != "" {
		cfg.Host = *flagHost
	}
	if *flagPort != 0 {
		cfg.Port = *flagPort
	}
	if *flagDataDir != "" {
		cfg.DataDir = *flagDataDir
	}
	if *flagLogLevel != "" {
		cfg.LogLevel = *flagLogLevel
	}
	if err := config.Validate(cfg); err != nil {
		return err
	}

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
		watcher = config.NewWatcher(*flagConfig, cfg, cfg.Reload.PollMS, logger)
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
	qp = newQuotaProxy(fallbackEngine, quotaManager)

	// --- HTTP server (p3.http-server + p3.ingest-endpoints) ------------
	// The router owns the middleware chain (request-id -> access-log
	// -> recover) and the listener timeout policy. The ingest
	// endpoints authenticate and decode onto the mux, forwarding
	// execution to the fallback engine.
	router := server.New(server.Options{
		Logger:  logger,
		Metrics: metricsRegistry,
		Timeouts: server.Timeouts{
			ReadHeader: time.Duration(cfg.HTTP.ReadHeaderTimeoutMS) * time.Millisecond,
			Read:       time.Duration(cfg.HTTP.ReadTimeoutMS) * time.Millisecond,
			Write:      time.Duration(cfg.HTTP.WriteTimeoutMS) * time.Millisecond,
			Idle:       time.Duration(cfg.HTTP.IdleTimeoutMS) * time.Millisecond,
		},
		Version:       version.Version,
		SchemaVersion: schemaVer,
	})
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

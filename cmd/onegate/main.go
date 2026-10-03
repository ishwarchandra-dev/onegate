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

	"github.com/ishwarchandra-dev/onegate/internal/auth"
	"github.com/ishwarchandra-dev/onegate/internal/config"
	"github.com/ishwarchandra-dev/onegate/internal/observability"
	"github.com/ishwarchandra-dev/onegate/internal/proxy/client"
	"github.com/ishwarchandra-dev/onegate/internal/proxy/fallback"
	"github.com/ishwarchandra-dev/onegate/internal/proxy/ingest"
	"github.com/ishwarchandra-dev/onegate/internal/ratelimit"
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

	// --- proxy: upstream client + fallback engine ---------------------
	upstreamClient := client.New(client.TransportConfig{}, nil)
	defer upstreamClient.CloseIdleConnections()

	fallbackEngine := fallback.NewEngine(fallback.Config{
		Resolver: fallback.StaticResolver{
			Targets: nil, // Dynamic routing engine lands in Phase 4
		},
		Client: upstreamClient,
		Logger: logger,
		OnUsage: func(ue fallback.UsageEvent) {
			usagePipeline.EnqueueEvent(ue.ToObservabilityEvent())
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
	})

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
		Auth:  verifier,
		Proxy: fallbackEngine,
	})
	router.Mux().Handle("GET /api/logs/live", logHub)
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

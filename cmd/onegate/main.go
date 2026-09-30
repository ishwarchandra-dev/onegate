// Command onegate is the entrypoint for the OneGate LLM gateway.
//
// OneGate is the Go rewrite of OmniRoute v3.8.52: a single-binary LLM gateway
// with an embedded management dashboard, provider routing, fallback chains and
// usage analytics.
//
// Phase 0 ships the process skeleton only: flag parsing, structured logging,
// a placeholder HTTP server with /healthz, and graceful shutdown. The proxy
// core lands in Phase 2 (see tasks/phase-2.protocols.graph.yaml).
package main

import (
        "context"
        "errors"
        "flag"
        "fmt"
        "log/slog"
        "net/http"
        "os"
        "os/signal"
        "syscall"
        "time"

        "github.com/ishwarchandra-dev/onegate/internal/config"
        "github.com/ishwarchandra-dev/onegate/internal/version"
)

func main() {
        if err := run(); err != nil {
                fmt.Fprintf(os.Stderr, "onegate: %v\n", err)
                os.Exit(1)
        }
}

func run() error {
        cfg := config.Default()

        fs := flag.NewFlagSet("onegate", flag.ContinueOnError)
        fs.StringVar(&cfg.Host, "host", cfg.Host, "address to listen on")
        fs.IntVar(&cfg.Port, "port", cfg.Port, "port to listen on")
        showVersion := fs.Bool("version", false, "print version and exit")
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

        logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
                Level: slog.LevelInfo,
        }))
        slog.SetDefault(logger)

        mux := http.NewServeMux()
        mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
                w.WriteHeader(http.StatusOK)
                _, _ = w.Write([]byte("ok"))
        })
        mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
                w.Header().Set("Content-Type", "text/plain; charset=utf-8")
                fmt.Fprintf(w, "OneGate %s — gateway core arrives in Phase 2\n", version.String())
        })

        srv := &http.Server{
                Addr:              fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
                Handler:           mux,
                ReadHeaderTimeout: 10 * time.Second,
        }

        ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
        defer stop()

        errCh := make(chan error, 1)
        go func() {
                slog.Info("onegate listening", "addr", srv.Addr, "version", version.String())
                errCh <- srv.ListenAndServe()
        }()

        select {
        case err := <-errCh:
                if errors.Is(err, http.ErrServerClosed) {
                        return nil
                }
                return err
        case <-ctx.Done():
                slog.Info("shutting down gracefully")
                shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
                defer cancel()
                return srv.Shutdown(shutdownCtx)
        }
}

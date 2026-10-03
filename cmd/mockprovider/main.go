// Command mockprovider runs a standalone mock upstream LLM provider server
// supporting OpenAI, Anthropic, and Gemini wire protocols.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/ishwarchandra-dev/onegate/internal/mockprovider"
)

func main() {
	var (
		host = flag.String("host", "127.0.0.1", "host address to listen on")
		port = flag.Int("port", 8080, "port to listen on")
	)
	flag.Parse()

	addr := fmt.Sprintf("%s:%d", *host, *port)
	srv := &http.Server{
		Addr:    addr,
		Handler: mockprovider.NewHandler(),
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("mockprovider listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("mockprovider: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("mockprovider shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("mockprovider shutdown: %v", err)
	}
}

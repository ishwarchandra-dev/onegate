// Package client is the upstream HTTP client for provider calls: a
// shared connection pool, per-protocol request building (URL, auth,
// headers), and the SSRF guard that keeps provider base URLs from
// reaching link-local metadata endpoints.
//
// Design contracts:
//
//   - Retry-safe by construction: a Request carries its body as []byte,
//     and every attempt rebuilds the http.Request from scratch. There is
//     no way to hand the pool a half-consumed reader, so the fallback
//     engine (p3.fallback-chain) can replay a request freely.
//   - Auth is injected here, never by callers: one place to audit for
//     credential handling. Keys are attached as headers (never query
//     parameters) so they cannot leak into logged URLs.
//   - The transport is shared across providers; Go pools connections by
//     host:port, so each provider gets its own pool slice. Idle
//     keep-alives give "one connection per provider under sequential
//     load" without per-provider client objects.
//   - The SSRF guard enforces the scheme allowlist at build time and the
//     IP blocklist at dial time (after DNS resolution, on the address
//     actually being connected) — the point where hostname tricks
//     (DNS rebinding, metadata hostnames) have already collapsed into a
//     concrete IP.
package client

import (
	"context"
	"net/http"
	"time"
)

// TransportConfig tunes the shared upstream pool. Zero values select
// package defaults; production wiring (config) lands with the routing
// phase. The values below are chosen for LLM workloads: long-lived
// idle pools (providers are dialed repeatedly), a bounded header wait
// that is safe for streaming (headers precede the body), and generous
// dial/TLS budgets.
type TransportConfig struct {
	// MaxIdleConns caps idle connections across all hosts (default 256).
	MaxIdleConns int
	// MaxIdleConnsPerHost caps idle connections per provider host
	// (default 32; Go's own default of 2 would churn under fan-out).
	MaxIdleConnsPerHost int
	// MaxConnsPerHost caps total connections per provider host
	// (0 = unlimited).
	MaxConnsPerHost int
	// IdleConnTimeout closes pooled connections after this idle period
	// (default 90s).
	IdleConnTimeout time.Duration
	// DialTimeout bounds establishing a TCP connection (default 10s).
	DialTimeout time.Duration
	// TLSHandshakeTimeout bounds the TLS handshake (default 10s).
	TLSHandshakeTimeout time.Duration
	// ResponseHeaderTimeout bounds waiting for response headers. It is
	// streaming-safe: providers send headers before the first SSE frame.
	// Default 120s (non-streaming Anthropic calls hold headers until the
	// full generation completes).
	ResponseHeaderTimeout time.Duration
	// ExpectContinueTimeout forExpect-100 (default 1s).
	ExpectContinueTimeout time.Duration
}

func (tc TransportConfig) withDefaults() TransportConfig {
	set := func(dst *int, v int) {
		if *dst == 0 {
			*dst = v
		}
	}
	setDur := func(dst *time.Duration, v time.Duration) {
		if *dst == 0 {
			*dst = v
		}
	}
	set(&tc.MaxIdleConns, 256)
	set(&tc.MaxIdleConnsPerHost, 32)
	setDur(&tc.IdleConnTimeout, 90*time.Second)
	setDur(&tc.DialTimeout, 10*time.Second)
	setDur(&tc.TLSHandshakeTimeout, 10*time.Second)
	setDur(&tc.ResponseHeaderTimeout, 120*time.Second)
	setDur(&tc.ExpectContinueTimeout, time.Second)
	return tc
}

// Client sends upstream requests through one shared transport.
type Client struct {
	transport *http.Transport
	http      *http.Client
	ssrf      *SSRFGuard
}

// New builds a client. The SSRF guard is mandatory: passing nil selects
// the default policy (https/http schemes, metadata IPs blocked). There
// is deliberately no way to construct a guard-free client — the SSRF
// protection is not optional.
func New(tc TransportConfig, guard *SSRFGuard) *Client {
	tc = tc.withDefaults()
	if guard == nil {
		guard = DefaultSSRFGuard()
	}
	tr := &http.Transport{
		Proxy:                 nil, // never proxy upstream LLM calls
		MaxIdleConns:          tc.MaxIdleConns,
		MaxIdleConnsPerHost:   tc.MaxIdleConnsPerHost,
		MaxConnsPerHost:       tc.MaxConnsPerHost,
		IdleConnTimeout:       tc.IdleConnTimeout,
		TLSHandshakeTimeout:   tc.TLSHandshakeTimeout,
		ExpectContinueTimeout: tc.ExpectContinueTimeout,
		ResponseHeaderTimeout: tc.ResponseHeaderTimeout,
		ForceAttemptHTTP2:     true,
		DialContext:           guard.dialer().DialContext,
	}
	return &Client{
		transport: tr,
		http:      &http.Client{Transport: tr},
		ssrf:      guard,
	}
}

// CloseIdleConnections drops pooled connections (shutdown path).
func (c *Client) CloseIdleConnections() { c.transport.CloseIdleConnections() }

// Do builds and sends one upstream request. The returned response body
// is the caller's to close (stream pipeline or buffered path). All
// failures — building, dialing, headers — come back as *Error carrying
// a domain.GatewayError, so the fallback engine can classify retry
// safety without touching transport types.
func (c *Client) Do(ctx context.Context, p Provider, r Request) (*http.Response, error) {
	hr, err := c.Build(ctx, p, r)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(hr)
	if err != nil {
		return nil, classifyTransportError(p.ID, err)
	}
	return resp, nil
}

// Build materializes the http.Request (also exported for tests and for
// the fallback engine, which may want to inspect what will be sent).
// Building performs the scheme allowlist check and auth injection but
// performs no I/O.
func (c *Client) Build(ctx context.Context, p Provider, r Request) (*http.Request, error) {
	hr, err := p.build(r, c.ssrf)
	if err != nil {
		return nil, err
	}
	hr = hr.WithContext(ctx)
	return hr, nil
}

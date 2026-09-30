package server

import (
	"bufio"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ishwarchandra-dev/onegate/internal/observability"
)

// newTestRouter builds a Router with a buffer-backed logger and returns
// both. Registered via register so tests can add routes before serving.
func newTestRouter(t *testing.T) (*Router, *logBuffer) {
	t.Helper()
	lb := &logBuffer{}
	r := New(Options{Logger: slog.New(slog.NewJSONHandler(lb, nil))})
	return r, lb
}

type logBuffer struct {
	mu    sync.Mutex
	lines []string
}

func (lb *logBuffer) Write(p []byte) (int, error) {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	lb.lines = append(lb.lines, strings.TrimSpace(string(p)))
	return len(p), nil
}

func (lb *logBuffer) snapshot() []string {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	out := make([]string, len(lb.lines))
	copy(out, lb.lines)
	return out
}

// ---------------------------------------------------------------------------
// Request ID
// ---------------------------------------------------------------------------

func TestRequestIDPolicy(t *testing.T) {
	cases := []struct {
		name     string
		incoming string
		valid    bool
	}{
		{"plain", "abc123", true},
		{"dashed", "req-001-x", true},
		{"uuid", "6f9619ff-8b86-d011-b42d-00c04fc964ff", true},
		{"dots", "a.b.c", true},
		{"colons", "tenant:req:1", true},
		{"empty", "", false},
		{"too long", strings.Repeat("a", 65), false},
		{"space", "abc def", false},
		{"newline", "abc\ndef", false},
		{"tab", "abc\tdef", false},
		{"unicode", "café", false},
		{"header injection", "abc\r\nX-Evil: 1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := validRequestID(tc.incoming); got != tc.valid {
				t.Fatalf("validRequestID(%q) = %v, want %v", tc.incoming, got, tc.valid)
			}
		})
	}
}

func TestRequestIDGenerated(t *testing.T) {
	id := requestID("")
	if !strings.HasPrefix(id, requestIDPrefix) {
		t.Fatalf("generated ID should carry prefix: %q", id)
	}
	if want := len(requestIDPrefix) + 2*requestIDLen; len(id) != want {
		t.Fatalf("generated ID length: want %d, got %d (%q)", want, len(id), id)
	}
	if id == requestID("") {
		t.Fatal("two generated IDs must differ")
	}
}

func TestRequestIDMiddleware(t *testing.T) {
	r, _ := newTestRouter(t)
	var ctxID string
	r.Mux().HandleFunc("GET /probe", func(w http.ResponseWriter, req *http.Request) {
		ctxID = observability.TraceID(req.Context())
		w.WriteHeader(201)
	})

	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	// Client-supplied valid ID is honored and echoed.
	req, _ := http.NewRequest("GET", srv.URL+"/probe", nil)
	req.Header.Set(HeaderRequestID, "client-42")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := resp.Header.Get(HeaderRequestID); got != "client-42" {
		t.Fatalf("echoed ID: want client-42, got %q", got)
	}
	if ctxID != "client-42" {
		t.Fatalf("context ID: want client-42, got %q", ctxID)
	}

	// Garbage IDs are replaced (and echoed with the replacement).
	req2, _ := http.NewRequest("GET", srv.URL+"/probe", nil)
	req2.Header.Set(HeaderRequestID, "bad id with spaces")
	resp2, err := srv.Client().Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	got2 := resp2.Header.Get(HeaderRequestID)
	if got2 == "bad id with spaces" || !strings.HasPrefix(got2, requestIDPrefix) {
		t.Fatalf("unsafe ID should be replaced, echoed %q", got2)
	}
	if ctxID != got2 {
		t.Fatalf("context ID %q should match echoed %q", ctxID, got2)
	}
}

func TestRequestIDUniqueUnderConcurrency(t *testing.T) {
	r, _ := newTestRouter(t)
	r.Mux().HandleFunc("GET /probe", func(w http.ResponseWriter, _ *http.Request) {})

	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	const n = 64
	ids := make([]string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp, err := srv.Client().Get(srv.URL + "/probe")
			if err != nil {
				t.Error(err)
				return
			}
			resp.Body.Close()
			ids[i] = resp.Header.Get(HeaderRequestID)
		}(i)
	}
	wg.Wait()

	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" {
			t.Fatal("missing request ID header")
		}
		if seen[id] {
			t.Fatalf("duplicate request ID %q", id)
		}
		seen[id] = true
	}
}

// ---------------------------------------------------------------------------
// Panic recovery
// ---------------------------------------------------------------------------

func TestPanicReturns500EnvelopeAndConnectionSurvives(t *testing.T) {
	r, lb := newTestRouter(t)
	r.Mux().HandleFunc("GET /boom", func(_ http.ResponseWriter, _ *http.Request) {
		panic("kaboom: secret internals")
	})

	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	// First request: panic -> clean 500 + envelope.
	resp, err := srv.Client().Get(srv.URL + "/boom")
	if err != nil {
		t.Fatalf("panic must not kill the request: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status: want 500, got %d", resp.StatusCode)
	}
	var env struct {
		Error struct {
			Status    int    `json:"status"`
			Type      string `json:"type"`
			Message   string `json:"message"`
			Retryable bool   `json:"retryable"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("envelope is not JSON: %v (%s)", err, body)
	}
	if env.Error.Status != 500 || env.Error.Type != "internal_error" ||
		env.Error.Message != "internal server error" || env.Error.Retryable {
		t.Fatalf("unexpected envelope: %+v", env.Error)
	}
	if strings.Contains(string(body), "kaboom") {
		t.Fatal("panic value leaked to client")
	}

	// Connection stays usable: same TCP connection serves the next
	// request. Go's client would quietly reconnect on a dead socket, so
	// we prove liveness at the raw connection level.
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	writeRaw := func(path string) {
		_, err := conn.Write([]byte("GET " + path + " HTTP/1.1\r\nHost: t\r\n\r\n"))
		if err != nil {
			t.Fatalf("raw write %s: %v", path, err)
		}
	}
	readResp := func() (status string, body string) {
		br := bufio.NewReader(conn)
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("raw read status line: %v", err)
		}
		status = strings.TrimSpace(line)
		var hdrs map[string]string = map[string]string{}
		for {
			h, err := br.ReadString('\n')
			if err != nil {
				t.Fatalf("raw read header: %v", err)
			}
			h = strings.TrimRight(h, "\r\n")
			if h == "" {
				break
			}
			parts := strings.SplitN(h, ":", 2)
			if len(parts) == 2 {
				hdrs[strings.ToLower(strings.TrimSpace(parts[0]))] = strings.TrimSpace(parts[1])
			}
		}
		n := 0
		if cl, ok := hdrs["content-length"]; ok {
			_, _ = fmtSscanf(cl, &n)
			buf := make([]byte, n)
			if _, err := io.ReadFull(br, buf); err != nil {
				t.Fatalf("raw read body: %v", err)
			}
			body = string(buf)
		} else {
			b, _ := io.ReadAll(br)
			body = string(b)
		}
		return status, body
	}

	writeRaw("/boom")
	st, _ := readResp()
	if !strings.Contains(st, "500") {
		t.Fatalf("raw first response: want 500, got %q", st)
	}
	writeRaw("/healthz")
	st2, body2 := readResp()
	if !strings.Contains(st2, "200") {
		t.Fatalf("connection must survive a panic; got %q (body %q)", st2, body2)
	}
	if !strings.Contains(body2, `"status":"ok"`) {
		t.Fatalf("second response body wrong: %q", body2)
	}

	// Panic was logged server-side with the trace ID.
	found := false
	for _, line := range lb.snapshot() {
		if strings.Contains(line, "panic recovered") && strings.Contains(line, "kaboom") {
			found = true
		}
	}
	if !found {
		t.Fatal("panic not logged with its value")
	}
}

// fmtSscanf parses a decimal content-length without pulling fmt into the
// hot test path twice.
func fmtSscanf(s string, out *int) (int, error) {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			break
		}
		n = n*10 + int(s[i]-'0')
	}
	*out = n
	return 1, nil
}

func TestPanicMidStreamClosesConnection(t *testing.T) {
	r, _ := newTestRouter(t)
	r.Mux().HandleFunc("GET /drip", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: first\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		panic("mid-stream failure")
	})

	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/drip")
	if err != nil {
		t.Fatalf("headers should arrive: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: want 200 (already committed), got %d", resp.StatusCode)
	}

	// The body must terminate abruptly: no graceful completion, no
	// swallowed panic masquerading as success.
	_, readErr := io.ReadAll(resp.Body)
	if readErr == nil {
		t.Fatal("mid-stream panic must terminate the body, got clean EOF")
	}
}

// ---------------------------------------------------------------------------
// Access log + middleware order
// ---------------------------------------------------------------------------

func TestAccessLogFieldsAndOrder(t *testing.T) {
	lb := &logBuffer{}
	logger := slog.New(slog.NewJSONHandler(lb, nil))
	r := New(Options{Logger: logger})
	r.Mux().HandleFunc("GET /ok", func(w http.ResponseWriter, _ *http.Request) {
		// The recover middleware must already have wrapped us: prove it
		// by verifying the writer is the chain's statusWriter.
		if _, ok := w.(*statusWriter); !ok {
			t.Error("handler writer should be the chain's statusWriter")
		}
		w.WriteHeader(202)
	})
	r.Mux().HandleFunc("GET /boom", func(_ http.ResponseWriter, _ *http.Request) {
		panic("order-probe")
	})

	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	// /ok: one info line with all fields.
	resp, err := srv.Client().Get(srv.URL + "/ok")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// /boom: the access log must still produce exactly one line (it
	// wraps recover), carrying the final 500 and the trace ID — which
	// proves RequestID ran outside AccessLog.
	resp2, err := srv.Client().Get(srv.URL + "/boom")
	if err != nil {
		t.Fatal(err)
	}
	traceID := resp2.Header.Get(HeaderRequestID)
	resp2.Body.Close()

	lines := lb.snapshot()
	var okLine, boomLine string
	boomCount := 0
	for _, l := range lines {
		if strings.Contains(l, `"http_request"`) && strings.Contains(l, `"/ok"`) {
			okLine = l
		}
		if strings.Contains(l, `"http_request"`) && strings.Contains(l, `"/boom"`) {
			boomLine = l
			boomCount++
		}
	}
	if okLine == "" {
		t.Fatalf("no access line for /ok in %v", lines)
	}
	for _, want := range []string{`"method":"GET"`, `"status":202`, `"duration_ms"`, `"remote_addr"`, `"trace_id"`} {
		if !strings.Contains(okLine, want) {
			t.Fatalf("/ok line missing %s: %s", want, okLine)
		}
	}
	if okLine != "" && !strings.Contains(okLine, `"level":"INFO"`) {
		t.Fatalf("2xx should log at info: %s", okLine)
	}

	if boomCount != 1 {
		t.Fatalf("panic request must yield exactly one access line, got %d", boomCount)
	}
	if !strings.Contains(boomLine, `"status":500`) {
		t.Fatalf("access line must show the recovered 500: %s", boomLine)
	}
	if !strings.Contains(boomLine, `"level":"ERROR"`) {
		t.Fatalf("5xx should log at error: %s", boomLine)
	}
	if !strings.Contains(boomLine, `"trace_id":"`+traceID+`"`) {
		t.Fatalf("access line must carry the echoed trace ID %q: %s", traceID, boomLine)
	}
}

// ---------------------------------------------------------------------------
// System routes
// ---------------------------------------------------------------------------

func TestSystemRoutes(t *testing.T) {
	r, _ := newTestRouter(t)
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("healthz: %d", resp.StatusCode)
	}
	var health map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		t.Fatalf("healthz body: %v", err)
	}
	if health["status"] != "ok" {
		t.Fatalf("healthz status: %v", health)
	}
	if _, ok := health["version"]; !ok {
		t.Fatal("healthz must report version")
	}
	if _, ok := health["schema_version"]; !ok {
		t.Fatal("healthz must report schema_version")
	}
	if resp.Header.Get(HeaderRequestID) == "" {
		t.Fatal("healthz must carry request ID")
	}

	// /metrics reflects traffic in the exposition format.
	mresp, err := srv.Client().Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer mresp.Body.Close()
	if mresp.StatusCode != 200 {
		t.Fatalf("metrics: %d", mresp.StatusCode)
	}
	if ct := mresp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("metrics content type: %q", ct)
	}
	mbody, _ := io.ReadAll(mresp.Body)
	if !strings.Contains(string(mbody), "onegate_http_requests_total{method=\"GET\",code=\"200\"}") {
		t.Fatalf("metrics should count the healthz request:\n%s", mbody)
	}

	// Unknown paths and wrong methods behave per net/http defaults.
	nresp, _ := srv.Client().Get(srv.URL + "/nope")
	nresp.Body.Close()
	if nresp.StatusCode != 404 {
		t.Fatalf("unknown path: want 404, got %d", nresp.StatusCode)
	}
	mresp2, _ := srv.Client().Post(srv.URL+"/healthz", "", nil)
	mresp2.Body.Close()
	if mresp2.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("wrong method: want 405, got %d", mresp2.StatusCode)
	}
	if allow := mresp2.Header.Get("Allow"); !strings.Contains(allow, "GET") {
		t.Fatalf("405 must carry Allow, got %q", allow)
	}
}

// ---------------------------------------------------------------------------
// Timeouts and metrics injection
// ---------------------------------------------------------------------------

func TestServerTimeoutMapping(t *testing.T) {
	r, _ := newTestRouter(t)
	srv := r.Server("127.0.0.1:0")
	if srv.ReadHeaderTimeout != 0 || srv.ReadTimeout != 0 ||
		srv.WriteTimeout != 0 || srv.IdleTimeout != 0 {
		t.Fatalf("zero policy must disable all timeouts: %+v", srv)
	}

	timeouts := Timeouts{
		ReadHeader: 3 * time.Second,
		Read:       30 * time.Second,
		Write:      time.Hour,
		Idle:       2 * time.Minute,
	}
	r2 := New(Options{Timeouts: timeouts})
	srv2 := r2.Server("127.0.0.1:1")
	if srv2.ReadHeaderTimeout != timeouts.ReadHeader ||
		srv2.ReadTimeout != timeouts.Read ||
		srv2.WriteTimeout != timeouts.Write ||
		srv2.IdleTimeout != timeouts.Idle {
		t.Fatalf("timeouts not mapped: %+v", srv2)
	}
	if srv2.Handler == nil {
		t.Fatal("server must embed the chain")
	}
}

func TestMetricsInjection(t *testing.T) {
	observed := &recordingMetrics{handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("custom-metrics\n"))
	})}
	r := New(Options{Metrics: observed})
	r.Mux().HandleFunc("GET /probe", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	})

	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/probe")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if len(observed.requests) != 1 || observed.requests[0] != "GET|200" {
		t.Fatalf("ObserveRequest not called correctly: %v", observed.requests)
	}

	mresp, err := srv.Client().Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer mresp.Body.Close()
	body, _ := io.ReadAll(mresp.Body)
	if string(body) != "custom-metrics\n" {
		t.Fatalf("injected metrics handler must own /metrics, got %q", body)
	}
}

type recordingMetrics struct {
	requests []string
	panics   int
	handler  http.Handler
}

func (m *recordingMetrics) ObserveRequest(method string, code int) {
	m.requests = append(m.requests, method+"|"+itoa(code))
}

func (m *recordingMetrics) ObservePanic() { m.panics++ }

func (m *recordingMetrics) Handler() http.Handler { return m.handler }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

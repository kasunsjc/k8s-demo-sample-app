package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	cfg := Config{Version: "v-test", Color: "#ff0000", Message: "hi", ConfigDir: t.TempDir(), SecretDir: t.TempDir()}
	s := NewServer(cfg)
	s.exit = func(int) {}
	return s, s.Routes()
}

func do(t *testing.T, h http.Handler, method, target, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func TestWhoami(t *testing.T) {
	t.Setenv("POD_NAME", "inspector-abc")
	t.Setenv("NODE_NAME", "node-1")
	_, h := newTestServer(t)
	rec, out := do(t, h, "GET", "/whoami", "")
	if rec.Code != 200 || out["pod"] != "inspector-abc" || out["node"] != "node-1" || out["version"] != "v-test" {
		t.Fatalf("unexpected whoami: %d %v", rec.Code, out)
	}
}

func TestStatus(t *testing.T) {
	_, h := newTestServer(t)
	if rec, _ := do(t, h, "POST", "/status/418", ""); rec.Code != 418 {
		t.Fatalf("want 418, got %d", rec.Code)
	}
	if rec, _ := do(t, h, "GET", "/status/abc", ""); rec.Code != 400 {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestReadinessToggle(t *testing.T) {
	_, h := newTestServer(t)
	if rec, _ := do(t, h, "GET", "/readyz", ""); rec.Code != 200 {
		t.Fatalf("want ready, got %d", rec.Code)
	}
	do(t, h, "POST", "/chaos/ready?state=false", "")
	if rec, _ := do(t, h, "GET", "/readyz", ""); rec.Code != 503 {
		t.Fatalf("want 503, got %d", rec.Code)
	}
	do(t, h, "POST", "/chaos/ready", "")
	if rec, _ := do(t, h, "GET", "/readyz", ""); rec.Code != 200 {
		t.Fatalf("toggle should restore readiness, got %d", rec.Code)
	}
}

func TestLivenessToggle(t *testing.T) {
	_, h := newTestServer(t)
	do(t, h, "POST", "/chaos/live?state=false", "")
	if rec, _ := do(t, h, "GET", "/livez", ""); rec.Code != 500 {
		t.Fatalf("want 500, got %d", rec.Code)
	}
}

func TestEnvMasksSecrets(t *testing.T) {
	t.Setenv("DB_PASSWORD", "supersecret")
	_, h := newTestServer(t)
	_, out := do(t, h, "GET", "/env", "")
	env := out["env"].(map[string]any)
	if v := env["DB_PASSWORD"]; v == "supersecret" || !strings.HasPrefix(v.(string), "su*") {
		t.Fatalf("password not masked: %v", v)
	}
}

func TestConfigAndSecretMounts(t *testing.T) {
	s, h := newTestServer(t)
	_ = os.WriteFile(filepath.Join(s.cfg.ConfigDir, "greeting"), []byte("hello"), 0o644)
	_ = os.WriteFile(filepath.Join(s.cfg.SecretDir, "api-key"), []byte("abcdef"), 0o600)

	_, cfg := do(t, h, "GET", "/config", "")
	if cfg["files"].(map[string]any)["greeting"] != "hello" {
		t.Fatalf("config not read: %v", cfg)
	}
	_, sec := do(t, h, "GET", "/secret", "")
	key := sec["keys"].(map[string]any)["api-key"].(map[string]any)
	if key["value"] == "abcdef" || key["length"].(float64) != 6 {
		t.Fatalf("secret not masked: %v", key)
	}
}

func TestAnythingEchoesJSON(t *testing.T) {
	_, h := newTestServer(t)
	rec, out := do(t, h, "PUT", "/anything/foo?x=1", `{"a":1}`)
	if rec.Code != 200 || out["method"] != "PUT" || out["path"] != "/anything/foo" {
		t.Fatalf("unexpected: %v", out)
	}
	if out["json"].(map[string]any)["a"].(float64) != 1 {
		t.Fatalf("json not parsed: %v", out["json"])
	}
}

func TestStressValidation(t *testing.T) {
	_, h := newTestServer(t)
	if rec, _ := do(t, h, "POST", "/stress/cpu?seconds=9999", ""); rec.Code != 400 {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	if rec, out := do(t, h, "POST", "/stress/memory?mb=2", ""); rec.Code != 200 || out["heldMB"].(float64) != 2 {
		t.Fatalf("memory alloc failed: %d %v", rec.Code, out)
	}
	if _, out := do(t, h, "DELETE", "/stress/memory", ""); out["heldMB"].(float64) != 0 {
		t.Fatalf("memory not released: %v", out)
	}
}

func TestMetricsExposed(t *testing.T) {
	_, h := newTestServer(t)
	do(t, h, "GET", "/whoami", "")
	rec, _ := do(t, h, "GET", "/metrics", "")
	body := rec.Body.String()
	if !strings.Contains(body, `kubeplay_http_requests_total{route="GET /whoami",code="200"} 1`) {
		t.Fatalf("metrics missing whoami counter:\n%s", body)
	}
}

func TestParseTraceparent(t *testing.T) {
	cases := []struct {
		header  string
		ok      bool
		sampled bool
	}{
		{"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", true, true},
		{"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00", true, false},
		{"", false, false},
		{"ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", false, false},
		{"00-00000000000000000000000000000000-00f067aa0ba902b7-01", false, false},
		{"00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01", false, false},
		{"00-4BF92F3577B34DA6A3CE929D0E0E4736-00f067aa0ba902b7-01", false, false},
		{"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-extra", false, false},
	}
	for _, c := range cases {
		sc, ok := parseTraceparent(c.header)
		if ok != c.ok || sc.Sampled != c.sampled {
			t.Errorf("parseTraceparent(%q) = %+v, %v; want ok=%v sampled=%v", c.header, sc, ok, c.ok, c.sampled)
		}
		if ok && sc.Traceparent() != c.header {
			t.Errorf("round trip %q -> %q", c.header, sc.Traceparent())
		}
	}
}

func TestTracingExportsServerSpans(t *testing.T) {
	bodies := make(chan map[string]any, 10)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path == "/v1/traces" {
			bodies <- body
		}
	}))
	defer collector.Close()
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", collector.URL)
	t.Setenv("POD_NAME", "inspector-abc")

	s, h := newTestServer(t)
	s.tracer = newTracerFromEnv("v-test")

	traced := httptest.NewRequest("GET", "/whoami", nil)
	traced.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, traced)
	if rec.Header().Get("X-Trace-Id") != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("want X-Trace-Id of the incoming trace, got %q", rec.Header().Get("X-Trace-Id"))
	}
	unsampled := httptest.NewRequest("GET", "/whoami", nil)
	unsampled.Header.Set("traceparent", "00-11111111111111111111111111111111-00f067aa0ba902b7-00")
	h.ServeHTTP(httptest.NewRecorder(), unsampled)
	do(t, h, "GET", "/readyz", "")
	do(t, h, "POST", "/status/503", "")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.tracer.Shutdown(ctx)

	var body map[string]any
	select {
	case body = <-bodies:
	case <-time.After(5 * time.Second):
		t.Fatal("collector received no spans")
	}
	rs := body["resourceSpans"].([]any)[0].(map[string]any)
	resource, _ := json.Marshal(rs["resource"])
	if !strings.Contains(string(resource), `"inspector-abc"`) || !strings.Contains(string(resource), `"service.name"`) {
		t.Fatalf("resource missing pod/service: %s", resource)
	}
	spans := rs["scopeSpans"].([]any)[0].(map[string]any)["spans"].([]any)
	if len(spans) != 2 {
		t.Fatalf("want 2 spans (sampled /whoami + /status), got %d: %v", len(spans), spans)
	}
	whoami := spans[0].(map[string]any)
	if whoami["name"] != "GET /whoami" || whoami["traceId"] != "4bf92f3577b34da6a3ce929d0e0e4736" ||
		whoami["parentSpanId"] != "00f067aa0ba902b7" || whoami["kind"] != float64(2) {
		t.Fatalf("unexpected whoami span: %v", whoami)
	}
	status := spans[1].(map[string]any)
	attrs, _ := json.Marshal(status["attributes"])
	if status["name"] != "POST /status/{code}" || status["parentSpanId"] != nil ||
		status["status"].(map[string]any)["code"] != float64(2) || !strings.Contains(string(attrs), `"intValue":"503"`) {
		t.Fatalf("unexpected status span: %v %s", status, attrs)
	}
}

func TestTracingDisabledByDefault(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	if tr := newTracerFromEnv("v1"); tr != nil {
		t.Fatal("tracer should be nil without an OTLP endpoint")
	}
	_, h := newTestServer(t)
	if rec, _ := do(t, h, "GET", "/whoami", ""); rec.Header().Get("X-Trace-Id") != "" {
		t.Fatal("no X-Trace-Id expected when tracing is disabled")
	}
}

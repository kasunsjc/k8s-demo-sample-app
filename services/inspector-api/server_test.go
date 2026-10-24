package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

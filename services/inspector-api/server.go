package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxDelaySeconds = 30
	maxCPUSeconds   = 300
	maxCPUWorkers   = 64
	maxMemoryMB     = 4096
	maxBodyBytes    = 1 << 20
)

type Server struct {
	cfg        Config
	start      time.Time
	started    atomic.Bool
	ready      atomic.Bool
	live       atomic.Bool
	cpuWorkers atomic.Int64
	memMu      sync.Mutex
	memHog     [][]byte
	metrics    *Metrics
	tracer     *Tracer
	exit       func(int)
}

func NewServer(cfg Config) *Server {
	s := &Server{cfg: cfg, start: time.Now(), metrics: NewMetrics(), exit: os.Exit}
	s.ready.Store(true)
	s.live.Store(true)
	if cfg.StartupDelay <= 0 {
		s.started.Store(true)
	} else {
		time.AfterFunc(cfg.StartupDelay, func() { s.started.Store(true) })
	}
	return s
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /whoami", s.handleWhoami)
	mux.HandleFunc("GET /headers", s.handleHeaders)
	mux.HandleFunc("GET /ip", s.handleIP)
	mux.HandleFunc("GET /env", s.handleEnv)
	mux.HandleFunc("GET /config", s.handleConfig)
	mux.HandleFunc("GET /secret", s.handleSecret)
	mux.HandleFunc("/status/{code}", s.handleStatus)
	mux.HandleFunc("/delay/{seconds}", s.handleDelay)
	mux.HandleFunc("/anything", s.handleAnything)
	mux.HandleFunc("/anything/{path...}", s.handleAnything)
	mux.HandleFunc("GET /livez", s.handleLivez)
	mux.HandleFunc("GET /readyz", s.handleReadyz)
	mux.HandleFunc("GET /startupz", s.handleStartupz)
	mux.HandleFunc("POST /chaos/ready", s.handleChaosReady)
	mux.HandleFunc("POST /chaos/live", s.handleChaosLive)
	mux.HandleFunc("POST /chaos/crash", s.handleChaosCrash)
	mux.HandleFunc("POST /stress/cpu", s.handleStressCPU)
	mux.HandleFunc("POST /stress/memory", s.handleStressMemory)
	mux.HandleFunc("DELETE /stress/memory", s.handleReleaseMemory)
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	return s.instrument(mux)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

var quietPaths = map[string]bool{"/livez": true, "/readyz": true, "/startupz": true, "/metrics": true}

func (s *Server) instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		// Probes and scrapes are not traced; everything else continues the caller's W3C trace.
		traced := s.tracer != nil && !quietPaths[r.URL.Path]
		var parent, sc SpanContext
		var hasParent bool
		if traced {
			parent, hasParent = parseTraceparent(r.Header.Get("traceparent"))
			sc = childSpanContext(parent, hasParent)
			if sc.Sampled {
				w.Header().Set("X-Trace-Id", sc.TraceID)
			}
		}
		next.ServeHTTP(rec, r)
		if traced && sc.Sampled {
			s.tracer.record(serverSpan(sc, parent, hasParent, r, rec.status, start, time.Now()))
		}
		// ServeMux sets r.Pattern on the matched request.
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		s.metrics.Observe(route, rec.status, time.Since(start))
		if !quietPaths[r.URL.Path] {
			slog.Info("request", "method", r.Method, "path", r.URL.Path, "status", rec.status, "duration", time.Since(start).String())
		}
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func (s *Server) podName() string {
	host, _ := os.Hostname()
	return getenv("POD_NAME", host)
}

func (s *Server) podInfo() map[string]any {
	host, _ := os.Hostname()
	return map[string]any{
		"service":        "inspector-api",
		"pod":            s.podName(),
		"hostname":       host,
		"namespace":      getenv("POD_NAMESPACE", "unknown"),
		"node":           getenv("NODE_NAME", "unknown"),
		"podIP":          getenv("POD_IP", "unknown"),
		"serviceAccount": getenv("SERVICE_ACCOUNT", "unknown"),
		"version":        s.cfg.Version,
		"color":          s.cfg.Color,
		"message":        s.cfg.Message,
		"uptimeSeconds":  int(time.Since(s.start).Seconds()),
		"ready":          s.ready.Load(),
		"live":           s.live.Load(),
		"limits": map[string]string{
			"cpuMillicores": getenv("CPU_LIMIT_MILLICORES", "unset"),
			"memoryMiB":     getenv("MEMORY_LIMIT_MIB", "unset"),
		},
		"runtime": map[string]any{"go": runtime.Version(), "numCPU": runtime.NumCPU(), "goroutines": runtime.NumGoroutine()},
	}
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"service": "inspector-api",
		"version": s.cfg.Version,
		"endpoints": []string{
			"GET /whoami", "GET /headers", "GET /ip", "GET /env", "GET /config", "GET /secret",
			"ANY /status/{code}", "ANY /delay/{seconds}", "ANY /anything[/{path}]",
			"GET /livez", "GET /readyz", "GET /startupz",
			"POST /chaos/ready?state=true|false", "POST /chaos/live?state=true|false", "POST /chaos/crash",
			"POST /stress/cpu?seconds=30&workers=N", "POST /stress/memory?mb=100", "DELETE /stress/memory",
			"GET /metrics",
		},
	})
}

func (s *Server) handleWhoami(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.podInfo())
}

func (s *Server) handleHeaders(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"headers": r.Header, "servedBy": s.podName()})
}

func (s *Server) handleIP(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"clientIP":      clientIP(r),
		"remoteAddr":    r.RemoteAddr,
		"xForwardedFor": r.Header.Get("X-Forwarded-For"),
		"servedBy":      s.podName(),
	})
}

var sensitiveMarkers = []string{"SECRET", "PASSWORD", "TOKEN", "KEY", "CREDENTIAL"}

func isSensitive(name string) bool {
	upper := strings.ToUpper(name)
	for _, m := range sensitiveMarkers {
		if strings.Contains(upper, m) {
			return true
		}
	}
	return false
}

func mask(v string) string {
	if len(v) <= 2 {
		return "****"
	}
	return v[:2] + strings.Repeat("*", min(len(v)-2, 8))
}

func (s *Server) handleEnv(w http.ResponseWriter, r *http.Request) {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		if isSensitive(k) {
			v = mask(v)
		}
		env[k] = v
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"env":      env,
		"hint":     "Variables like *_SERVICE_HOST/PORT are injected by Kubernetes for service discovery.",
		"servedBy": s.podName(),
	})
}

// readMountedDir reads a ConfigMap/Secret volume, skipping the hidden ..data symlink machinery.
func readMountedDir(dir string) (map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		info, err := os.Stat(p)
		if err != nil || info.IsDir() {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		out[e.Name()] = string(b)
	}
	return out, nil
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	files, err := readMountedDir(s.cfg.ConfigDir)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"mounted": false,
			"path":    s.cfg.ConfigDir,
			"hint":    "Mount a ConfigMap at this path to see its keys as files.",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mounted":       true,
		"path":          s.cfg.ConfigDir,
		"files":         files,
		"envAppMessage": s.cfg.Message,
		"hint":          "Files refresh automatically after a ConfigMap edit (~1 min); env vars only change on pod restart.",
		"servedBy":      s.podName(),
	})
}

func (s *Server) handleSecret(w http.ResponseWriter, r *http.Request) {
	files, err := readMountedDir(s.cfg.SecretDir)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"mounted": false,
			"path":    s.cfg.SecretDir,
			"hint":    "Mount a Secret at this path to see its (masked) keys.",
		})
		return
	}
	masked := map[string]any{}
	for k, v := range files {
		masked[k] = map[string]any{"value": mask(v), "length": len(v)}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mounted":  true,
		"path":     s.cfg.SecretDir,
		"keys":     masked,
		"hint":     "Secrets are mounted as tmpfs files; values are masked here on purpose.",
		"servedBy": s.podName(),
	})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	code, err := strconv.Atoi(r.PathValue("code"))
	if err != nil || code < 200 || code > 599 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "code must be an integer between 200 and 599"})
		return
	}
	if code == http.StatusNoContent || code == http.StatusNotModified {
		w.WriteHeader(code)
		return
	}
	writeJSON(w, code, map[string]any{"status": code, "text": http.StatusText(code), "servedBy": s.podName()})
}

func (s *Server) handleDelay(w http.ResponseWriter, r *http.Request) {
	secs, err := strconv.ParseFloat(r.PathValue("seconds"), 64)
	if err != nil || secs < 0 || secs > maxDelaySeconds {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "seconds must be between 0 and 30"})
		return
	}
	select {
	case <-time.After(time.Duration(secs * float64(time.Second))):
		writeJSON(w, http.StatusOK, map[string]any{"delayedSeconds": secs, "servedBy": s.podName()})
	case <-r.Context().Done():
	}
}

func (s *Server) handleAnything(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "could not read body"})
		return
	}
	resp := map[string]any{
		"method":   r.Method,
		"path":     r.URL.Path,
		"query":    r.URL.Query(),
		"headers":  r.Header,
		"body":     string(body),
		"clientIP": clientIP(r),
		"servedBy": s.podName(),
	}
	var parsed any
	if len(body) > 0 && json.Unmarshal(body, &parsed) == nil {
		resp["json"] = parsed
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleLivez(w http.ResponseWriter, r *http.Request) {
	if !s.live.Load() {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "unhealthy", "pod": s.podName()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "alive"})
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	switch {
	case !s.started.Load():
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "starting"})
	case !s.ready.Load():
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not ready", "pod": s.podName()})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"status": "ready"})
	}
}

func (s *Server) handleStartupz(w http.ResponseWriter, r *http.Request) {
	if !s.started.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "starting"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "started"})
}

func toggle(flag *atomic.Bool, r *http.Request) (bool, error) {
	raw := r.URL.Query().Get("state")
	if raw == "" {
		v := !flag.Load()
		flag.Store(v)
		return v, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, err
	}
	flag.Store(v)
	return v, nil
}

func (s *Server) handleChaosReady(w http.ResponseWriter, r *http.Request) {
	v, err := toggle(&s.ready, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "state must be true or false"})
		return
	}
	slog.Warn("readiness changed", "ready", v)
	writeJSON(w, http.StatusOK, map[string]any{"pod": s.podName(), "ready": v,
		"effect": "Pod is removed from / re-added to Service endpoints by the readiness probe."})
}

func (s *Server) handleChaosLive(w http.ResponseWriter, r *http.Request) {
	v, err := toggle(&s.live, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "state must be true or false"})
		return
	}
	slog.Warn("liveness changed", "live", v)
	writeJSON(w, http.StatusOK, map[string]any{"pod": s.podName(), "live": v,
		"effect": "Kubelet restarts the container after the liveness probe fails failureThreshold times."})
}

func (s *Server) handleChaosCrash(w http.ResponseWriter, r *http.Request) {
	slog.Error("crash requested, exiting with code 1")
	writeJSON(w, http.StatusOK, map[string]any{"pod": s.podName(), "effect": "Process exits with code 1; repeated crashes lead to CrashLoopBackOff."})
	go func() {
		time.Sleep(200 * time.Millisecond)
		s.exit(1)
	}()
}

func queryInt(r *http.Request, key string, def, lo, hi int) (int, bool) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return def, true
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < lo || v > hi {
		return 0, false
	}
	return v, true
}

func (s *Server) handleStressCPU(w http.ResponseWriter, r *http.Request) {
	secs, ok1 := queryInt(r, "seconds", 30, 1, maxCPUSeconds)
	workers, ok2 := queryInt(r, "workers", runtime.NumCPU(), 1, maxCPUWorkers)
	if !ok1 || !ok2 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "seconds must be 1-300 and workers 1-64"})
		return
	}
	deadline := time.Now().Add(time.Duration(secs) * time.Second)
	for i := 0; i < workers; i++ {
		s.cpuWorkers.Add(1)
		go func() {
			defer s.cpuWorkers.Add(-1)
			x := 0
			for time.Now().Before(deadline) {
				for j := 0; j < 1_000_000; j++ {
					x += j
				}
			}
			_ = x
		}()
	}
	slog.Warn("cpu stress started", "seconds", secs, "workers", workers)
	writeJSON(w, http.StatusAccepted, map[string]any{"pod": s.podName(), "seconds": secs, "workers": workers,
		"effect": "CPU is throttled at the container limit; the HPA scales out when average utilization exceeds its target."})
}

func (s *Server) memoryHeld() int {
	total := 0
	for _, b := range s.memHog {
		total += len(b)
	}
	return total
}

func (s *Server) handleStressMemory(w http.ResponseWriter, r *http.Request) {
	mb, ok := queryInt(r, "mb", 100, 1, maxMemoryMB)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "mb must be 1-4096"})
		return
	}
	buf := make([]byte, mb<<20)
	// Touch every page so the memory is actually resident.
	for i := 0; i < len(buf); i += 4096 {
		buf[i] = 1
	}
	s.memMu.Lock()
	s.memHog = append(s.memHog, buf)
	held := s.memoryHeld()
	s.memMu.Unlock()
	slog.Warn("memory allocated", "addedMB", mb, "heldMB", held>>20)
	writeJSON(w, http.StatusOK, map[string]any{"pod": s.podName(), "addedMB": mb, "heldMB": held >> 20,
		"effect": "Exceeding the container memory limit gets the container OOMKilled (exit code 137)."})
}

func (s *Server) handleReleaseMemory(w http.ResponseWriter, r *http.Request) {
	s.memMu.Lock()
	s.memHog = nil
	s.memMu.Unlock()
	debug.FreeOSMemory()
	writeJSON(w, http.StatusOK, map[string]any{"pod": s.podName(), "heldMB": 0})
}

func boolGauge(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	s.memMu.Lock()
	held := s.memoryHeld()
	s.memMu.Unlock()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	s.metrics.Write(w, map[string]float64{
		"kubeplay_ready":              boolGauge(s.ready.Load()),
		"kubeplay_live":               boolGauge(s.live.Load()),
		"kubeplay_cpu_stress_workers": float64(s.cpuWorkers.Load()),
		"kubeplay_memory_hog_bytes":   float64(held),
		"kubeplay_uptime_seconds":     time.Since(s.start).Seconds(),
	})
}

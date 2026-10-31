package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Minimal OpenTelemetry tracing with the standard library only:
// W3C Trace Context (traceparent) propagation + OTLP/HTTP JSON export to a collector,
// which forwards spans to Azure Monitor Application Insights.

const (
	spanKindServer  = 2
	statusCodeError = 2
	maxQueuedSpans  = 2048
	maxBatchSpans   = 256
	flushInterval   = 5 * time.Second
)

type SpanContext struct {
	TraceID string // 32 lowercase hex chars
	SpanID  string // 16 lowercase hex chars
	Sampled bool
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func isLowerHex(s string) bool {
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

func isHex(s string, n int) bool {
	return len(s) == n && strings.Trim(s, "0") != "" && isLowerHex(s)
}

// parseTraceparent parses a W3C traceparent header: 00-<trace-id>-<parent-id>-<flags>.
func parseTraceparent(h string) (SpanContext, bool) {
	parts := strings.Split(strings.TrimSpace(h), "-")
	if len(parts) < 4 || len(parts[0]) != 2 || !isLowerHex(parts[0]) || parts[0] == "ff" ||
		len(parts[3]) != 2 || !isLowerHex(parts[3]) {
		return SpanContext{}, false
	}
	if parts[0] == "00" && len(parts) != 4 {
		return SpanContext{}, false
	}
	flags, err := strconv.ParseUint(parts[3], 16, 8)
	if err != nil || !isHex(parts[1], 32) || !isHex(parts[2], 16) {
		return SpanContext{}, false
	}
	return SpanContext{TraceID: parts[1], SpanID: parts[2], Sampled: flags&1 == 1}, true
}

func (sc SpanContext) Traceparent() string {
	flags := "00"
	if sc.Sampled {
		flags = "01"
	}
	return "00-" + sc.TraceID + "-" + sc.SpanID + "-" + flags
}

// childSpanContext continues the caller's trace, or starts a new sampled trace.
func childSpanContext(parent SpanContext, hasParent bool) SpanContext {
	if !hasParent {
		return SpanContext{TraceID: randomHex(16), SpanID: randomHex(8), Sampled: true}
	}
	return SpanContext{TraceID: parent.TraceID, SpanID: randomHex(8), Sampled: parent.Sampled}
}

type otlpValue struct {
	StringValue *string `json:"stringValue,omitempty"`
	IntValue    *string `json:"intValue,omitempty"`
}

type otlpAttr struct {
	Key   string    `json:"key"`
	Value otlpValue `json:"value"`
}

func strAttr(k, v string) otlpAttr { return otlpAttr{Key: k, Value: otlpValue{StringValue: &v}} }
func intAttr(k string, v int) otlpAttr {
	s := strconv.Itoa(v)
	return otlpAttr{Key: k, Value: otlpValue{IntValue: &s}}
}

type otlpStatus struct {
	Code int `json:"code,omitempty"`
}

type otlpSpan struct {
	TraceID      string     `json:"traceId"`
	SpanID       string     `json:"spanId"`
	ParentSpanID string     `json:"parentSpanId,omitempty"`
	Name         string     `json:"name"`
	Kind         int        `json:"kind"`
	Start        string     `json:"startTimeUnixNano"`
	End          string     `json:"endTimeUnixNano"`
	Attributes   []otlpAttr `json:"attributes"`
	Status       otlpStatus `json:"status"`
}

// Tracer batches finished spans and posts them to <endpoint>/v1/traces. A nil *Tracer is a no-op.
type Tracer struct {
	url      string
	resource []otlpAttr
	client   *http.Client
	queue    chan otlpSpan
	flush    chan chan struct{}
	stopOnce sync.Once
	failing  bool
}

// newTracerFromEnv returns nil (tracing disabled) unless an OTLP endpoint is configured.
func newTracerFromEnv(version string) *Tracer {
	url := getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	if url == "" {
		base := getenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
		if base == "" {
			return nil
		}
		url = strings.TrimRight(base, "/") + "/v1/traces"
	}
	pod := getenv("POD_NAME", "")
	if pod == "" {
		pod = randomHex(8)
	}
	resource := []otlpAttr{
		strAttr("service.name", getenv("OTEL_SERVICE_NAME", "inspector-api")),
		strAttr("service.version", version),
		// Becomes cloud_RoleInstance in Application Insights.
		strAttr("service.instance.id", pod),
		strAttr("k8s.pod.name", pod),
		strAttr("k8s.namespace.name", getenv("POD_NAMESPACE", "unknown")),
		strAttr("k8s.node.name", getenv("NODE_NAME", "unknown")),
		strAttr("telemetry.sdk.language", "go"),
	}
	return newTracer(url, resource)
}

func newTracer(url string, resource []otlpAttr) *Tracer {
	t := &Tracer{
		url:      url,
		resource: resource,
		client:   &http.Client{Timeout: 5 * time.Second},
		queue:    make(chan otlpSpan, maxQueuedSpans),
		flush:    make(chan chan struct{}),
	}
	go t.run()
	slog.Info("tracing enabled", "otlpEndpoint", url)
	return t
}

// record queues a span without blocking the request; spans are dropped if the queue is full.
func (t *Tracer) record(s otlpSpan) {
	if t == nil {
		return
	}
	select {
	case t.queue <- s:
	default:
	}
}

func (t *Tracer) run() {
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	var batch []otlpSpan
	for {
		select {
		case s := <-t.queue:
			batch = append(batch, s)
			if len(batch) >= maxBatchSpans {
				t.export(batch)
				batch = nil
			}
		case <-ticker.C:
			t.export(batch)
			batch = nil
		case done := <-t.flush:
			for len(t.queue) > 0 {
				batch = append(batch, <-t.queue)
			}
			t.export(batch)
			batch = nil
			close(done)
		}
	}
}

func (t *Tracer) export(spans []otlpSpan) {
	if len(spans) == 0 {
		return
	}
	body, _ := json.Marshal(map[string]any{
		"resourceSpans": []any{map[string]any{
			"resource": map[string]any{"attributes": t.resource},
			"scopeSpans": []any{map[string]any{
				"scope": map[string]any{"name": "kubeplay/inspector-api"},
				"spans": spans,
			}},
		}},
	})
	resp, err := t.client.Post(t.url, "application/json", bytes.NewReader(body))
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			err = &exportError{resp.StatusCode}
		}
	}
	// Log only on state changes so an unreachable collector doesn't flood the logs.
	if err != nil && !t.failing {
		slog.Warn("trace export failed, dropping spans", "err", err, "spans", len(spans))
	} else if err == nil && t.failing {
		slog.Info("trace export recovered")
	}
	t.failing = err != nil
}

type exportError struct{ status int }

func (e *exportError) Error() string { return "collector returned HTTP " + strconv.Itoa(e.status) }

// Shutdown exports any queued spans; call it before the process exits.
func (t *Tracer) Shutdown(ctx context.Context) {
	if t == nil {
		return
	}
	t.stopOnce.Do(func() {
		done := make(chan struct{})
		select {
		case t.flush <- done:
			select {
			case <-done:
			case <-ctx.Done():
			}
		case <-ctx.Done():
		}
	})
}

// serverSpan builds the SERVER span for one handled request using OpenTelemetry HTTP semantic conventions.
func serverSpan(sc, parent SpanContext, hasParent bool, r *http.Request, status int, start, end time.Time) otlpSpan {
	// ServeMux patterns may carry a method ("GET /whoami"); http.route is only the path template.
	route := r.Pattern
	if _, path, ok := strings.Cut(route, " "); ok {
		route = path
	}
	name := r.Method
	attrs := []otlpAttr{
		strAttr("http.request.method", r.Method),
		strAttr("url.path", r.URL.Path),
		strAttr("url.scheme", "http"),
		strAttr("server.address", r.Host),
		strAttr("client.address", clientIP(r)),
		intAttr("http.response.status_code", status),
	}
	if route != "" {
		name += " " + route
		attrs = append(attrs, strAttr("http.route", route))
	}
	span := otlpSpan{
		TraceID:    sc.TraceID,
		SpanID:     sc.SpanID,
		Name:       name,
		Kind:       spanKindServer,
		Start:      strconv.FormatInt(start.UnixNano(), 10),
		End:        strconv.FormatInt(end.UnixNano(), 10),
		Attributes: attrs,
	}
	if hasParent {
		span.ParentSpanID = parent.SpanID
	}
	if status >= 500 {
		span.Status.Code = statusCodeError
	}
	return span
}

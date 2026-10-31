import fakeredis
import pytest
from fastapi.testclient import TestClient

from app.main import create_app


@pytest.fixture
def client():
    app = create_app(lambda: fakeredis.FakeAsyncRedis(decode_responses=True))
    with TestClient(app) as c:
        yield c


def test_health_and_ready(client):
    assert client.get("/healthz").status_code == 200
    assert client.get("/readyz").json() == {"status": "ready"}


def test_note_crud(client):
    created = client.post("/notes", json={"title": "hello", "body": "k8s"})
    assert created.status_code == 201
    note_id = created.json()["id"]

    listed = client.get("/notes").json()
    assert listed["count"] == 1 and listed["notes"][0]["title"] == "hello"
    assert client.get(f"/notes/{note_id}").json()["body"] == "k8s"

    assert client.delete(f"/notes/{note_id}").status_code == 204
    assert client.get(f"/notes/{note_id}").status_code == 404


def test_note_validation(client):
    assert client.post("/notes", json={"title": ""}).status_code == 422


def test_enqueue_job(client):
    resp = client.post("/jobs", json={"type": "slow", "seconds": 1})
    assert resp.status_code == 202
    assert resp.json()["queueLength"] == 1
    assert client.get("/jobs").json()["queueLength"] == 1


def test_readiness_fails_without_redis():
    import redis.asyncio as aioredis

    app = create_app(lambda: aioredis.Redis(host="127.0.0.1", port=1, socket_connect_timeout=0.2))
    with TestClient(app) as c:
        assert c.get("/readyz").status_code == 503
        assert c.get("/notes").status_code == 503


TRACEPARENT = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"


@pytest.fixture
def traced():
    from opentelemetry.instrumentation.fastapi import FastAPIInstrumentor
    from opentelemetry.instrumentation.redis import RedisInstrumentor
    from opentelemetry.sdk.trace import TracerProvider
    from opentelemetry.sdk.trace.export import SimpleSpanProcessor
    from opentelemetry.sdk.trace.export.in_memory_span_exporter import InMemorySpanExporter

    exporter = InMemorySpanExporter()
    provider = TracerProvider()
    provider.add_span_processor(SimpleSpanProcessor(exporter))
    app = create_app(lambda: fakeredis.FakeAsyncRedis(decode_responses=True), tracer_provider=provider)
    with TestClient(app) as c:
        yield c, exporter
    FastAPIInstrumentor.uninstrument_app(app)
    RedisInstrumentor().uninstrument()


def test_tracing_disabled_without_endpoint(monkeypatch):
    from app.main import tracer_provider_from_env

    monkeypatch.delenv("OTEL_EXPORTER_OTLP_ENDPOINT", raising=False)
    monkeypatch.delenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", raising=False)
    assert tracer_provider_from_env() is None


def test_untraced_job_has_no_trace_context(client):
    # Without tracing the job payload is unchanged.
    assert "traceContext" not in client.post("/jobs", json={"type": "report"}).json()


def test_traced_job_continues_gateway_trace(traced):
    client, exporter = traced
    job = client.post("/jobs", json={"type": "report"}, headers={"traceparent": TRACEPARENT}).json()
    trace_id = TRACEPARENT.split("-")[1]
    assert job["traceContext"]["traceparent"].split("-")[1] == trace_id

    spans = exporter.get_finished_spans()
    server = [s for s in spans if s.kind.name == "SERVER"]
    assert len(server) == 1 and server[0].attributes["http.route"] == "/jobs"
    assert format(server[0].context.trace_id, "032x") == trace_id
    assert format(server[0].parent.span_id, "016x") == TRACEPARENT.split("-")[2]
    redis_spans = [s for s in spans if s.kind.name == "CLIENT" and s.attributes.get("db.system") == "redis"]
    assert {s.name for s in redis_spans} >= {"RPUSH", "LLEN"}
    assert all(s.context.trace_id == server[0].context.trace_id for s in redis_spans)


def test_probes_are_not_traced(traced):
    client, exporter = traced
    assert client.get("/readyz").status_code == 200
    assert client.get("/healthz").status_code == 200
    assert exporter.get_finished_spans() == ()

import json

import fakeredis
import pytest

import worker


@pytest.fixture
def r():
    return fakeredis.FakeRedis(decode_responses=True)


def test_report_job_counts_notes(r):
    worker.seed(r, 3)
    result = worker.process_job(r, {"jobId": "j1", "type": "report"})
    assert result["output"]["totalNotes"] == 3
    stored = json.loads(r.lindex(worker.RESULTS_KEY, 0))
    assert stored["jobId"] == "j1" and stored["status"] == "done"


def test_slow_job_is_capped(r, monkeypatch):
    slept = []
    monkeypatch.setattr(worker.time, "sleep", slept.append)
    result = worker.process_job(r, {"type": "slow", "payload": {"seconds": 999}})
    assert slept == [60] and result["output"]["sleptSeconds"] == 60


def test_unknown_job_raises(r):
    with pytest.raises(ValueError):
        worker.process_job(r, {"type": "nope"})


def test_results_are_trimmed(r):
    for i in range(worker.MAX_RESULTS + 10):
        worker.store_result(r, {"jobId": str(i)})
    assert r.llen(worker.RESULTS_KEY) == worker.MAX_RESULTS


def test_scheduled_report(r):
    worker.run_report(r)
    assert json.loads(r.lindex(worker.RESULTS_KEY, 0))["type"] == "scheduled-report"


@pytest.fixture(scope="module")
def spans():
    from opentelemetry import trace
    from opentelemetry.sdk.trace import TracerProvider
    from opentelemetry.sdk.trace.export import SimpleSpanProcessor
    from opentelemetry.sdk.trace.export.in_memory_span_exporter import InMemorySpanExporter

    exporter = InMemorySpanExporter()
    provider = TracerProvider()
    provider.add_span_processor(SimpleSpanProcessor(exporter))
    trace.set_tracer_provider(provider)
    return exporter


def test_tracing_disabled_without_endpoint(monkeypatch):
    monkeypatch.delenv("OTEL_EXPORTER_OTLP_ENDPOINT", raising=False)
    monkeypatch.delenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", raising=False)
    assert worker.tracer_provider_from_env("consume") is None


def test_job_continues_trace_from_queue(r, spans):
    spans.clear()
    traceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
    job = {"jobId": "j2", "type": "report", "traceContext": {"traceparent": traceparent}}
    worker.handle_message(r, json.dumps(job))

    stored = json.loads(r.lindex(worker.RESULTS_KEY, 0))
    assert stored["jobId"] == "j2" and stored["traceId"] == "4bf92f3577b34da6a3ce929d0e0e4736"
    (span,) = spans.get_finished_spans()
    assert span.name == "process report" and span.kind.name == "CONSUMER"
    assert format(span.parent.span_id, "016x") == "00f067aa0ba902b7"
    assert span.attributes["messaging.message.id"] == "j2"


def test_failed_job_is_recorded_on_span(r, spans):
    spans.clear()
    worker.handle_message(r, json.dumps({"jobId": "j3", "type": "nope"}))
    assert json.loads(r.lindex(worker.RESULTS_KEY, 0))["status"] == "failed"
    (span,) = spans.get_finished_spans()
    assert span.status.status_code.name == "ERROR"


def test_scheduled_report_starts_new_trace(r, spans):
    spans.clear()
    worker.run_report(r)
    stored = json.loads(r.lindex(worker.RESULTS_KEY, 0))
    (span,) = spans.get_finished_spans()
    assert span.name == "scheduled report" and span.parent is None
    assert stored["traceId"] == format(span.context.trace_id, "032x")

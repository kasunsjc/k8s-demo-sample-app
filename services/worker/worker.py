"""KubePlay worker: queue consumer (Deployment), scheduled report (CronJob), and seeder (Job)."""

import json
import logging
import os
import signal
import socket
import sys
import time
import uuid
from datetime import datetime, timezone

os.environ["OTEL_SEMCONV_STABILITY_OPT_IN"] = "http/dup,database/dup"

import redis
from opentelemetry import propagate, trace
from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter
from opentelemetry.instrumentation.redis import RedisInstrumentor
from opentelemetry.instrumentation.utils import suppress_instrumentation
from opentelemetry.sdk.resources import Resource
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor
from opentelemetry.trace import SpanKind, Status, StatusCode

NOTES_KEY = "kubeplay:notes"
QUEUE_KEY = "kubeplay:jobs"
RESULTS_KEY = "kubeplay:results"
MAX_RESULTS = 50

POD_NAME = os.getenv("POD_NAME", socket.gethostname())
HEARTBEAT_FILE = os.getenv("HEARTBEAT_FILE", "/tmp/heartbeat")

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
log = logging.getLogger("worker")
tracer = trace.get_tracer("kubeplay.worker")

_stopping = False


def now() -> str:
    return datetime.now(timezone.utc).isoformat()


def tracer_provider_from_env(mode: str) -> TracerProvider | None:
    """OpenTelemetry tracing is off unless OTEL_EXPORTER_OTLP_ENDPOINT points at an OpenTelemetry Collector."""
    if not (os.getenv("OTEL_EXPORTER_OTLP_ENDPOINT") or os.getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")):
        return None
    attributes = {
        "service.name": "worker",
        "service.instance.id": POD_NAME,
        "k8s.pod.name": POD_NAME,
        "kubeplay.worker.mode": mode,
    }
    for key, env in (("k8s.namespace.name", "POD_NAMESPACE"), ("k8s.node.name", "NODE_NAME")):
        if os.getenv(env):
            attributes[key] = os.environ[env]
    resource = Resource.create(attributes)
    provider = TracerProvider(resource=resource)
    provider.add_span_processor(BatchSpanProcessor(OTLPSpanExporter()))
    trace.set_tracer_provider(provider)
    RedisInstrumentor().instrument(tracer_provider=provider)
    return provider


def current_trace_id() -> str | None:
    ctx = trace.get_current_span().get_span_context()
    return format(ctx.trace_id, "032x") if ctx.is_valid else None


def connect() -> redis.Redis:
    return redis.Redis(
        host=os.getenv("REDIS_HOST", "localhost"),
        port=int(os.getenv("REDIS_PORT", "6379")),
        password=os.getenv("REDIS_PASSWORD") or None,
        decode_responses=True,
        socket_connect_timeout=5,
        socket_timeout=15,
    )


def heartbeat() -> None:
    with open(HEARTBEAT_FILE, "w") as f:
        f.write(str(time.time()))


def store_result(r: redis.Redis, result: dict) -> None:
    r.lpush(RESULTS_KEY, json.dumps(result))
    r.ltrim(RESULTS_KEY, 0, MAX_RESULTS - 1)


def build_report(r: redis.Redis) -> dict:
    notes = [json.loads(n) for n in r.hvals(NOTES_KEY)]
    by_pod: dict[str, int] = {}
    for n in notes:
        by_pod[n.get("createdBy", "unknown")] = by_pod.get(n.get("createdBy", "unknown"), 0) + 1
    return {"totalNotes": len(notes), "notesByPod": by_pod, "queueLength": r.llen(QUEUE_KEY)}


def process_job(r: redis.Redis, job: dict) -> dict:
    job_type = job.get("type")
    if job_type == "report":
        output = build_report(r)
    elif job_type == "slow":
        seconds = min(max(float(job.get("payload", {}).get("seconds", 5)), 0), 60)
        time.sleep(seconds)
        output = {"sleptSeconds": seconds}
    else:
        raise ValueError(f"unknown job type: {job_type!r}")
    result = {
        "jobId": job.get("jobId", "unknown"),
        "type": job_type,
        "status": "done",
        "processedBy": POD_NAME,
        "finishedAt": now(),
        "output": output,
    }
    if trace_id := current_trace_id():
        result["traceId"] = trace_id
    store_result(r, result)
    return result


def _handle_sigterm(signum, _frame) -> None:
    global _stopping
    log.info("signal %s received, finishing current job then exiting", signum)
    _stopping = True


def handle_message(r: redis.Redis, raw: str) -> None:
    try:
        job = json.loads(raw)
        # Continue the trace started by the browser -> ui-gateway -> notes-api request that enqueued the job.
        parent = propagate.extract(job.get("traceContext") or {})
        with tracer.start_as_current_span(
            f"process {job.get('type')}",
            context=parent,
            kind=SpanKind.CONSUMER,
            attributes={
                "messaging.system": "redis",
                "messaging.operation.type": "process",
                "messaging.destination.name": QUEUE_KEY,
                "messaging.message.id": str(job.get("jobId", "unknown")),
            },
        ) as span:
            try:
                result = process_job(r, job)
            except ValueError as exc:
                span.record_exception(exc)
                span.set_status(Status(StatusCode.ERROR, str(exc)))
                result = {
                    "jobId": job.get("jobId", "unknown"),
                    "status": "failed",
                    "error": str(exc),
                    "processedBy": POD_NAME,
                    "finishedAt": now(),
                    "traceId": current_trace_id(),
                }
                store_result(r, result)
                log.error("job failed: %s", exc)
                return
        log.info("job %s (%s) done", result["jobId"], result["type"])
    except (ValueError, json.JSONDecodeError) as exc:
        log.error("job failed: %s", exc)
        store_result(r, {"jobId": "unknown", "status": "failed", "error": str(exc), "processedBy": POD_NAME, "finishedAt": now()})


def consume(r: redis.Redis) -> None:
    signal.signal(signal.SIGTERM, _handle_sigterm)
    signal.signal(signal.SIGINT, _handle_sigterm)
    log.info("worker %s consuming %s", POD_NAME, QUEUE_KEY)
    while not _stopping:
        heartbeat()
        try:
            # Idle polling is not traced; only processing a job is.
            with suppress_instrumentation():
                item = r.blpop([QUEUE_KEY], timeout=5)
        except redis.RedisError as exc:
            log.warning("redis error: %s; retrying in 2s", exc)
            time.sleep(2)
            continue
        if not item:
            continue
        handle_message(r, item[1])
    log.info("worker stopped cleanly")


def run_report(r: redis.Redis) -> None:
    with tracer.start_as_current_span("scheduled report"):
        result = {
            "jobId": f"cron-{uuid.uuid4().hex[:8]}",
            "type": "scheduled-report",
            "status": "done",
            "processedBy": POD_NAME,
            "finishedAt": now(),
            "output": build_report(r),
        }
        if trace_id := current_trace_id():
            result["traceId"] = trace_id
        store_result(r, result)
    log.info("scheduled report: %s", json.dumps(result["output"]))


def seed(r: redis.Redis, count: int) -> None:
    with tracer.start_as_current_span("seed notes", attributes={"kubeplay.seed.count": count}):
        for i in range(1, count + 1):
            note = {
                "id": uuid.uuid4().hex[:12],
                "title": f"Seeded note #{i}",
                "body": "Created by a Kubernetes Job",
                "createdAt": now(),
                "createdBy": POD_NAME,
            }
            r.hset(NOTES_KEY, note["id"], json.dumps(note))
    log.info("seeded %d notes", count)


def main(argv: list[str]) -> int:
    mode = argv[1] if len(argv) > 1 else "consume"
    provider = tracer_provider_from_env(mode)
    r = connect()
    try:
        if mode == "consume":
            consume(r)
        elif mode == "report":
            run_report(r)
        elif mode == "seed":
            seed(r, int(os.getenv("SEED_COUNT", "5")))
        else:
            log.error("unknown mode %r (expected consume|report|seed)", mode)
            return 2
        return 0
    finally:
        if provider is not None:
            provider.shutdown()  # Jobs/CronJobs exit right away, so flush spans first


if __name__ == "__main__":
    sys.exit(main(sys.argv))

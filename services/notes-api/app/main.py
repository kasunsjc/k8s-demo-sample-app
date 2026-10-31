import json
import os
import socket
import uuid
from contextlib import asynccontextmanager
from datetime import datetime, timezone
from typing import Callable, Literal

import redis.asyncio as aioredis
from fastapi import FastAPI, HTTPException, Request, Response, status
from fastapi.responses import JSONResponse
from opentelemetry import propagate, trace
from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter
from opentelemetry.instrumentation.fastapi import FastAPIInstrumentor
from opentelemetry.instrumentation.redis import RedisInstrumentor
from opentelemetry.instrumentation.utils import suppress_instrumentation
from opentelemetry.sdk.resources import Resource
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor
from pydantic import BaseModel, Field
from redis.exceptions import RedisError

NOTES_KEY = "kubeplay:notes"
QUEUE_KEY = "kubeplay:jobs"
RESULTS_KEY = "kubeplay:results"

POD_NAME = os.getenv("POD_NAME", socket.gethostname())
APP_VERSION = os.getenv("APP_VERSION", "v1")


class NoteIn(BaseModel):
    title: str = Field(min_length=1, max_length=200)
    body: str = Field(default="", max_length=5000)


class JobIn(BaseModel):
    type: Literal["report", "slow"]
    seconds: float = Field(default=5, ge=0, le=60)


def redis_from_env() -> aioredis.Redis:
    return aioredis.Redis(
        host=os.getenv("REDIS_HOST", "localhost"),
        port=int(os.getenv("REDIS_PORT", "6379")),
        password=os.getenv("REDIS_PASSWORD") or None,
        decode_responses=True,
        socket_connect_timeout=2,
        socket_timeout=2,
    )


def tracer_provider_from_env() -> TracerProvider | None:
    """OpenTelemetry tracing is off unless OTEL_EXPORTER_OTLP_ENDPOINT points at an OpenTelemetry Collector."""
    if not (os.getenv("OTEL_EXPORTER_OTLP_ENDPOINT") or os.getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")):
        return None
    resource = Resource.create({
        "service.name": "notes-api",
        "service.version": APP_VERSION,
        # Becomes cloud_RoleInstance in Application Insights.
        "service.instance.id": POD_NAME,
        "k8s.pod.name": POD_NAME,
        "k8s.namespace.name": os.getenv("POD_NAMESPACE", "unknown"),
        "k8s.node.name": os.getenv("NODE_NAME", "unknown"),
    })
    provider = TracerProvider(resource=resource)
    provider.add_span_processor(BatchSpanProcessor(OTLPSpanExporter()))
    trace.set_tracer_provider(provider)
    return provider


def now() -> str:
    return datetime.now(timezone.utc).isoformat()


def create_app(
    redis_factory: Callable[[], aioredis.Redis] = redis_from_env,
    tracer_provider: TracerProvider | None = None,
) -> FastAPI:
    @asynccontextmanager
    async def lifespan(app: FastAPI):
        app.state.redis = redis_factory()
        yield
        await app.state.redis.aclose()
        if tracer_provider is not None:
            tracer_provider.shutdown()  # flush queued spans before the pod exits

    # ROOT_PATH lets Swagger UI work behind the gateway prefix (/api/notes).
    app = FastAPI(title="KubePlay Notes API", version=APP_VERSION, lifespan=lifespan, root_path=os.getenv("ROOT_PATH", ""))
    if tracer_provider is not None:
        # SERVER spans continue the gateway's traceparent; every Redis command becomes a child CLIENT span.
        FastAPIInstrumentor.instrument_app(app, tracer_provider=tracer_provider, excluded_urls="healthz,readyz")
        RedisInstrumentor().instrument(tracer_provider=tracer_provider)

    def db(request: Request) -> aioredis.Redis:
        return request.app.state.redis

    @app.exception_handler(RedisError)
    async def redis_unavailable(_: Request, exc: RedisError):
        return JSONResponse(
            status_code=status.HTTP_503_SERVICE_UNAVAILABLE,
            content={"error": "redis unavailable", "detail": type(exc).__name__, "servedBy": POD_NAME},
        )

    @app.get("/")
    async def info():
        return {
            "service": "notes-api",
            "pod": POD_NAME,
            "namespace": os.getenv("POD_NAMESPACE", "unknown"),
            "node": os.getenv("NODE_NAME", "unknown"),
            "version": APP_VERSION,
            "redis": f"{os.getenv('REDIS_HOST', 'localhost')}:{os.getenv('REDIS_PORT', '6379')}",
        }

    @app.get("/healthz")
    async def healthz():
        return {"status": "alive"}

    @app.get("/readyz")
    async def readyz(request: Request, response: Response):
        # Readiness depends on Redis: if it is down, this pod leaves the Service but is not restarted.
        try:
            # Probe traffic is not traced.
            with suppress_instrumentation():
                await db(request).ping()
        except (RedisError, OSError):
            response.status_code = status.HTTP_503_SERVICE_UNAVAILABLE
            return {"status": "not ready", "reason": "redis unreachable"}
        return {"status": "ready"}

    @app.get("/notes")
    async def list_notes(request: Request):
        raw = await db(request).hvals(NOTES_KEY)
        notes = sorted((json.loads(n) for n in raw), key=lambda n: n["createdAt"], reverse=True)
        return {"count": len(notes), "notes": notes, "servedBy": POD_NAME}

    @app.post("/notes", status_code=status.HTTP_201_CREATED)
    async def create_note(note: NoteIn, request: Request):
        item = {
            "id": uuid.uuid4().hex[:12],
            "title": note.title,
            "body": note.body,
            "createdAt": now(),
            "createdBy": POD_NAME,
        }
        await db(request).hset(NOTES_KEY, item["id"], json.dumps(item))
        return item

    @app.get("/notes/{note_id}")
    async def get_note(note_id: str, request: Request):
        raw = await db(request).hget(NOTES_KEY, note_id)
        if raw is None:
            raise HTTPException(status_code=404, detail="note not found")
        return json.loads(raw)

    @app.delete("/notes/{note_id}", status_code=status.HTTP_204_NO_CONTENT)
    async def delete_note(note_id: str, request: Request):
        if not await db(request).hdel(NOTES_KEY, note_id):
            raise HTTPException(status_code=404, detail="note not found")
        return Response(status_code=status.HTTP_204_NO_CONTENT)

    @app.post("/jobs", status_code=status.HTTP_202_ACCEPTED)
    async def enqueue_job(job: JobIn, request: Request):
        item = {
            "jobId": uuid.uuid4().hex[:12],
            "type": job.type,
            "payload": {"seconds": job.seconds},
            "enqueuedAt": now(),
            "enqueuedBy": POD_NAME,
        }
        # Carry the W3C trace context inside the job so the worker continues the same trace.
        trace_context: dict[str, str] = {}
        propagate.inject(trace_context)
        if trace_context:
            item["traceContext"] = trace_context
        r = db(request)
        await r.rpush(QUEUE_KEY, json.dumps(item))
        return {**item, "queueLength": await r.llen(QUEUE_KEY)}

    @app.get("/jobs")
    async def job_status(request: Request):
        r = db(request)
        results = [json.loads(x) for x in await r.lrange(RESULTS_KEY, 0, 19)]
        return {"queueLength": await r.llen(QUEUE_KEY), "results": results}

    return app


app = create_app(tracer_provider=tracer_provider_from_env())

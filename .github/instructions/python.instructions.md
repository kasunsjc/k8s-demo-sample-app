---
description: "Python conventions for notes-api (FastAPI) and worker"
applyTo: "services/notes-api/**,services/worker/**"
---
# Python services

- Python 3.12, type hints on public functions, pinned with `~=` in `requirements.txt`; test-only deps go in `requirements-dev.txt`.
- notes-api: build the app in `create_app(redis_factory)` so tests can inject `fakeredis.FakeAsyncRedis`. Use Pydantic models with bounds (`Field(min_length=..., le=...)`) for all request bodies.
- Redis failures surface as HTTP 503 via the `RedisError` exception handler; `/readyz` must stay dependency-aware (ping Redis), `/healthz` must not.
- worker: keep mode dispatch in `main()` (`consume`, `report`, `seed`). The consumer must write the heartbeat each loop and stop cleanly on SIGTERM after the current job.
- Store results with `store_result` (bounded list). Share Redis key names with the other service — don't invent new keys without updating both.
- Tests use `fakeredis`; run `python -m pytest -q` from the service directory.

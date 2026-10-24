# KubePlay — project guidelines

KubePlay is a polyglot demo app for teaching Kubernetes. Every feature should make a Kubernetes concept **visible** (in `kubectl`, logs, or the UI). Keep services small and readable — this code is shown on screen during demos.

## Architecture
- `services/ui-gateway` — Node.js 22, **zero npm dependencies**. Serves `public/` and proxies `/api/inspector/*` and `/api/notes/*`.
- `services/inspector-api` — Go 1.23, **standard library only**, distroless image. httpbin-style, Kubernetes-aware endpoints.
- `services/notes-api` — Python 3.12 FastAPI + Redis.
- `services/worker` — Python 3.12; modes `consume` (Deployment), `report` (CronJob), `seed` (Job).
- Redis StatefulSet is the only stateful component. Redis keys: `kubeplay:notes`, `kubeplay:jobs`, `kubeplay:results`.
- Services reach each other via plain Service DNS names (`http://inspector-api`, `redis`). Resource names are fixed (one release per namespace).

## Deployment artifacts must stay in sync
Any change to a container's env, ports, probes, resources or security context must be applied to **all** of:
1. `deploy/kustomize/base/*.yaml`
2. `deploy/helm/kubeplay/templates/*.yaml` (+ `values.yaml` if configurable)
3. `docker-compose.yml` (when relevant locally)
4. `README.md` concept map and `demos/README.md` when behaviour changes

## Keep the README current
Before finishing any task that adds, removes or renames a Make target, endpoint, service, dashboard panel, port, compose/deploy setting or skill, run the `readme-check` skill and update `README.md` in the same change. A task is not done while the README describes something that no longer exists.

## Branching (Git Flow)
All changes follow Git Flow — use the `git-flow` skill. Never commit directly to `main` or `develop`: work on `feature/*` or `bugfix/*` from `develop`, `release/x.y.z` from `develop`, and `hotfix/x.y.z` from `main`. Pushes to `main` publish images to Docker Hub via `.github/workflows/dockerhub.yml`, so only release/hotfix merges land there. Use Conventional Commit messages.

## Build and test
```sh
make test     # go test, pytest, node --test
make lint     # helm lint + kubectl kustomize render
make kind-up kind-load deploy-kustomize   # local cluster, UI at http://localhost:8088
```
Python tests need `pip install -r requirements-dev.txt` in each Python service.

## Conventions
- Containers run non-root with a **numeric** UID (distroless `65532`, Python `10001`, Node `1000`), `readOnlyRootFilesystem: true`, `capabilities: drop: [ALL]`, `seccompProfile: RuntimeDefault`.
- Every service reads pod identity from the Downward API (`POD_NAME`, `POD_NAMESPACE`, `NODE_NAME`) and includes `pod`/`servedBy` in responses so load balancing is visible.
- Chaos/stress endpoints must validate and cap inputs and explain their effect in an `effect` field.
- Secrets in the repo are demo values only; never log or return secret values unmasked.
- No new runtime dependencies without a strong reason.

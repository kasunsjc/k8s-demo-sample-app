---
name: add-k8s-concept
description: "Add a new Kubernetes concept demo to KubePlay end to end: endpoint or workload, manifests (Kustomize + Helm), UI button, tests, and demo docs. Use when: adding a new endpoint, new chaos action, new Kubernetes feature demo (e.g. sidecar, RBAC, VPA, KEDA, Gateway API), or extending inspector-api/notes-api/worker."
argument-hint: "Kubernetes concept to demonstrate, e.g. 'sidecar container' or 'RBAC with the API server'"
---

# Add a Kubernetes concept to KubePlay

## 1. Design
- Name the concept and the **observable effect** (what changes in `kubectl get ...`, logs or the UI).
- Pick the owner:
  - Pod/runtime behaviour (probes, resources, signals, env) → `services/inspector-api`
  - Stateful/data/dependency behaviour → `services/notes-api` + Redis
  - Batch/async/scaling on queue → `services/worker`
  - Pure platform objects (RBAC, quotas, policies) → manifests only

## 2. Implement the service change
- Follow the matching file instructions (`.github/instructions/*.instructions.md`).
- Include `pod`/`servedBy` and an `effect` string in responses.
- Bound all inputs; add unit tests next to existing ones.

## 3. Wire manifests (both!)
- `deploy/kustomize/base/<service>.yaml`
- `deploy/helm/kubeplay/templates/<service>.yaml` and, if configurable, `values.yaml` (feature toggle `enabled: true|false` for optional features)
- Update NetworkPolicy if a new pod talks to Redis or notes-api.
- Environment-specific differences go in `overlays/{local,aks}` and `values-{local,aks}.yaml`.

## 4. Surface it in the UI (optional)
- Add a button in `services/ui-gateway/public/index.html` using `data-method` + `data-url` (chaos) or `data-inspect` (read-only).

## 5. Document
- Add a row to the concept map in `README.md`.
- Add a numbered scenario in `demos/README.md` with copy-paste commands and the expected result. Put extra YAML in `demos/manifests/`.

## 6. Verify
```sh
make test && make lint
make kind-load deploy-kustomize      # or deploy-helm
```
Run the new demo scenario against the kind cluster and confirm the observable effect.

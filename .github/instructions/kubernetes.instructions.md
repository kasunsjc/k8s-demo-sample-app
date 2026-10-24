---
description: "Conventions for Kubernetes manifests, Kustomize overlays and the Helm chart"
applyTo: "deploy/**,demos/manifests/**"
---
# Kubernetes manifests

- Kustomize base (`deploy/kustomize/base`) and Helm templates (`deploy/helm/kubeplay/templates`) describe the **same** resources — change both together. Overlays/values only change environment-specific bits (image registry/tag, Service type, ingress class, storage class).
- Selectors use only `app.kubernetes.io/name`; never add mutable labels to selectors.
- Every container: requests + limits, probes appropriate to the workload, numeric `runAsUser`, `runAsNonRoot`, `allowPrivilegeEscalation: false`, `readOnlyRootFilesystem: true` (mount `emptyDir` where writes are needed), `capabilities.drop: [ALL]`, `seccompProfile: RuntimeDefault`, `automountServiceAccountToken: false` unless the API is needed.
- Pods that talk to Redis must have their `app.kubernetes.io/name` added to the `redis-allow-clients` NetworkPolicy (both Kustomize and Helm).
- The inspector ConfigMap intentionally has no hash suffix/checksum annotation — it demonstrates hot reload of mounted files vs. env vars.
- Local overlay: NodePort 30080 (kind maps to localhost:8088), no Ingress. AKS: `ACR_NAME.azurecr.io` placeholder, `managed-csi`, `webapprouting.kubernetes.azure.com`.
- Validate with `make lint`; for Helm also `helm template kubeplay deploy/helm/kubeplay -f <values>`.

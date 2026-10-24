---
name: deploy-kubeplay
description: "Build, deploy and troubleshoot KubePlay on kind, minikube, Docker Desktop or AKS using Kustomize or Helm. Use when: setting up a demo cluster, deploying or redeploying KubePlay, loading images into kind, pushing to ACR, pods not ready, CreateContainerConfigError, ImagePullBackOff, HPA shows unknown, UI not reachable on localhost:8088."
argument-hint: "Target (kind | minikube | docker-desktop | aks) and method (kustomize | helm)"
---

# Deploy KubePlay

## Safety first
- Check `kubectl config current-context` before applying anything. Never deploy to a context the user didn't name.
- For kind without touching the user's kubeconfig: `kind get kubeconfig --name kubeplay > /tmp/kubeplay.kubeconfig && chmod 600 /tmp/kubeplay.kubeconfig && export KUBECONFIG=/tmp/kubeplay.kubeconfig`.

## kind (default demo setup)
```sh
make kind-up            # 3 nodes + metrics-server; maps NodePort 30080 -> localhost:8088
make kind-load          # docker build kubeplay/<svc>:dev + kind load
make deploy-kustomize   # or: make deploy-helm
kubectl get pods,svc,pvc,hpa -n kubeplay
```
After code changes: `make kind-load` then `kubectl rollout restart deploy/<svc> -n kubeplay` (tag `dev` doesn't change).

## minikube / Docker Desktop
Build into the cluster's runtime (`eval $(minikube docker-env)` then `make build`), deploy, then
`kubectl port-forward svc/ui-gateway 8088:80 -n kubeplay`.

## AKS
Prereqs: ACR attached (`az aks update --attach-acr <acr>`), app routing enabled (`az aks approuting enable`), and a network policy engine for the NetworkPolicy demo.
```sh
make aks-push ACR=<acr>
make aks-deploy-kustomize ACR=<acr>      # or aks-deploy-helm
kubectl get ingress kubeplay -n kubeplay
```

## Troubleshooting
| Symptom | Likely cause / fix |
|---|---|
| `CreateContainerConfigError: image has non-numeric user` | Dockerfile `USER` must be numeric (distroless `65532`) |
| `ErrImageNeverPull` / `ImagePullBackOff` on kind | Images not loaded: `make kind-load`; tag must match overlay (`dev`) |
| notes-api stuck `Init:0/1` | Redis not ready or `redis-auth` secret mismatch: `kubectl logs <pod> -c wait-for-redis` |
| HPA `cpu: <unknown>` | metrics-server missing/starting: `make metrics-server`, wait ~60s |
| UI not on :8088 | Cluster not created with `deploy/kind/kind-config.yaml`; use port-forward instead |
| NetworkPolicy not enforced | CNI doesn't support it (needs kind ≥ v0.24 or Azure/Cilium policy on AKS) |
| PVC `Pending` on AKS | Wrong storage class; overlay uses `managed-csi` |

Use `kubectl describe pod` (Events) and `kubectl logs --previous` before changing manifests.

# KubePlay demo scenarios

Prereqs: cluster running and app deployed (`make kind-up kind-load deploy-kustomize` or `deploy-helm`).
Open the UI at http://localhost:8088 and keep a watch running in a second terminal:

```sh
kubectl get pods,endpointslices,hpa -n kubeplay -w
```

Shortcut used below (calls the inspector through the gateway):

```sh
export KP=http://localhost:8088/api/inspector
```

## 1. Readiness vs liveness probes
```sh
curl -X POST "$KP/chaos/ready?state=false"   # one pod goes 0/1 READY, removed from endpoints, NOT restarted
curl -X POST "$KP/chaos/live?state=false"    # after ~15s kubelet restarts the container (RESTARTS +1)
```
In the UI the affected pod tile turns grey ("not answering") while the others keep serving.
An unready pod no longer receives Service traffic, so recover it with `kubectl delete pod <name> -n kubeplay`.
Startup probe: `kubectl rollout restart deploy/inspector-api -n kubeplay` — new pods stay unready for `STARTUP_DELAY_SECONDS`.

## 2. CrashLoopBackOff
```sh
for i in 1 2 3 4; do curl -s -X POST "$KP/chaos/crash"; sleep 12; done
kubectl get pods -n kubeplay -l app.kubernetes.io/name=inspector-api
```

## 3. Horizontal Pod Autoscaler
```sh
for i in 1 2 3 4 5 6; do curl -s -X POST "$KP/stress/cpu?seconds=120&workers=2"; done
kubectl get hpa inspector-api -n kubeplay -w
```
New pod tiles appear in the UI; they scale back in ~1 min after the load stops.

## 4. Resource limits and OOMKilled
```sh
curl -X POST "$KP/stress/memory?mb=300"      # limit is 256Mi
kubectl get pod -n kubeplay -l app.kubernetes.io/name=inspector-api \
  -o custom-columns=NAME:.metadata.name,LAST:.status.containerStatuses[0].lastState.terminated.reason
```

## 5. Rolling update, rollback and blue/green-style colours
```sh
kubectl set env deploy/inspector-api -n kubeplay APP_VERSION=v2 APP_COLOR="#f97316"
kubectl rollout status deploy/inspector-api -n kubeplay
kubectl rollout undo deploy/inspector-api -n kubeplay
```
Watch tiles change colour one by one (`maxSurge: 1`, `maxUnavailable: 0`, PDB keeps 2 available).

## 6. ConfigMaps and Secrets
```sh
curl -s "$KP/config" | jq
kubectl patch cm inspector-config -n kubeplay --type merge -p '{"data":{"app-message":"Changed live!"}}'
# ~60s later: files.app-message changes, envAppMessage does NOT (env needs a restart)
kubectl rollout restart deploy/inspector-api -n kubeplay
curl -s "$KP/secret" | jq                      # mounted Secret, values masked
curl -s "$KP/whoami" | jq                      # Downward API: pod, node, IP, limits
curl -s "$KP/env" | jq '.env | with_entries(select(.key|endswith("_SERVICE_HOST")))'
```

## 7. StatefulSet + PersistentVolumeClaim
Add notes in the UI, then:
```sh
kubectl delete pod redis-0 -n kubeplay
kubectl get pvc -n kubeplay                    # data-redis-0 survives
```
While Redis is down, notes-api pods go NotReady (dependency-aware readiness) but are not restarted. Notes come back once `redis-0` is rescheduled with the same volume.

## 8. Jobs and CronJobs
```sh
kubectl apply -f demos/manifests/seed-job.yaml && kubectl logs -n kubeplay job/seed-notes -f
kubectl create job --from=cronjob/worker-report manual-report -n kubeplay
kubectl get cronjob,jobs -n kubeplay
```
Enqueue "slow" jobs in the UI and scale the consumer: `kubectl scale deploy/worker -n kubeplay --replicas=3`.
On scale-down the worker finishes its current job before exiting (SIGTERM + `terminationGracePeriodSeconds`).

## 9. NetworkPolicy
Requires a CNI that enforces policies (kind ≥ v0.24 kindnet, AKS with Azure/Cilium network policy).
```sh
kubectl apply -f demos/manifests/rogue-pod.yaml
kubectl exec -n kubeplay rogue -- redis-cli -h redis -t 3 ping      # times out: blocked
kubectl exec -n kubeplay deploy/worker -- python -c "import socket; socket.create_connection(('redis', 6379), 3); print('allowed')"
kubectl delete -f demos/manifests/rogue-pod.yaml
```

## 10. Graceful shutdown and node drain
```sh
kubectl logs -n kubeplay -l app.kubernetes.io/name=inspector-api -f &
kubectl drain kubeplay-worker --ignore-daemonsets --delete-emptydir-data
kubectl uncordon kubeplay-worker
```
Logs show `SIGTERM received, draining`; the PDB keeps at least 2 inspector pods available during the drain.

## 11. httpbin-style endpoints
```sh
curl -i "$KP/status/418"
curl "$KP/delay/2"
curl -X POST "$KP/anything/orders?id=7" -H 'Content-Type: application/json' -d '{"qty":3}'
curl "$KP/headers"; curl "$KP/ip"; curl "$KP/metrics"
open http://localhost:8088/api/notes/docs     # FastAPI Swagger UI
```

## 12. Distributed tracing with Application Insights
Requires tracing to be deployed (`make aks-deploy-kustomize ACR=<acr> APPINSIGHTS_CONNECTION_STRING='...'`, see the README).
```sh
kubectl get deploy,svc otel-collector -n kubeplay
curl -s "$KP/headers" | grep -i traceparent       # the gateway injected a W3C traceparent
curl -si "$KP/status/500" | grep -i x-trace-id    # a failed request to find in Trace Analytics
kubectl logs deploy/otel-collector -n kubeplay --tail=5   # "Traces ... spans: N"
```
In the dashboard, add a note and enqueue a *report* job: the activity log and the job result show the same trace ID.
In the Application Insights resource open *Monitoring → Traces* and search for that ID. One trace spans
`ui-gateway` → `notes-api` → Redis (`RPUSH`) → `worker` (`process report`), with each span's `k8s.pod.name` showing which pod did the work.

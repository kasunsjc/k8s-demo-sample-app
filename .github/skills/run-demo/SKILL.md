---
name: run-demo
description: "Run a live KubePlay demo scenario against a deployed cluster and narrate what Kubernetes does. Use when: presenting or rehearsing a demo of probes, CrashLoopBackOff, HPA autoscaling, OOMKilled, rolling update/rollback, ConfigMap hot reload, Secrets, StatefulSet/PVC persistence, Jobs/CronJobs, NetworkPolicy, PodDisruptionBudget or node drain."
argument-hint: "Scenario name or number from demos/README.md"
---

# Run a KubePlay demo scenario

1. Confirm the target context (`kubectl config current-context`) and that all pods in `kubeplay` are Ready.
2. Open [demos/README.md](../../../demos/README.md) and pick the scenario. Set `export KP=http://localhost:8088/api/inspector` (or the AKS ingress address).
3. Before triggering, run a watch so the audience sees the change:
   `kubectl get pods,endpointslices,hpa -n kubeplay -w`
4. Run the scenario commands one at a time. After each, explain:
   - **What we did** (the API call or kubectl command)
   - **What Kubernetes observed** (probe failure, metric, signal, OOM, schedule)
   - **What it did about it** (remove endpoint, restart, scale, reschedule, block)
5. Point out the matching tile/log entry in the UI at http://localhost:8088.
6. Reset state afterwards so the next scenario starts clean:
   - Unready/unhealthy pods: `kubectl delete pod <name> -n kubeplay`
   - Memory hogs: `curl -X DELETE "$KP/stress/memory"` (or restart the pod)
   - Rollouts: `kubectl rollout undo deploy/inspector-api -n kubeplay`
   - ConfigMap edits: `kubectl apply -k deploy/kustomize/overlays/local`
   - Extra objects: `kubectl delete -f demos/manifests/<file>.yaml --ignore-not-found`

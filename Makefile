CLUSTER   ?= kubeplay
NS        ?= kubeplay
TAG       ?= dev
REGISTRY  ?=
IMAGE_TAG ?= 0.1.1
STORAGE_CLASS ?=
INGRESS_CLASS ?=
DOCKERHUB_USER ?=
DOCKERHUB_TAG  ?= 0.1.1
# Registry images target amd64 (most managed clusters); override with PLATFORMS=linux/amd64,linux/arm64 if needed.
PLATFORMS      ?= linux/amd64
BUILDER        ?= kubeplay-builder
# kind nodes run on the Docker engine's native arch (arm64 on Apple Silicon), so local images must match it.
LOCAL_PLATFORM := linux/$(shell docker version -f '{{.Server.Arch}}' 2>/dev/null || echo amd64)
SERVICES  := inspector-api notes-api worker ui-gateway
CHART     := deploy/helm/kubeplay
# Application Insights connection string; when set, remote deploys add the OpenTelemetry Collector (tracing).
APPINSIGHTS_CONNECTION_STRING ?=
REMOTE_OVERLAY := $(if $(APPINSIGHTS_CONNECTION_STRING),remote-tracing,remote)
METRICS_SERVER_URL := https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml

.PHONY: help test build compose-up compose-tracing compose-down kind-up kind-down kind-destroy kind-load metrics-server \
        deploy-kustomize deploy-helm undeploy-kustomize undeploy-helm lint \
        push deploy-remote-kustomize deploy-remote-helm dockerhub-push buildx-clean appinsights-secret

help: ## Show targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-22s %s\n", $$1, $$2}'

test: ## Run unit tests for all services
	cd services/inspector-api && go test ./...
	cd services/notes-api && python -m pytest -q
	cd services/worker && python -m pytest -q
	cd services/ui-gateway && node --test

build: ## Build all images as kubeplay/<svc>:$(TAG) for the local Docker/kind architecture
	@for s in $(SERVICES); do docker build --platform $(LOCAL_PLATFORM) -t kubeplay/$$s:$(TAG) services/$$s || exit 1; done

compose-up: ## Run everything with docker compose (UI on :3000)
	docker compose up --build -d

compose-tracing: ## Docker compose + OpenTelemetry Collector sending traces to Application Insights (APPINSIGHTS_CONNECTION_STRING=...)
	@test -n "$$APPINSIGHTS_CONNECTION_STRING" || (echo "Set APPINSIGHTS_CONNECTION_STRING to the Application Insights connection string" && exit 1)
	APPLICATIONINSIGHTS_CONNECTION_STRING="$$APPINSIGHTS_CONNECTION_STRING" OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4318 \
	  docker compose --profile tracing up --build -d

compose-down: ## Stop docker compose and remove volumes
	docker compose --profile tracing down -v

# Docker Hub has no nested repos, so images are pushed as <user>/kubeplay-<svc>.
dockerhub-push: ## Build linux/amd64 images and push to Docker Hub (DOCKERHUB_USER=<user> [DOCKERHUB_TAG=0.1.1])
	@test -n "$(DOCKERHUB_USER)" || (echo "Set DOCKERHUB_USER=<username>, e.g. make dockerhub-push DOCKERHUB_USER=jdoe" && exit 1)
	@docker info 2>/dev/null | grep -q '^ *Username:' || echo "Note: run 'docker login' first if the push is denied."
	@# A temporary docker-container builder handles cross-arch builds; it is removed afterwards even on failure.
	@docker buildx inspect $(BUILDER) >/dev/null 2>&1 || docker buildx create --name $(BUILDER) --driver docker-container --bootstrap >/dev/null; \
	rc=0; \
	for s in $(SERVICES); do \
	  docker buildx build --builder $(BUILDER) --platform $(PLATFORMS) --push \
	    -t $(DOCKERHUB_USER)/kubeplay-$$s:$(DOCKERHUB_TAG) \
	    -t $(DOCKERHUB_USER)/kubeplay-$$s:latest \
	    services/$$s || { rc=1; break; }; \
	done; \
	docker buildx rm $(BUILDER) >/dev/null 2>&1 && echo "Removed buildx builder $(BUILDER)"; \
	exit $$rc

buildx-clean: ## Remove the temporary buildx builder if a push was interrupted
	-@docker buildx rm $(BUILDER) 2>/dev/null && echo "Removed buildx builder $(BUILDER)" || echo "No $(BUILDER) builder to remove"

kind-up: ## Create kind cluster (2 workers) + metrics-server
	kind create cluster --config deploy/kind/kind-config.yaml --name $(CLUSTER)
	$(MAKE) metrics-server

kind-down: ## Delete the kind cluster
	kind delete cluster --name $(CLUSTER)

kind-destroy: kind-down ## Delete the kind cluster and remove local kubeplay images
	@ids=$$(docker images -q --filter=reference='kubeplay/*:$(TAG)' | sort -u); \
	if [ -n "$$ids" ]; then docker image rm -f $$ids; else echo "No kubeplay/*:$(TAG) images to remove"; fi

metrics-server: ## Install metrics-server (needed by HPA); insecure TLS is for local clusters only
	kubectl apply -f $(METRICS_SERVER_URL)
	kubectl patch deployment metrics-server -n kube-system --type=json \
	  -p='[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]'

kind-load: build ## Build and load images into kind (checks image arch matches the nodes)
	@node_arch=$$(kubectl get nodes --context kind-$(CLUSTER) -o jsonpath='{.items[0].status.nodeInfo.architecture}' 2>/dev/null); \
	for s in $(SERVICES); do \
	  img_arch=$$(docker image inspect -f '{{.Architecture}}' kubeplay/$$s:$(TAG)); \
	  if [ -n "$$node_arch" ] && [ "$$img_arch" != "$$node_arch" ]; then \
	    echo "ERROR: kubeplay/$$s:$(TAG) is $$img_arch but kind nodes are $$node_arch"; exit 1; fi; \
	  kind load docker-image kubeplay/$$s:$(TAG) --name $(CLUSTER) || exit 1; \
	done; \
	echo "Loaded $(SERVICES) ($${node_arch:-unknown arch}) into kind"

deploy-kustomize: ## Deploy local overlay (UI: http://localhost:8088)
	kubectl apply -k deploy/kustomize/overlays/local
	kubectl rollout status deploy/inspector-api -n $(NS) --timeout=180s

deploy-helm: ## Deploy with Helm using values-local.yaml
	helm upgrade --install kubeplay $(CHART) -n $(NS) --create-namespace \
	  -f $(CHART)/values-local.yaml --set global.imageTag=$(TAG)

undeploy-kustomize: ## Delete the local Kustomize deployment
	kubectl delete -k deploy/kustomize/overlays/local --ignore-not-found

undeploy-helm: ## Uninstall the Helm release
	helm uninstall kubeplay -n $(NS)

lint: ## Lint Helm chart and render Kustomize overlays
	helm lint $(CHART) -f $(CHART)/values-local.yaml
	helm lint $(CHART) -f $(CHART)/values-remote.yaml
	kubectl kustomize deploy/kustomize/overlays/local > /dev/null
	kubectl kustomize deploy/kustomize/overlays/remote > /dev/null
	kubectl kustomize deploy/kustomize/overlays/remote-tracing > /dev/null
	helm template kubeplay $(CHART) -f $(CHART)/values-remote.yaml --set tracing.enabled=true > /dev/null

check-registry:
	@test -n "$(REGISTRY)" || (echo "Set REGISTRY=<registry/repo prefix>, e.g. make push REGISTRY=registry.example.com/myorg" && exit 1)

push: check-registry ## Build and push images to $(REGISTRY)/kubeplay/<service>:$(IMAGE_TAG) (run docker login first)
	@for s in $(SERVICES); do docker buildx build --platform $(PLATFORMS) --push -t $(REGISTRY)/kubeplay/$$s:$(IMAGE_TAG) services/$$s || exit 1; done

appinsights-secret: ## Create/update the appinsights Secret for the OpenTelemetry Collector (APPINSIGHTS_CONNECTION_STRING=...)
	@test -n "$$APPINSIGHTS_CONNECTION_STRING" || (echo "Set APPINSIGHTS_CONNECTION_STRING to the Application Insights connection string" && exit 1)
	@kubectl create namespace $(NS) --dry-run=client -o yaml | kubectl apply -f - >/dev/null
	@printf '%s' "$$APPINSIGHTS_CONNECTION_STRING" | kubectl create secret generic appinsights -n $(NS) --from-file=connection-string=/dev/stdin \
	  --dry-run=client -o yaml | kubectl apply -f -

deploy-remote-kustomize: check-registry ## Deploy remote overlay with images from $(REGISTRY) (+ tracing if APPINSIGHTS_CONNECTION_STRING is set)
	$(if $(APPINSIGHTS_CONNECTION_STRING),@$(MAKE) --no-print-directory appinsights-secret)
	kubectl kustomize deploy/kustomize/overlays/$(REMOTE_OVERLAY) | sed 's#REGISTRY_HOST#$(REGISTRY)#g; s#\(kubeplay/[a-z-]*\):0\.1\.1$$#\1:$(IMAGE_TAG)#' | kubectl apply -f -
	$(if $(APPINSIGHTS_CONNECTION_STRING),kubectl rollout restart deployment/otel-collector -n $(NS))

deploy-remote-helm: check-registry ## Deploy to any cluster with Helm (+ tracing if APPINSIGHTS_CONNECTION_STRING is set; optional STORAGE_CLASS, INGRESS_CLASS)
	$(if $(APPINSIGHTS_CONNECTION_STRING),@$(MAKE) --no-print-directory appinsights-secret)
	helm upgrade --install kubeplay $(CHART) -n $(NS) --create-namespace \
	  -f $(CHART)/values-remote.yaml --set global.imageRegistry=$(REGISTRY) --set global.imageTag=$(IMAGE_TAG) \
	  $(if $(STORAGE_CLASS),--set redis.storage.storageClass=$(STORAGE_CLASS)) \
	  $(if $(INGRESS_CLASS),--set ingress.enabled=true --set ingress.className=$(INGRESS_CLASS)) \
	  $(if $(APPINSIGHTS_CONNECTION_STRING),--set tracing.enabled=true)
	$(if $(APPINSIGHTS_CONNECTION_STRING),kubectl rollout restart deployment/otel-collector -n $(NS))

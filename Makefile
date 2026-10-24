CLUSTER   ?= kubeplay
NS        ?= kubeplay
TAG       ?= dev
ACR       ?=
AKS_TAG   ?= 0.1.0
SERVICES  := inspector-api notes-api worker ui-gateway
CHART     := deploy/helm/kubeplay
METRICS_SERVER_URL := https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml

.PHONY: help test build compose-up compose-down kind-up kind-down kind-load metrics-server \
        deploy-kustomize deploy-helm undeploy-kustomize undeploy-helm lint \
        aks-push aks-deploy-kustomize aks-deploy-helm

help: ## Show targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-22s %s\n", $$1, $$2}'

test: ## Run unit tests for all services
	cd services/inspector-api && go test ./...
	cd services/notes-api && python -m pytest -q
	cd services/worker && python -m pytest -q
	cd services/ui-gateway && node --test

build: ## Build all images as kubeplay/<svc>:$(TAG)
	@for s in $(SERVICES); do docker build -t kubeplay/$$s:$(TAG) services/$$s || exit 1; done

compose-up: ## Run everything with docker compose (UI on :3000)
	docker compose up --build -d

compose-down:
	docker compose down -v

kind-up: ## Create kind cluster (2 workers) + metrics-server
	kind create cluster --config deploy/kind/kind-config.yaml --name $(CLUSTER)
	$(MAKE) metrics-server

kind-down:
	kind delete cluster --name $(CLUSTER)

metrics-server: ## Install metrics-server (needed by HPA); insecure TLS is for local clusters only
	kubectl apply -f $(METRICS_SERVER_URL)
	kubectl patch deployment metrics-server -n kube-system --type=json \
	  -p='[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]'

kind-load: build ## Build and load images into kind
	@for s in $(SERVICES); do kind load docker-image kubeplay/$$s:$(TAG) --name $(CLUSTER) || exit 1; done

deploy-kustomize: ## Deploy local overlay (UI: http://localhost:8088)
	kubectl apply -k deploy/kustomize/overlays/local
	kubectl rollout status deploy/inspector-api -n $(NS) --timeout=180s

deploy-helm: ## Deploy with Helm using values-local.yaml
	helm upgrade --install kubeplay $(CHART) -n $(NS) --create-namespace \
	  -f $(CHART)/values-local.yaml --set global.imageTag=$(TAG)

undeploy-kustomize:
	kubectl delete -k deploy/kustomize/overlays/local --ignore-not-found

undeploy-helm:
	helm uninstall kubeplay -n $(NS)

lint: ## Lint Helm chart and render Kustomize overlays
	helm lint $(CHART) -f $(CHART)/values-local.yaml
	helm lint $(CHART) -f $(CHART)/values-aks.yaml
	kubectl kustomize deploy/kustomize/overlays/local > /dev/null
	kubectl kustomize deploy/kustomize/overlays/aks > /dev/null

check-acr:
	@test -n "$(ACR)" || (echo "Set ACR=<registry name>, e.g. make aks-push ACR=myacr" && exit 1)

aks-push: check-acr ## Build in ACR (no local Docker needed)
	@for s in $(SERVICES); do az acr build -r $(ACR) -t kubeplay/$$s:$(AKS_TAG) services/$$s || exit 1; done

aks-deploy-kustomize: check-acr ## Deploy AKS overlay with images from $(ACR)
	kubectl kustomize deploy/kustomize/overlays/aks | sed 's#ACR_NAME.azurecr.io#$(ACR).azurecr.io#g' | kubectl apply -f -

aks-deploy-helm: check-acr ## Deploy to AKS with Helm
	helm upgrade --install kubeplay $(CHART) -n $(NS) --create-namespace \
	  -f $(CHART)/values-aks.yaml --set global.imageRegistry=$(ACR).azurecr.io --set global.imageTag=$(AKS_TAG)

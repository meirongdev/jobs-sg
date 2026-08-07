GO ?= go
BIN_DIR := bin
CMD := ingest enrich report web

.PHONY: all build test vet fmt tidy clean fixture \
	kind-up kind-build kind-load kind-apply kind-seed kind-wait kind-smoke kind-e2e kind-down
all: build

build:
	@mkdir -p $(BIN_DIR)
	@for c in $(CMD); do CGO_ENABLED=0 $(GO) build -o $(BIN_DIR)/jobs-sg-$$c ./cmd/$$c; done

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

tidy:
	$(GO) mod tidy

clean:
	rm -rf $(BIN_DIR) data report

# --- Local kind integration testing (single-node, no external services) ---
# Requires: docker, kind, kubectl, kustomize. Manifests live in
# test/integration/kind/ (a local overlay over deploy/ that drops homelab-only
# resources, e.g. Cilium gateway, monitoring, Vault ExternalSecret).
KIND_CLUSTER ?= jobs-sg
KIND_CTX    ?= kind-$(KIND_CLUSTER)
KIND_IMG    ?= jobs-sg:local
KIND_OVERLAY:= test/integration/kind

## kind-up: create the (single-node) cluster if absent + install local-path SC
kind-up:
	@kind get clusters 2>/dev/null | grep -qx '$(KIND_CLUSTER)' \
		|| kind create cluster --name $(KIND_CLUSTER)
	kubectl --context $(KIND_CTX) apply -f \
		https://raw.githubusercontent.com/rancher/local-path-provisioner/v0.0.31/deploy/local-path-storage.yaml

## kind-build: build the local integration image
kind-build:
	docker build -t $(KIND_IMG) .

## kind-load: load the image into the kind cluster
kind-load:
	kind load docker-image $(KIND_IMG) --name $(KIND_CLUSTER)

## kind-apply: apply the integration overlay (web, cronjobs, pvc, rbac, quota)
kind-apply:
	kubectl --context $(KIND_CTX) kustomize $(KIND_OVERLAY) \
		| kubectl --context $(KIND_CTX) apply -f -

## kind-seed: bootstrap the PVC-backed DB so the read-only web pod can open it
kind-seed:
	kubectl --context $(KIND_CTX) apply -f $(KIND_OVERLAY)/init-db.yaml

## kind-wait: wait for the seed Job, then the web Deployment rollout
kind-wait:
	kubectl --context $(KIND_CTX) wait --for=condition=complete \
		job/init-db -n jobs-sg --timeout=180s
	kubectl --context $(KIND_CTX) rollout status \
		deployment/jobs-sg-web -n jobs-sg --timeout=120s

## kind-smoke: health + metrics check over a temporary port-forward
## /metrics is bound to its own listener (9090) so the public route cannot
## reach it, so the smoke test checks both ports and asserts the public one
## does NOT serve metrics.
kind-smoke:
	@set -e; \
	kubectl --context $(KIND_CTX) port-forward -n jobs-sg \
		svc/jobs-sg-web 18080:80 18090:9090 & PF=$$!; \
	sleep 3; \
	curl -fsS http://127.0.0.1:18080/healthz; echo; \
	curl -fsS -o /dev/null -w 'metrics (9090) HTTP %{http_code}\n' \
		http://127.0.0.1:18090/metrics; \
	code=$$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:18080/metrics); \
	echo "metrics on public port HTTP $$code (want 404)"; \
	test "$$code" = "404" || { echo "FAIL: /metrics is reachable on the public port"; kill $$PF; exit 1; }; \
	kill $$PF 2>/dev/null || true

## kind-e2e: full local end-to-end — cluster -> image -> overlay -> seed -> verify
kind-e2e: kind-up kind-build kind-load kind-apply kind-seed kind-wait kind-smoke
	@echo "Local kind e2e OK (cluster $(KIND_CLUSTER))."

## kind-down: tear down the cluster (data on the PVC is deleted with it)
kind-down:
	kind delete cluster --name $(KIND_CLUSTER)

# kubevirt-drs developer entry points. Every target works without a local Go toolchain (hack/go.sh runs
# go in a container when it is missing); helm / promtool / kubeconform fall back to containers too.
GO            ?= hack/go.sh
ENGINE        ?= $(shell command -v podman 2>/dev/null || command -v docker 2>/dev/null)
VERSION       ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
IMAGE         ?= ghcr.io/zhanwei-ai/kubevirt-drs
CHART         := charts/kubevirt-drs
CONTROLLER_GEN_VERSION ?= v0.23.0
GOLANGCI_LINT_IMAGE    ?= docker.io/golangci/golangci-lint:v2.15.0
HELM_IMAGE             ?= docker.io/alpine/helm:4.3.1
PROMETHEUS_IMAGE       ?= quay.io/prometheus/prometheus:v3.15.1
KUBECONFORM_IMAGE      ?= ghcr.io/yannh/kubeconform:v0.8.0
# API versions the chart's Capabilities checks need when rendering offline.
HELM_API_VERSIONS := --kube-version 1.36.0 --api-versions monitoring.coreos.com/v1/PrometheusRule --api-versions monitoring.coreos.com/v1/ServiceMonitor --api-versions kubevirt.io/v1/VirtualMachineInstance

.PHONY: all generate manifests build test vet fmt lint chart-lint rules-check rules-export image verify-generated help

all: generate manifests build test

## generate: deepcopy methods for the API types
generate:
	$(GO) run sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION) object:headerFile=hack/boilerplate.go.txt paths=./api/...

## manifests: CRDs + RBAC from markers, copied into the chart
manifests:
	$(GO) run sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION) crd rbac:roleName=kubevirt-drs-manager paths=./... output:crd:artifacts:config=config/crd/bases output:rbac:artifacts:config=config/rbac
	cp config/crd/bases/*.yaml $(CHART)/crds/
	hack/sync-rbac.sh

build:
	$(GO) build -trimpath -ldflags "-s -w -X github.com/zhanwei-ai/kubevirt-drs/internal/version.Version=$(VERSION)" -o bin/manager ./cmd/manager

test:
	$(GO) test ./... -coverprofile=coverage.out

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

lint:
	$(ENGINE) run --rm -v $(CURDIR):/src:Z -w /src $(GOLANGCI_LINT_IMAGE) golangci-lint run ./...

## verify-generated: fail when generated files are out of date (CI)
verify-generated: generate manifests
	git diff --exit-code -- api config $(CHART)/crds $(CHART)/templates/controller/clusterrole.yaml

HELM ?= $(shell command -v helm 2>/dev/null)
ifeq ($(HELM),)
HELM := $(ENGINE) run --rm -v $(CURDIR):/src:Z -w /src $(HELM_IMAGE)
endif

chart-lint:
	$(HELM) lint $(CHART)
	$(HELM) template drs $(CHART) $(HELM_API_VERSIONS) --set controller.enabled=true > /dev/null
	$(HELM) template drs $(CHART) $(HELM_API_VERSIONS) --set controller.enabled=true \
	  | $(ENGINE) run --rm -i $(KUBECONFORM_IMAGE) -strict -ignore-missing-schemas -summary

## rules-export: write the rendered recording rules as a plain Prometheus rule file (for non-operator users)
rules-export:
	$(HELM) template drs $(CHART) $(HELM_API_VERSIONS) --show-only templates/prometheusrule.yaml \
	  | $(ENGINE) run --rm -i -v $(CURDIR):/src:Z -w /src docker.io/mikefarah/yq:4.54.1 '.spec' > docs/examples/drs-rules.yaml

## rules-check: promtool check rules on the exported file
rules-check: rules-export
	$(ENGINE) run --rm -v $(CURDIR):/src:Z -w /src --entrypoint promtool $(PROMETHEUS_IMAGE) check rules docs/examples/drs-rules.yaml

image:
	$(ENGINE) build -f Containerfile -t $(IMAGE):$(VERSION) --build-arg VERSION=$(VERSION) .

help:
	@grep -E '^## ' Makefile | sed 's/^## //'

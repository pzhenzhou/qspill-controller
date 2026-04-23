ifeq ($(shell uname), Darwin)
SED_IN_PLACE := sed -i ''
else
SED_IN_PLACE := sed -i
endif

# qspill-controller binary name
BINARY_NAME=qspill-controller-bin

# qspill-controller Docker image name
IMG ?= qspill-controller:latest

# Number of replicas for deployment
REPLICAS ?= 2

# CONTAINER_TOOL defines the container tool to be used for building images.
CONTAINER_TOOL ?= docker

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped cmd fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

export KUBECONFIG= $(HOME)/.kube/config

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Tool Binaries
KUSTOMIZE ?= $(LOCALBIN)/kustomize

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)
	@echo ""
	@echo "Examples:"
	@echo "  make run ARGS=\"--service-port=8080 --pprof=false\""
	@echo "  make run-dev"
	@echo "  make docker-build"

##@ Build

.PHONY: all
all: build

.PHONY: fmt
fmt:
	gofmt -l -w -d  ./pkg ./cmd

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...


.PHONY: build
build: fmt
	go build -o bin/$(BINARY_NAME) ./cmd/controller

.PHONY: test
test: build
	go test $(shell go list ./... | grep -v /proto | grep -v /cmd) -v -coverprofile cover.out

##@ Docker

.PHONY: docker-build
docker-build: ## Build docker image with the manager.
	docker build -f ./docker/Dockerfile -t ${IMG} .

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	docker push ${IMG}

# PLATFORMS defines the target platforms for the manager image be build to provide support to multiple
# architectures. (i.e. make docker-buildx IMG=myregistry/myimage:0.0.1). To use this option you need to:
# - able to use docker buildx. More info: https://docs.docker.com/build/buildx/
# - have enable BuildKit. More info: https://docs.docker.com/develop/develop-images/build_enhancements/
# - be able to push the image for your registry (i.e. if you do not inform a valid value via IMG=<myregistry/image:<tag>> then the export will fail)
PLATFORMS ?= linux/arm64,linux/amd64
.PHONY: docker-buildx
docker-buildx: build ## Build and push docker image for cross-platform support
	- docker buildx create --name project-v3-builder
	docker buildx use project-v3-builder
	- docker buildx build --push --platform=$(PLATFORMS)  --tag ${IMG} -f ./docker/Dockerfile .
	- docker buildx rm project-v3-builder

##@ Development

.PHONY: run
run: build ## Run the built binary locally. Use ARGS="--flag value" to pass arguments.
	@echo "Running $(BINARY_NAME) with args: $(ARGS)"
	./bin/$(BINARY_NAME) $(ARGS)

.PHONY: run-dev
run-dev: build ## Run with development configuration (pprof enabled, metrics enabled).
	@echo "Running $(BINARY_NAME) in development mode..."
	./bin/$(BINARY_NAME) --pprof=true --metrics.enable=true

.PHONY: run-with-metrics
run-with-metrics: build ## Run with metrics enabled.
	@echo "Running $(BINARY_NAME) with metrics enabled..."
	./bin/$(BINARY_NAME) --metrics.enable=true --metrics.sink=prometheus

.PHONY: run-local
run-local: envtest build fmt vet ## Run using go run (for development with hot reload).
	go run cmd/main.go $(ARGS)

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = true
endif

.PHONY: deploy-rbac
deploy-rbac: kustomize ## Deploy RBAC resources to the K8s cluster specified in ~/.kube/config.
	@echo "Deploying RBAC resources..."
	$(KUSTOMIZE) build config/rbac | kubectl apply -f -

.PHONY: deploy
deploy: kustomize ## Deploy to the K8s cluster specified in ~/.kube/config.
	@echo "Deploying qspill-controller..."
	$(KUSTOMIZE) build config/base | kubectl apply -f -

.PHONY: undeploy
undeploy: kustomize ## Remove resources from the K8s cluster specified in ~/.kube/config.
	@echo "Removing qspill-controller..."
	$(KUSTOMIZE) build config/base | kubectl delete --ignore-not-found=$(ignore-not-found) -f -

.PHONY: undeploy-rbac
undeploy-rbac: kustomize ## Remove RBAC resources from the K8s cluster.
	@echo "Removing RBAC resources..."
	$(KUSTOMIZE) build config/rbac | kubectl delete --ignore-not-found=$(ignore-not-found) -f -

##@ Tools

## Tool Versions
KUSTOMIZE_VERSION ?= v4.5.7

KUSTOMIZE_INSTALL_SCRIPT ?= "https://raw.githubusercontent.com/kubernetes-sigs/kustomize/master/hack/install_kustomize.sh"
.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary. If wrong version is installed, it will be removed before downloading.
$(KUSTOMIZE): $(LOCALBIN)
	@if test -x $(LOCALBIN)/kustomize && ! $(LOCALBIN)/kustomize version | grep -q $(KUSTOMIZE_VERSION); then \
		echo "$(LOCALBIN)/kustomize version is not expected $(KUSTOMIZE_VERSION). Removing it before installing."; \
		rm -rf $(LOCALBIN)/kustomize; \
	fi
	test -s $(LOCALBIN)/kustomize || { curl -Ss $(KUSTOMIZE_INSTALL_SCRIPT) | bash -s -- $(subst v,,$(KUSTOMIZE_VERSION)) $(LOCALBIN); }

.PHONY: envtest
envtest: $(ENVTEST) ## Download envtest-setup locally if necessary.
ENVTEST ?= $(LOCALBIN)/setup-envtest
$(ENVTEST): $(LOCALBIN)
	test -s $(LOCALBIN)/setup-envtest || GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest

IMG ?= ghcr.io/pzhenzhou/qspill-controller:latest
ENVTEST_K8S_VERSION = 1.29.0

ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

.PHONY: help
help: ## Show this help
	@echo 'Usage: make <target>'
	@grep -E '^[a-zA-Z_0-9-]+:.*?##' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*##"}; {printf "  %-15s %s\n", $$1, $$2}'

.PHONY: fmt
fmt: ## Run go fmt
	go fmt ./...

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: test
test: fmt vet ## Run tests
	go test ./... -coverprofile cover.out

.PHONY: build
build: fmt vet ## Build manager binary
	go build -o bin/manager cmd/manager/main.go

.PHONY: run
run: fmt vet ## Run the controller locally
	go run ./cmd/manager/main.go

.PHONY: docker-build
docker-build: ## Build docker image
	docker build -t ${IMG} .

.PHONY: docker-push
docker-push: ## Push docker image
	docker push ${IMG}

.PHONY: install
install: ## Install CRDs
	kubectl apply -f config/crd/

.PHONY: uninstall
uninstall: ## Uninstall CRDs
	kubectl delete --ignore-not-found=$(ignore-not-found) -f config/crd/

.PHONY: deploy
deploy: ## Deploy controller
	kubectl apply -f config/rbac/
	kubectl apply -f config/manager/

.PHONY: undeploy
undeploy: ## Undeploy controller
	kubectl delete --ignore-not-found=$(ignore-not-found) -f config/manager/
	kubectl delete --ignore-not-found=$(ignore-not-found) -f config/rbac/

# Makefile
CONTROLLER_GEN = $(shell pwd)/bin/controller-gen
.PHONY: generate
generate: controller-gen
    $(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./api/v1/..."

.PHONY: controller-gen
controller-gen:
    GOBIN=$(shell pwd)/bin go install sigs.k8s.io/controller-tools/cmd/controller-gen@v0.13.0
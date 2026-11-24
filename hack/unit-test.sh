#!/usr/bin/env bash
set -euo pipefail

assets=$(go run sigs.k8s.io/controller-runtime/tools/setup-envtest@32759fab31191897ae86091d8831b2d4b890aedd use 1.31.0 --bin-dir "${RUNNER_TEMP:-/tmp}/envtest" -p path)
test -x "$assets/kube-apiserver"
export KUBEBUILDER_ASSETS=$assets
go test $(go list ./... | grep -v /e2e) -timeout 10m

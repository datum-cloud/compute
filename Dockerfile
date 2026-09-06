# Build the manager and compute-mcp binaries
#
# ONE image carries both. They are released together and share the condition
# vocabulary: internal/agent's catalog classifies every reason api/v1alpha
# declares, and the manager's controllers are what write those reasons. Two
# separately tagged images could drift into a skew nothing prevents — an
# assistant answering "no catalog entry for this reason" on exactly the
# failures it exists to explain. Each Deployment picks its binary with
# `command`; see config/base/manager and config/components/compute-mcp.
FROM --platform=$BUILDPLATFORM golang:1.26 AS builder
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG GIT_COMMIT=unknown
ARG GIT_TREE_STATE=unknown
ARG BUILD_DATE=unknown

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer

RUN go mod download

# Copy the go source. docs/agent is a build input, not documentation: it is a
# package whose embed.FS carries the knowledge and runbooks compute-mcp serves.
COPY cmd/main.go cmd/main.go
COPY cmd/compute-mcp/ cmd/compute-mcp/
COPY api/ api/
COPY internal/ internal/
COPY docs/agent/ docs/agent/

# Build
# the GOARCH has not a default value to allow the binary be built according to the host where the command
# was called. For example, if we call make docker-build in a local env which has the Apple Silicon M1 SO
# the docker BUILDPLATFORM arg will be linux/arm64 when for Apple x86 it will be linux/amd64. Therefore,
# by leaving it empty we can ensure that the container and binary shipped on it will have the same platform.
#
# Both binaries build in one stage so the dependency tree they share — 816 of
# compute-mcp's 845 packages are already in the manager's — compiles once.
ENV GOCACHE=/root/.cache/go-build
ENV GOTMPDIR=/root/.cache/go-build
RUN --mount=type=cache,target=/go/pkg/mod/ \
  --mount=type=cache,target="/root/.cache/go-build" \
  CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build \
    -ldflags "-s -w \
      -X main.version=${VERSION} \
      -X main.gitCommit=${GIT_COMMIT} \
      -X main.gitTreeState=${GIT_TREE_STATE} \
      -X main.buildDate=${BUILD_DATE}" \
    -o manager cmd/main.go

# compute-mcp carries its version as an MCP protocol constant, not an ldflags
# variable, so the build metadata above is not stamped into it.
RUN --mount=type=cache,target=/go/pkg/mod/ \
  --mount=type=cache,target="/root/.cache/go-build" \
  CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build \
    -ldflags "-s -w" \
    -o compute-mcp ./cmd/compute-mcp

# Use distroless as minimal base image to package the binaries
# Refer to https://github.com/GoogleContainerTools/distroless for more details
#
# The nonroot variant has no shell and no package manager, which matters most
# for compute-mcp: it is the process an untrusted model's tool calls reach. The
# manager binary now sits beside it there, and grants it nothing — it is a
# controller, not an execution primitive, and the compute-mcp pod holds no
# credential it could use (an unbound ServiceAccount, a credential-free
# kubeconfig). See config/components/compute-mcp/service_account.yaml.
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/manager .
COPY --from=builder /workspace/compute-mcp .
USER 65532:65532

# The manager keeps the entrypoint it has always had, so nothing that runs this
# image bare changes behaviour. compute-mcp's Deployment overrides it with
# `command`, the same way the manager's own Deployment already states /manager.
ENTRYPOINT ["/manager"]

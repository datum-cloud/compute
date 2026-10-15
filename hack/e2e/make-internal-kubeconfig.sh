#!/usr/bin/env bash
# make-internal-kubeconfig.sh <input-kubeconfig> <output-kubeconfig> <kind-cluster-name>
#
# Produces a kubeconfig variant that uses the Kind node's Docker container IP
# instead of localhost. This variant is stored in Karmada so the controller
# manager (running inside Docker) can reach member cluster API servers across
# the kind bridge network.
#
# Background: Kind maps each cluster's API server to a random localhost port
# on the developer machine. Inside Docker containers, "localhost" refers to the
# container's own loopback — not the host. We therefore swap the server address
# to the Kind control-plane container's Docker bridge IP (e.g. 172.18.0.x) and
# set insecure-skip-tls-verify because the node certificate does not include
# the Docker bridge IP in its SANs.
#
# Usage:
#   hack/e2e/make-internal-kubeconfig.sh \
#     tmp/e2e/kubeconfigs/pop-dfw.yaml \
#     tmp/e2e/kubeconfigs/pop-dfw-internal.yaml \
#     compute-pop-dfw

set -euo pipefail

INPUT="${1:?usage: $0 <input-kubeconfig> <output-kubeconfig> <kind-cluster-name>}"
OUTPUT="${2:?usage: $0 <input-kubeconfig> <output-kubeconfig> <kind-cluster-name>}"
CLUSTER_NAME="${3:?usage: $0 <input-kubeconfig> <output-kubeconfig> <kind-cluster-name>}"

CONTAINER_NAME="${CLUSTER_NAME}-control-plane"

# Resolve the container's Docker bridge IP.
DOCKER_IP=$(docker inspect \
  -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' \
  "${CONTAINER_NAME}" 2>/dev/null || true)

if [ -z "${DOCKER_IP}" ]; then
  echo "ERROR: Could not resolve Docker IP for container '${CONTAINER_NAME}'." >&2
  echo "       Is the Kind cluster '${CLUSTER_NAME}' running?" >&2
  exit 1
fi

echo "  ${CLUSTER_NAME}: Docker IP ${DOCKER_IP} → ${OUTPUT}"

python3 - "${INPUT}" "${OUTPUT}" "${DOCKER_IP}" <<'PYEOF'
import sys, yaml

src, dst, docker_ip = sys.argv[1], sys.argv[2], sys.argv[3]

with open(src) as f:
    cfg = yaml.safe_load(f)

for cluster in cfg.get('clusters', []):
    # Kind API server always listens on port 6443 inside the container.
    cluster['cluster']['server'] = f'https://{docker_ip}:6443'
    # The node cert only covers localhost / 127.0.0.1, not the bridge IP.
    cluster['cluster']['insecure-skip-tls-verify'] = True
    cluster['cluster'].pop('certificate-authority-data', None)

with open(dst, 'w') as f:
    yaml.dump(cfg, f, default_flow_style=False)
PYEOF

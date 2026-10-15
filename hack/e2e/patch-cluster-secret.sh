#!/usr/bin/env bash
# patch-cluster-secret.sh <karmada-kubeconfig> <cluster-name> <internal-kubeconfig>
#
# After "karmadactl join", Karmada stores the member cluster's kubeconfig in a
# Secret referenced by the Cluster object's spec.secretRef, and sets
# spec.apiEndpoint to the localhost address it resolved from the external
# kubeconfig. The Karmada controller manager runs inside Docker and cannot use
# localhost to reach POP cell API servers.
#
# This script:
#   1. Replaces the kubeconfig in the Secret with the Docker-IP variant so that
#      the Karmada controller can make API calls to the member cluster.
#   2. Patches spec.apiEndpoint on the Cluster object so that health checks also
#      use the Docker bridge IP instead of localhost.
#
# Usage:
#   hack/e2e/patch-cluster-secret.sh \
#     tmp/e2e/kubeconfigs/karmada.yaml \
#     compute-pop-dfw \
#     tmp/e2e/kubeconfigs/pop-dfw-internal.yaml

set -euo pipefail

KARMADA_KUBECONFIG="${1:?usage: $0 <karmada-kubeconfig> <cluster-name> <internal-kubeconfig>}"
CLUSTER_NAME="${2:?usage: $0 <karmada-kubeconfig> <cluster-name> <internal-kubeconfig>}"
INTERNAL_KUBECONFIG="${3:?usage: $0 <karmada-kubeconfig> <cluster-name> <internal-kubeconfig>}"

# ------------------------------------------------------------------
# Read the Cluster object's secretRef (name + namespace)
# ------------------------------------------------------------------
SECRET_NAME=$(kubectl \
  --kubeconfig="${KARMADA_KUBECONFIG}" \
  get cluster "${CLUSTER_NAME}" \
  -o jsonpath='{.spec.secretRef.name}' 2>/dev/null || true)

if [ -z "${SECRET_NAME}" ]; then
  echo "ERROR: Could not find spec.secretRef.name on cluster '${CLUSTER_NAME}'." >&2
  echo "       Has karmadactl join completed successfully?" >&2
  exit 1
fi

SECRET_NAMESPACE=$(kubectl \
  --kubeconfig="${KARMADA_KUBECONFIG}" \
  get cluster "${CLUSTER_NAME}" \
  -o jsonpath='{.spec.secretRef.namespace}' 2>/dev/null || true)

SECRET_NAMESPACE="${SECRET_NAMESPACE:-karmada-system}"

echo "  Patching secret ${SECRET_NAMESPACE}/${SECRET_NAME} with Docker-IP kubeconfig..."

# ------------------------------------------------------------------
# Replace the kubeconfig data in the secret
# ------------------------------------------------------------------
kubectl \
  --kubeconfig="${KARMADA_KUBECONFIG}" \
  create secret generic "${SECRET_NAME}" \
  --namespace="${SECRET_NAMESPACE}" \
  --from-file=kubeconfig="${INTERNAL_KUBECONFIG}" \
  --dry-run=client -o yaml \
  | kubectl \
      --kubeconfig="${KARMADA_KUBECONFIG}" \
      apply -f -

echo "  Secret ${SECRET_NAMESPACE}/${SECRET_NAME} updated — Karmada controller will use Docker bridge IP"

# ------------------------------------------------------------------
# Extract the Docker-IP server URL from the internal kubeconfig and
# patch spec.apiEndpoint on the Cluster object so that Karmada's
# cluster-status controller uses the same reachable address for health
# checks. Without this patch the controller continues to probe the
# localhost address stored by karmadactl join and the cluster never
# transitions to Ready.
# ------------------------------------------------------------------
DOCKER_SERVER=$(kubectl \
  --kubeconfig="${INTERNAL_KUBECONFIG}" \
  config view --minify -o jsonpath='{.clusters[0].cluster.server}')

if [ -z "${DOCKER_SERVER}" ]; then
  echo "ERROR: Could not read server URL from ${INTERNAL_KUBECONFIG}" >&2
  exit 1
fi

echo "  Patching spec.apiEndpoint on cluster '${CLUSTER_NAME}' → ${DOCKER_SERVER}..."
kubectl \
  --kubeconfig="${KARMADA_KUBECONFIG}" \
  patch cluster "${CLUSTER_NAME}" \
  --type=merge \
  -p "{\"spec\":{\"apiEndpoint\":\"${DOCKER_SERVER}\"}}"

echo "  Cluster '${CLUSTER_NAME}' patched — health checks will now use Docker bridge IP"

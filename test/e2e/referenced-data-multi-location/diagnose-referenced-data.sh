#!/bin/sh
set -eu

KDIR=../../../tmp/e2e/kubeconfigs
DOWNSTREAM_NS=$(kubectl --kubeconfig="$KDIR/control-plane.yaml" \
  get namespace "$NAMESPACE" -o template='{{printf "ns-%s" .metadata.uid}}')

for PLANE in control-plane karmada pop-dfw pop-ord; do
  NS="$DOWNSTREAM_NS"
  if [ "$PLANE" = control-plane ]; then
    NS="$NAMESPACE"
  fi
  kubectl --kubeconfig="$KDIR/$PLANE.yaml" -n "$NS" \
    get workloaddeployments -o json | jq --arg plane "$PLANE" \
    '.items[] | {plane: $plane, name: .metadata.name,
      expected: .metadata.annotations["compute.datumapis.com/expected-referenced-data"],
      conditions: .status.conditions}'
done

for CELL in pop-dfw pop-ord; do
  kubectl --kubeconfig="$KDIR/$CELL.yaml" -n "$DOWNSTREAM_NS" \
    get instances -o json | jq --arg cell "$CELL" \
    '.items[] | {cell: $cell, name: .metadata.name,
      owner: .metadata.ownerReferences,
      gates: .spec.controller.schedulingGates,
      conditions: .status.conditions}'
done

#!/usr/bin/env bash
# Runs the InstanceConsoleSession interpreter against fixtures and checks what
# Karmada would write onto the hub copy.
set -euo pipefail

karmadactl=${KARMADACTL:?set KARMADACTL to the karmadactl binary}
dir=$(cd "$(dirname "$0")" && pwd)
customization="$dir/../../config/components/federation/instanceconsolesession-interpreter.yaml"
fixtures="$dir/instanceconsolesession"
failures=0

aggregate() {
  "$karmadactl" interpret -f "$customization" --operation aggregateStatus \
    --observed-file "$fixtures/$1" --status-file "$fixtures/$2"
}

reflect() {
  "$karmadactl" interpret -f "$customization" --operation interpretStatus \
    --observed-file "$fixtures/$1"
}

expect() {
  local name=$1 out=$2
  shift 2
  local want
  for want in "$@"; do
    if ! grep -qF -- "$want" <<<"$out"; then
      printf 'FAIL %s: missing %q in\n%s\n' "$name" "$want" "$out"
      failures=$((failures + 1))
      return
    fi
  done
  printf 'ok   %s\n' "$name"
}

reject() {
  local name=$1 out=$2 unwanted=$3
  if grep -qF -- "$unwanted" <<<"$out"; then
    printf 'FAIL %s: unexpected %q in\n%s\n' "$name" "$unwanted" "$out"
    failures=$((failures + 1))
    return
  fi
  printf 'ok   %s\n' "$name"
}

"$karmadactl" interpret -f "$customization" --check

expect "reflection carries the whole status" "$(reflect cell-connected.yaml)" \
  "endpointID: 5f0c0a1e" "startedAt:" "expiresAt:" "reason: Connected"

expect "the claiming cell's status wins over cells that left the session alone" \
  "$(aggregate hub-unclaimed.yaml items-one-cell-claimed.yaml)" \
  "endpointID: 5f0c0a1e" "reason: SessionReady"

expect "cells without a claim never wipe the hub copy's claim" \
  "$(aggregate hub-claimed.yaml items-none-claimed.yaml)" \
  "endpointID: 5f0c0a1e" "reason: SessionReady"

expect "the end the claiming cell reports replaces its claim" \
  "$(aggregate hub-claimed.yaml items-completed.yaml)" \
  "endpointID: 5f0c0a1e" "reason: Completed" "exitCode: 3" "endedAt:"

expect "a refusal from the cell that runs the instance is reported" \
  "$(aggregate hub-unclaimed.yaml items-refused.yaml)" \
  "reason: NoShell"

expect "the defaulted Pending status is carried while no cell has claimed" \
  "$(aggregate hub-unclaimed.yaml items-none-claimed.yaml)" \
  "reason: Pending"

reject "no status is invented when no cell reports one" \
  "$(aggregate hub-unclaimed.yaml items-empty.yaml)" "status:"

if [ "$failures" -ne 0 ]; then
  printf '%d interpreter check(s) failed\n' "$failures"
  exit 1
fi

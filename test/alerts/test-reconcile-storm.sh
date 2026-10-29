#!/usr/bin/env bash
set -euo pipefail

# Extract the actual VMRule rather than maintaining a second copy of its expr.
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
yq '.spec' "$root/config/components/alerts/reconcile-storm.yaml" > "$work/reconcile-storm.rules.yaml"
cp "$root/test/alerts/reconcile-storm.test.yaml" "$work/test.yaml"
cd "$work"
promtool check rules reconcile-storm.rules.yaml
promtool test rules test.yaml

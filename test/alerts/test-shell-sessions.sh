#!/usr/bin/env bash
set -euo pipefail

# Extract the actual VMRule rather than maintaining a second copy of its expr.
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
rules="$root/config/components/alerts/shell-sessions.yaml"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# Every alert names the service and team the platform's catalog gives compute.
# An alert whose service the catalog does not list reaches no team, and a team
# the catalog does not list is counted against nobody.
owners="$(yq '[.spec.groups[].rules[] | select(.alert) | .labels.service + "/" + .labels.team] | unique | join(",")' "$rules")"
if [ "$owners" != "compute/build" ]; then
  echo "shell-session alerts name service/team '$owners'; want 'compute/build', the catalog's row" >&2
  exit 1
fi

yq '.spec' "$rules" > "$work/shell-sessions.rules.yaml"
cp "$root/test/alerts/shell-sessions.test.yaml" "$work/test.yaml"
cd "$work"
promtool check rules shell-sessions.rules.yaml
promtool test rules test.yaml

// SPDX-License-Identifier: AGPL-3.0-only

package shellagent

import (
	"strconv"
	"strings"
)

const markerPrefix = ".datum-exec-"

// The probe exits 4 when the requested executable is missing and 3 when the
// container has no writable directory for the marker file. A container
// without sh cannot run it at all.
const probeScript = `command -v "$1" >/dev/null 2>&1 || exit 4
for d in /tmp /dev/shm /run /var/tmp; do
  if [ -d "$d" ] && [ -w "$d" ]; then echo "$d"; exit 0; fi
done
exit 3`

const (
	probeExitCommandMissing = 4
	probeExitNoWritableDir  = 3
)

// The wrapper records the command's process ID before it replaces itself with
// the command, so the agent can stop the process group on any ending.
const wrapperScript = `echo $$ > "$1"; shift; exec "$@"`

const killScript = `f="$1"; grace="$2"
[ -f "$f" ] || exit 0
p=$(cat "$f")
case "$p" in ''|*[!0-9]*) rm -f "$f"; exit 0 ;; esac
kill -HUP -"$p" 2>/dev/null; kill -HUP "$p" 2>/dev/null
i=0
while [ "$i" -lt "$grace" ] && kill -0 "$p" 2>/dev/null; do sleep 1; i=$((i+1)); done
kill -KILL -"$p" 2>/dev/null; kill -KILL "$p" 2>/dev/null
rm -f "$f"`

const listMarkersScript = `for f in "$1"/` + markerPrefix + `*; do
  [ -f "$f" ] && echo "${f##*/` + markerPrefix + `}"
done
true`

func markerPath(dir, sessionUID string) string {
	return strings.TrimSuffix(dir, "/") + "/" + markerPrefix + sessionUID
}

func probeCommand(executable string) []string {
	return []string{"sh", "-c", probeScript, "sh", executable}
}

func wrappedCommand(dir, sessionUID string, command []string) []string {
	return append([]string{"sh", "-c", wrapperScript, "sh", markerPath(dir, sessionUID)}, command...)
}

func killCommand(dir, sessionUID string, graceSeconds int) []string {
	return []string{"sh", "-c", killScript, "sh", markerPath(dir, sessionUID), strconv.Itoa(graceSeconds)}
}

func listMarkersCommand(dir string) []string {
	return []string{"sh", "-c", listMarkersScript, "sh", dir}
}

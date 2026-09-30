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

// procStatFunction reads /proc/<pid>/stat into Z (state), G (process group),
// S (session) and T (start time). The command name may hold spaces and
// parentheses, so the fields are taken after its last ")".
const procStatFunction = `st() { Z=; G=; S=; T=; read -r l < "$P/$1/stat" 2>/dev/null || return 1; set -- ${l##*)}; Z=$1 G=$3 S=$4 T=${20}; }
`

// The wrapper records who to stop when the session ends, then replaces itself
// with the command. Job control moves background jobs to process groups of
// their own but never out of the session, so the wrapper records its session
// when it leads one, as every exec does under Kata. Elsewhere it starts a new
// session with setsid where it can, and falls back to its process group. The
// record carries the wrapper's start time so a reused process ID is never
// mistaken for it. Without a terminal or stdin the command reads /dev/null,
// because Kata leaves an exec's stdin open when none was asked for.
const wrapperScript = `w=$1 m=$2 mode=$3 again=$4; shift 4; P=/proc
` + procStatFunction + `if st $$ && [ "$S" != "$$" ] && [ "$G" != "$$" ] && [ "$again" = 0 ] && command -v setsid >/dev/null 2>&1; then
  exec setsid sh -c "$w" sh "$w" "$m" "$mode" 1 "$@"
fi
if ! st $$; then echo "$$" > "$m"
elif [ "$S" = "$$" ]; then echo "$$ $T session" > "$m"
else echo "$$ $T group" > "$m"; fi
[ "$mode" = null ] && exec "$@" </dev/null
exec "$@"`

// The kill script stops every process in the recorded session, or process
// group, that started no earlier than the wrapper: SIGHUP and SIGCONT, then
// SIGKILL after the grace period. It removes the marker, and exits 0, only
// once none is left; otherwise it exits 5 and keeps the marker for the sweep
// to retry. A leader whose process ID now belongs to a younger process means
// the session is long gone. A process that starts a session of its own, with
// setsid, leaves the recorded session and is not stopped.
const killScript = `f=$1 grace=$2 P=${3:-/proc}
[ -f "$f" ] || exit 0
read -r p t kind < "$f"
case "$p" in ''|*[!0-9]*) rm -f "$f"; exit 0 ;; esac
case "$t" in *[!0-9]*) t= ;; esac
` + procStatFunction + `if [ -n "$t" ] && st "$p" && [ "$T" != "$t" ]; then rm -f "$f"; exit 0; fi
members() {
  M=
  if [ -z "$t" ]; then
    kill -0 -"$p" 2>/dev/null && M="-$p"
    kill -0 "$p" 2>/dev/null && M="$M $p"
    return 0
  fi
  for d in "$P"/[0-9]*; do
    q=${d##*/}
    [ "$q" = "$$" ] && continue
    st "$q" || continue
    [ "$Z" = Z ] && continue
    [ "$T" -ge "$t" ] 2>/dev/null || continue
    if [ "$S" = "$p" ] && [ "$kind" = session ] || [ "$G" = "$p" ]; then M="$M $q"; fi
  done
}
members
[ -z "$M" ] && { rm -f "$f"; exit 0; }
kill -HUP $M 2>/dev/null; kill -CONT $M 2>/dev/null
i=0
while [ "$i" -lt "$grace" ]; do
  sleep 1; i=$((i+1))
  members
  [ -z "$M" ] && break
done
i=0
while :; do
  members
  [ -z "$M" ] && { rm -f "$f"; exit 0; }
  [ "$i" -ge 5 ] && exit 5
  kill -KILL $M 2>/dev/null
  sleep 1; i=$((i+1))
done`

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

func wrappedCommand(dir, sessionUID string, command []string, stdin, tty bool) []string {
	mode := "-"
	if !stdin && !tty {
		mode = "null"
	}
	return append([]string{"sh", "-c", wrapperScript, "sh", wrapperScript, markerPath(dir, sessionUID), mode, "0"}, command...)
}

func killCommand(dir, sessionUID string, graceSeconds int) []string {
	return []string{"sh", "-c", killScript, "sh", markerPath(dir, sessionUID), strconv.Itoa(graceSeconds), "/proc"}
}

func listMarkersCommand(dir string) []string {
	return []string{"sh", "-c", listMarkersScript, "sh", dir}
}

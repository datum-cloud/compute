// SPDX-License-Identifier: AGPL-3.0-only

package shellagent

import (
	"strconv"
	"strings"
)

const (
	markerPrefix = ".datum-exec-"
	exitPrefix   = ".datum-exit-"
)

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

// The wrapper records who to stop when the session ends, runs the command,
// and records the command's exit code. Job control moves background jobs to
// process groups of their own but never out of the session, so the wrapper
// records its session when it leads one, as every exec does under Kata.
// Elsewhere it starts a new session with setsid where it can, and falls back
// to its process group. The record carries the wrapper's start time so a
// reused process ID is never mistaken for it. Without a terminal or stdin the
// command reads /dev/null, because Kata leaves an exec's stdin open when none
// was asked for.
//
// The wrapper stays the command's parent rather than exec'ing it, because a
// background job left holding the terminal keeps the exec stream open after
// the command exits. The exit file is how the agent learns the command is
// done without waiting for the stream. It catches SIGINT and SIGQUIT so that
// Ctrl-C reaches the command without killing the wrapper; the command gets
// the default handlers back when it starts.
const wrapperScript = `w=$1 m=$2 x=$3 mode=$4 again=$5; shift 5; P=/proc
` + procStatFunction + `if st $$ && [ "$S" != "$$" ] && [ "$G" != "$$" ] && [ "$again" = 0 ] && command -v setsid >/dev/null 2>&1; then
  exec setsid sh -c "$w" sh "$w" "$m" "$x" "$mode" 1 "$@"
fi
if ! st $$; then echo "$$" > "$m"
elif [ "$S" = "$$" ]; then echo "$$ $T session" > "$m"
else echo "$$ $T group" > "$m"; fi
trap : INT QUIT
if [ "$mode" = null ]; then "$@" </dev/null; else "$@"; fi
rc=$?
echo "$rc" > "$x"
exit "$rc"`

// The exit watch prints the command's exit code once the wrapper records it.
// It gives up once the marker it saw is removed, when the session has been
// stopped, or after its limit in seconds. It exits 3 without looking when the
// container has no sleep, so it never spins.
const exitWatchScript = `x=$1 m=$2 n=$3 seen=0 i=0
command -v sleep >/dev/null 2>&1 || exit 3
while [ "$i" -lt "$n" ]; do
  if [ -s "$x" ]; then read -r rc < "$x"; echo "$rc"; exit 0; fi
  if [ -f "$m" ]; then seen=1; elif [ "$seen" = 1 ]; then exit 2; fi
  sleep 1; i=$((i+1))
done
exit 2`

// The kill script stops every process in the recorded session, or process
// group, that started no earlier than the wrapper: SIGHUP and SIGCONT, then
// SIGKILL after the grace period. It removes the marker, and exits 0, only
// once none is left; otherwise it exits 5 and keeps the marker for the sweep
// to retry. A leader whose process ID now belongs to a younger process means
// the session is long gone. A process that starts a session of its own, with
// setsid, leaves the recorded session and is not stopped. The command's exit
// file goes with the marker.
const killScript = `f=$1 grace=$2 P=${3:-/proc}
x=${f%/*}/` + exitPrefix + `${f##*/` + markerPrefix + `}
[ -f "$f" ] || { rm -f "$x"; exit 0; }
read -r p t kind < "$f"
case "$p" in ''|*[!0-9]*) rm -f "$f" "$x"; exit 0 ;; esac
case "$t" in *[!0-9]*) t= ;; esac
` + procStatFunction + `if [ -n "$t" ] && st "$p" && [ "$T" != "$t" ]; then rm -f "$f" "$x"; exit 0; fi
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
[ -z "$M" ] && { rm -f "$f" "$x"; exit 0; }
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
  [ -z "$M" ] && { rm -f "$f" "$x"; exit 0; }
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

func exitPath(dir, sessionUID string) string {
	return strings.TrimSuffix(dir, "/") + "/" + exitPrefix + sessionUID
}

func probeCommand(executable string) []string {
	return []string{"sh", "-c", probeScript, "sh", executable}
}

func wrappedCommand(dir, sessionUID string, command []string, stdin, tty bool) []string {
	mode := "-"
	if !stdin && !tty {
		mode = "null"
	}
	return append([]string{"sh", "-c", wrapperScript, "sh", wrapperScript,
		markerPath(dir, sessionUID), exitPath(dir, sessionUID), mode, "0"}, command...)
}

func exitWatchCommand(dir, sessionUID string, limitSeconds int) []string {
	return []string{"sh", "-c", exitWatchScript, "sh", exitPath(dir, sessionUID), markerPath(dir, sessionUID), strconv.Itoa(limitSeconds)}
}

func killCommand(dir, sessionUID string, graceSeconds int) []string {
	return []string{"sh", "-c", killScript, "sh", markerPath(dir, sessionUID), strconv.Itoa(graceSeconds), "/proc"}
}

func listMarkersCommand(dir string) []string {
	return []string{"sh", "-c", listMarkersScript, "sh", dir}
}

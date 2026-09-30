// SPDX-License-Identifier: AGPL-3.0-only

package shellagent

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

type fakeProcess struct {
	pid, pgrp, sid, start int
	comm, state           string
	ignoresHUP            bool
	unkillable            bool
}

// fakeKill replaces kill and sleep in the kill script: a signalled process
// dies unless it ignores SIGHUP or cannot be killed, and every call is logged.
const fakeKill = `kill() {
  sig=$1; shift
  echo "$sig $*" >> "$LOG"
  for q in "$@"; do
    case "$sig" in
      -HUP) grep -qx "$q" "$NOHUP" || rm -rf "$P/$q" ;;
      -KILL) grep -qx "$q" "$UNKILLABLE" || rm -rf "$P/$q" ;;
    esac
  done
}
sleep() { :; }
`

type killRun struct {
	exit       int
	signalled  map[string][]int
	survivors  []int
	markerLeft bool
	exitLeft   bool
}

func runKillScript(t *testing.T, marker string, procs []fakeProcess) killRun {
	t.Helper()
	dir := t.TempDir()
	proc := filepath.Join(dir, "proc")
	var nohup, unkillable []string
	for _, p := range procs {
		comm, state := p.comm, p.state
		if comm == "" {
			comm = "sh"
		}
		if state == "" {
			state = "S"
		}
		stat := fmt.Sprintf("%d (%s) %s 1 %d %d 34816 %d 0 0 0 0 0 0 0 0 0 20 0 1 0 %d 4210688 489\n",
			p.pid, comm, state, p.pgrp, p.sid, p.pgrp, p.start)
		if err := os.MkdirAll(filepath.Join(proc, strconv.Itoa(p.pid)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(proc, strconv.Itoa(p.pid), "stat"), []byte(stat), 0o644); err != nil {
			t.Fatal(err)
		}
		if p.ignoresHUP {
			nohup = append(nohup, strconv.Itoa(p.pid))
		}
		if p.unkillable {
			unkillable = append(unkillable, strconv.Itoa(p.pid))
		}
	}
	markerFile := filepath.Join(dir, ".datum-exec-uid")
	if err := os.WriteFile(markerFile, []byte(marker+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	exitFile := filepath.Join(dir, ".datum-exit-uid")
	if err := os.WriteFile(exitFile, []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	logFile := filepath.Join(dir, "log")
	write := func(name string, lines []string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	cmd := exec.Command("sh", "-c", fakeKill+killScript, "sh", markerFile, "3", proc)
	cmd.Env = append(os.Environ(), "LOG="+logFile, "NOHUP="+write("nohup", nohup),
		"UNKILLABLE="+write("unkillable", unkillable))
	out, err := cmd.CombinedOutput()
	res := killRun{signalled: map[string][]int{}}
	if exitErr, ok := err.(*exec.ExitError); ok {
		res.exit = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("kill script: %v: %s", err, out)
	}
	log, _ := os.ReadFile(logFile)
	for _, line := range strings.Split(strings.TrimSpace(string(log)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		for _, f := range fields[1:] {
			n, _ := strconv.Atoi(f)
			res.signalled[fields[0]] = append(res.signalled[fields[0]], n)
		}
	}
	for sig := range res.signalled {
		sort.Ints(res.signalled[sig])
		res.signalled[sig] = uniq(res.signalled[sig])
	}
	entries, _ := os.ReadDir(proc)
	for _, e := range entries {
		n, _ := strconv.Atoi(e.Name())
		res.survivors = append(res.survivors, n)
	}
	sort.Ints(res.survivors)
	_, statErr := os.Stat(markerFile)
	res.markerLeft = statErr == nil
	_, statErr = os.Stat(exitFile)
	res.exitLeft = statErr == nil
	return res
}

func uniq(in []int) []int {
	var out []int
	for i, v := range in {
		if i == 0 || v != in[i-1] {
			out = append(out, v)
		}
	}
	return out
}

func TestKillScriptStopsTheWholeSession(t *testing.T) {
	res := runKillScript(t, "40 1000 session", []fakeProcess{
		{pid: 1, pgrp: 1, sid: 1, start: 10, comm: "init"},
		{pid: 40, pgrp: 40, sid: 40, start: 1000},
		{pid: 41, pgrp: 41, sid: 40, start: 1100, comm: "sleep"},
		{pid: 42, pgrp: 42, sid: 40, start: 1200, comm: "nohup sleep", ignoresHUP: true},
		{pid: 43, pgrp: 43, sid: 40, start: 1300, comm: "odd) name (x", state: "Z"},
		{pid: 44, pgrp: 44, sid: 44, start: 1400, comm: "other session"},
		{pid: 45, pgrp: 45, sid: 40, start: 900, comm: "older"},
	})

	if res.exit != 0 || res.markerLeft || res.exitLeft {
		t.Fatalf("exit %d, marker kept %v, exit file kept %v", res.exit, res.markerLeft, res.exitLeft)
	}
	if got := fmt.Sprint(res.signalled["-HUP"]); got != "[40 41 42]" {
		t.Fatalf("SIGHUP sent to %s", got)
	}
	if got := fmt.Sprint(res.signalled["-KILL"]); got != "[42]" {
		t.Fatalf("SIGKILL sent to %s, want only the process that ignored SIGHUP", got)
	}
	if got := fmt.Sprint(res.survivors); got != "[1 43 44 45]" {
		t.Fatalf("survivors = %s", got)
	}
}

func TestKillScriptKeepsMarkerWhileProcessesSurvive(t *testing.T) {
	res := runKillScript(t, "40 1000 session", []fakeProcess{
		{pid: 40, pgrp: 40, sid: 40, start: 1000},
		{pid: 41, pgrp: 41, sid: 40, start: 1100, ignoresHUP: true, unkillable: true},
	})

	if res.exit != 5 || !res.markerLeft || !res.exitLeft {
		t.Fatalf("exit %d, marker kept %v; want 5 and the marker kept for the sweep", res.exit, res.markerLeft)
	}
}

func TestKillScriptLeavesAReusedProcessIDAlone(t *testing.T) {
	res := runKillScript(t, "40 1000 session", []fakeProcess{
		{pid: 40, pgrp: 40, sid: 40, start: 5000},
		{pid: 41, pgrp: 41, sid: 40, start: 5100},
	})

	if res.exit != 0 || res.markerLeft || len(res.signalled) != 0 {
		t.Fatalf("exit %d, marker kept %v, signalled %v", res.exit, res.markerLeft, res.signalled)
	}
}

func TestKillScriptStopsOnlyTheGroupWithoutASession(t *testing.T) {
	res := runKillScript(t, "40 1000 group", []fakeProcess{
		{pid: 30, pgrp: 30, sid: 30, start: 900},
		{pid: 40, pgrp: 40, sid: 30, start: 1000},
		{pid: 41, pgrp: 40, sid: 30, start: 1100},
		{pid: 42, pgrp: 42, sid: 30, start: 1200},
	})

	if res.exit != 0 || res.markerLeft {
		t.Fatalf("exit %d, marker kept %v", res.exit, res.markerLeft)
	}
	if got := fmt.Sprint(res.survivors); got != "[30 42]" {
		t.Fatalf("survivors = %s, want the rest of the shared session", got)
	}
}

func TestKillScriptWithoutRecordDoesNothing(t *testing.T) {
	res := runKillScript(t, "not-a-pid", []fakeProcess{{pid: 40, pgrp: 40, sid: 40, start: 1000}})

	if res.exit != 0 || res.markerLeft || len(res.signalled) != 0 {
		t.Fatalf("exit %d, marker kept %v, signalled %v", res.exit, res.markerLeft, res.signalled)
	}
}

// The wrapper runs for real where there is a /proc: it records a session it
// leads, or starts one with setsid, and gives a command without stdin or a
// terminal an empty input.
func TestWrapperRecordsItsSession(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("needs /proc")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, ".datum-exec-uid")
	args := wrappedCommand(dir, "uid", []string{"sh", "-c", `cat; read -r l < /proc/$$/stat; set -- ${l##*)}; echo "$4"`}, false, false)
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin = strings.NewReader("must not be read\n")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("wrapper: %v", err)
	}
	record, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(record))
	if len(fields) != 3 {
		t.Fatalf("marker = %q", record)
	}
	if strings.Contains(string(out), "must not be read") {
		t.Fatal("the command read stdin the session did not ask for")
	}
	if _, err := exec.LookPath("setsid"); err == nil &&
		(fields[2] != "session" || strings.TrimSpace(string(out)) != fields[0]) {
		t.Fatalf("marker = %q, command session %q; want a session the wrapper leads", record, out)
	}
}

// A background job that inherits the command's output keeps it open after the
// command exits, as a job holding the terminal keeps the exec stream open. The
// exit watch reports the command's own exit code regardless, and the kill
// script then stops the job.
func TestExitWatchReportsTheCommandWhileABackgroundJobHoldsItsOutput(t *testing.T) {
	dir := t.TempDir()
	args := wrappedCommand(dir, "uid", []string{"sh", "-c", "sleep 30 & exit 3"}, true, false)
	wrapper := exec.Command(args[0], args[1:]...)
	wrapper.Stdout = &strings.Builder{}
	wrapper.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := wrapper.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- wrapper.Wait() }()
	hasProc := runtime.GOOS == "linux"
	stop := func() {
		if hasProc {
			kill := killCommand(dir, "uid", 1)
			if out, err := exec.Command(kill[0], kill[1:]...).CombinedOutput(); err != nil {
				t.Errorf("kill script: %v: %s", err, out)
			}
		}
		_ = syscall.Kill(-wrapper.Process.Pid, syscall.SIGKILL)
	}
	t.Cleanup(stop)

	watch := exitWatchCommand(dir, "uid", 10)
	out, err := exec.Command(watch[0], watch[1:]...).Output()

	if err != nil || strings.TrimSpace(string(out)) != "3" {
		t.Fatalf("exit watch = %q, %v; want the command's exit code 3", out, err)
	}
	select {
	case <-done:
		t.Fatal("the background job no longer holds the output, so this test proves nothing")
	default:
	}
	stop()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the background job survived the session's end")
	}
	if hasProc {
		for _, f := range []string{markerPath(dir, "uid"), exitPath(dir, "uid")} {
			if _, err := os.Stat(f); err == nil {
				t.Errorf("%s left behind", f)
			}
		}
	}
}

func TestExitWatchGivesUpOnceTheSessionIsStopped(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(markerPath(dir, "uid"), []byte("1 1 session\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	watch := exitWatchCommand(dir, "uid", 30)
	cmd := exec.Command(watch[0], watch[1:]...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	time.Sleep(1500 * time.Millisecond)
	if err := os.Remove(markerPath(dir, "uid")); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
			t.Fatalf("exit watch ended with %v, want exit 2", err)
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the exit watch outlived its session")
	}
}

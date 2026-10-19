// SPDX-License-Identifier: AGPL-3.0-only

// shell-reaper is placed in instance Pods by the platform. It is the parent
// and Linux child subreaper for one shell session. It must stay alive until
// all descendants have exited and their statuses have been collected.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const pollInterval = 50 * time.Millisecond

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: shell-reaper run|stop marker [args...]")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = run(os.Args[2], os.Args[3:])
	case "stop":
		err = stop(os.Args[2], os.Args[3:])
	case "install":
		err = install(os.Args[2])
	default:
		err = fmt.Errorf("unknown action %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(125)
	}
}

func install(destination string) error {
	self, err := os.Open("/shell-reaper")
	if err != nil {
		return err
	}
	defer self.Close()
	out, err := os.CreateTemp(filepath.Dir(destination), ".shell-reaper-*")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	if _, err := out.ReadFrom(self); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Chmod(0555); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(out.Name(), destination)
}

func run(marker string, args []string) error {
	if len(args) < 4 || args[0] == "" {
		return errors.New("run needs exit path, grace seconds, stdin mode, and command")
	}
	exitFile := args[0]
	graceSeconds, err := strconv.Atoi(args[1])
	if err != nil || graceSeconds < 0 {
		return errors.New("invalid grace period")
	}
	command := args[3:]
	if err := becomeSubreaper(); err != nil {
		return fmt.Errorf("become child subreaper: %w", err)
	}
	start, err := startTime(os.Getpid())
	if err != nil {
		return fmt.Errorf("read own start time: %w", err)
	}
	// The marker is created before the command, so a failed exec is still
	// visible to the agent's cleanup path.
	if err := os.WriteFile(marker, []byte(fmt.Sprintf("%d %d reaper\n", os.Getpid(), start)), 0600); err != nil {
		return fmt.Errorf("write session marker: %w", err)
	}
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if args[2] == "null" {
		null, err := os.Open(os.DevNull)
		if err != nil {
			_ = os.Remove(marker)
			return err
		}
		defer null.Close()
		cmd.Stdin = null
	}
	if err := cmd.Start(); err != nil {
		_ = os.Remove(marker)
		return fmt.Errorf("start command: %w", err)
	}
	primary := cmd.Process.Pid
	_ = cmd.Process.Release() // wait4 below collects the child instead.
	signals := make(chan os.Signal, 8)
	signal.Notify(signals, syscall.SIGCHLD, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT)
	defer signal.Stop(signals)
	code := 0
	primaryExited, stopping := false, false
	var graceDeadline time.Time
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		for {
			var status syscall.WaitStatus
			pid, waitErr := syscall.Wait4(-1, &status, syscall.WNOHANG, nil)
			if pid <= 0 {
				if waitErr != nil && waitErr != syscall.ECHILD {
					return fmt.Errorf("wait for descendants: %w", waitErr)
				}
				break
			}
			if pid == primary {
				primaryExited = true
				if status.Exited() {
					code = status.ExitStatus()
				} else if status.Signaled() {
					code = 128 + int(status.Signal())
				}
				_ = os.WriteFile(exitFile, []byte(fmt.Sprintf("%d\n", code)), 0600)
				stopping = true
				graceDeadline = time.Now().Add(time.Duration(graceSeconds) * time.Second)
			}
		}
		alive, err := descendants(os.Getpid())
		if err != nil {
			return err
		}
		if stopping {
			sig := syscall.SIGHUP
			if !time.Now().Before(graceDeadline) {
				sig = syscall.SIGKILL
			}
			for _, p := range alive {
				_ = signalProcess(p, sig)
				if sig == syscall.SIGHUP {
					_ = signalProcess(p, syscall.SIGCONT)
				}
			}
			if primaryExited && len(alive) == 0 {
				// One last wait closes the race with an exiting adopted child.
				var status syscall.WaitStatus
				if pid, e := syscall.Wait4(-1, &status, syscall.WNOHANG, nil); pid == -1 && e == syscall.ECHILD {
					_ = os.Remove(marker)
					os.Exit(code)
				}
			}
		}
		select {
		case sig := <-signals:
			// Ctrl-C and Ctrl-\\ reach the foreground command too. Keep
			// the supervisor alive so an interactive shell survives them.
			if (sig == syscall.SIGHUP || sig == syscall.SIGTERM) && !stopping {
				stopping = true
				graceDeadline = time.Now().Add(time.Duration(graceSeconds) * time.Second)
			}
		case <-ticker.C:
		}
	}
}

type process struct {
	pid, ppid int
	start     uint64
	state     byte
}

func procStat(pid int) (process, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return process{}, err
	}
	// comm can contain spaces and parentheses; the last ')' ends the field.
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return process{}, errors.New("malformed proc stat")
	}
	f := strings.Fields(string(data[end+1:]))
	if len(f) < 20 {
		return process{}, errors.New("short proc stat")
	}
	ppid, err := strconv.Atoi(f[1])
	if err != nil {
		return process{}, err
	}
	start, err := strconv.ParseUint(f[19], 10, 64)
	return process{pid: pid, ppid: ppid, state: f[0][0], start: start}, err
}

func startTime(pid int) (uint64, error) {
	p, err := procStat(pid)
	return p.start, err
}

func descendants(parent int) ([]process, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	byParent := make(map[int][]process)
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		p, err := procStat(pid)
		if err == nil {
			byParent[p.ppid] = append(byParent[p.ppid], p)
		}
	}
	var found []process
	queue := []int{parent}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		for _, child := range byParent[pid] {
			queue = append(queue, child.pid)
			if child.state != 'Z' && child.state != 'X' {
				found = append(found, child)
			}
		}
	}
	return found, nil
}

func signalProcess(p process, sig syscall.Signal) error {
	current, err := procStat(p.pid)
	if err != nil || current.start != p.start {
		return err
	}
	return syscall.Kill(p.pid, sig)
}

func stop(marker string, args []string) error {
	if len(args) != 1 {
		return errors.New("stop needs timeout seconds")
	}
	seconds, err := strconv.Atoi(args[0])
	if err != nil || seconds < 0 {
		return errors.New("invalid timeout")
	}
	data, err := os.ReadFile(marker)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	f := strings.Fields(string(data))
	if len(f) != 3 || f[2] != "reaper" {
		return errors.New("unsupported session marker")
	}
	pid, err := strconv.Atoi(f[0])
	if err != nil {
		return err
	}
	start, err := strconv.ParseUint(f[1], 10, 64)
	if err != nil {
		return err
	}
	p, err := procStat(pid)
	if err != nil || p.start != start {
		return errors.New("session supervisor vanished before confirming cleanup")
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return err
	}
	deadline := time.Now().Add(time.Duration(seconds+5) * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); errors.Is(err, os.ErrNotExist) {
			return nil
		}
		time.Sleep(pollInterval)
	}
	return fmt.Errorf("session supervisor did not confirm cleanup: %s", filepath.Base(marker))
}

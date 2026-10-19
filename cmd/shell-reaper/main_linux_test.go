// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestReapsBackgroundAndDetachedDescendants(t *testing.T) {
	for _, detached := range []bool{false, true} {
		t.Run(fmt.Sprint(detached), func(t *testing.T) {
			if detached {
				if _, err := exec.LookPath("setsid"); err != nil {
					t.Skip("setsid is unavailable")
				}
			}
			dir := t.TempDir()
			marker, exitFile, pidFile := filepath.Join(dir, "marker"), filepath.Join(dir, "exit"), filepath.Join(dir, "pid")
			command := "sleep 30 & echo $! > '" + pidFile + "'"
			if detached {
				command = "setsid sh -c 'sleep 30' & echo $! > '" + pidFile + "'"
			}
			cmd := testReaper("run", marker, exitFile, "1", "null", "sh", "-c", command)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("supervisor: %v: %s", err, out)
			}
			pidBytes, err := os.ReadFile(pidFile)
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(fmt.Sprintf("/proc/%d/stat", pid)); !os.IsNotExist(err) {
				t.Fatalf("descendant %d was not reaped: %v", pid, err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("marker still exists: %v", err)
			}
		})
	}
}

func TestStopWaitsForCleanup(t *testing.T) {
	dir := t.TempDir()
	marker, exitFile := filepath.Join(dir, "marker"), filepath.Join(dir, "exit")
	cmd := testReaper("run", marker, exitFile, "1", "null", "sh", "-c", "sleep 30 & wait")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill() }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("supervisor did not start")
	}
	if err := stop(marker, []string{"1"}); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("interrupted command should not exit successfully")
	}
}

func testReaper(args ...string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], append([]string{"-test.run=TestReaperProcess", "--"}, args...)...)
	cmd.Env = append(os.Environ(), "SHELL_REAPER_TEST=1")
	return cmd
}

func TestReaperProcess(_ *testing.T) {
	if os.Getenv("SHELL_REAPER_TEST") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"shell-reaper"}, os.Args[i+1:]...)
			main()
			return
		}
	}
	os.Exit(2)
}

func TestStopDoesNotClaimStaleMarkerWasCleaned(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "marker")
	if err := os.WriteFile(marker, []byte("999999 1 reaper\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := stop(marker, []string{"0"}); err == nil {
		t.Fatal("stale supervisor must leave cleanup unconfirmed")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("stale marker must remain for investigation")
	}
}

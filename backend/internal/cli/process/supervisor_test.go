package process

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSupervisorParentHelper(t *testing.T) {
	if os.Getenv("FLOWLINE_CRASH_PARENT_HELPER") != "1" {
		return
	}

	_, _, err := RunProcess(context.Background(), []string{"/bin/sh", "-c", "trap '' TERM; echo $$ > target.pid; sleep 60 & echo $! > descendant.pid; wait"}, os.Getenv("FLOWLINE_CRASH_DIRECTORY"), "", nil)
	if err != nil {
		os.Exit(1)
	}

	os.Exit(0)
}

func TestSupervisorKillsTargetAndDescendantsWhenBackendCrashes(t *testing.T) {
	dir := t.TempDir()
	parent := exec.Command(os.Args[0], "-test.run=^TestSupervisorParentHelper$")
	parent.Env = append(os.Environ(), "FLOWLINE_CRASH_PARENT_HELPER=1", "FLOWLINE_CRASH_DIRECTORY="+dir)
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Process.Kill() })
	target := awaitPID(t, filepath.Join(dir, "target.pid"))
	descendant := awaitPID(t, filepath.Join(dir, "descendant.pid"))
	targetStopped, descendantStopped := false, false
	t.Cleanup(func() {
		if !targetStopped {
			_ = syscall.Kill(target, syscall.SIGKILL)
		}
		if !descendantStopped {
			_ = syscall.Kill(descendant, syscall.SIGKILL)
		}
	})

	if err := parent.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = parent.Wait()

	awaitDead(t, target)
	targetStopped = true
	awaitDead(t, descendant)
	descendantStopped = true
}

func TestSupervisorDoesNotLeakLivenessPipe(t *testing.T) {
	out, code, err := RunProcess(context.Background(), []string{"/bin/sh", "-c", "if test -e /dev/fd/3; then echo leaked; exit 9; fi; printf clean"}, t.TempDir(), "", nil)
	if err != nil || code != 0 || out != "clean" {
		t.Fatalf("liveness descriptor leaked: out=%q code=%d err=%v", out, code, err)
	}
}

func TestSupervisorRejectsInvocationWithoutLivenessPipe(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], supervisorArgument, "/bin/sh", "-c", "touch should-not-run")
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "liveness pipe") {
		t.Fatalf("invalid supervisor accepted: %q, %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "should-not-run")); !os.IsNotExist(err) {
		t.Fatal("command executed without a guardian pipe")
	}
}

func TestSupervisorPreservesExitStatusAndFastSuccess(t *testing.T) {
	started := time.Now()
	out, code, err := RunProcess(context.Background(), []string{"/bin/sh", "-c", "printf result; exit 23"}, t.TempDir(), "", nil)
	if err == nil || code != 23 || out != "result" {
		t.Fatalf("exit status lost: %q, %d, %v", out, code, err)
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("completed command stalled in guardian")
	}
}

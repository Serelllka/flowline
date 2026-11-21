package process

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunProcessOutputAndFailure(t *testing.T) {
	var logs strings.Builder
	out, code, err := RunProcess(context.Background(), []string{"/bin/sh", "-c", "read value; printf '%s' \"$value\"; printf 'problem\\n' >&2; exit 7"}, t.TempDir(), "actual input\n", func(s string) { logs.WriteString(s) })
	if err == nil || code != 7 {
		t.Fatalf("failure hidden: code=%d err=%v", code, err)
	}
	if out != "actual input" {
		t.Fatalf("stdout contaminated: %q", out)
	}
	if !strings.Contains(logs.String(), "[stderr] problem") {
		t.Fatal("stderr missing")
	}
}

func TestRunProcessRedactsAcrossWrites(t *testing.T) {
	t.Setenv("FLOWLINE_TEST_TOKEN", "sensitive-value-123")
	var logs strings.Builder
	out, _, err := RunProcess(context.Background(), []string{"/bin/sh", "-c", "printf sensitive-; sleep 0.05; printf 'value-123\\n'; printf 'Authorization: Bearer abcdefghijklmnop\\n'"}, t.TempDir(), "", func(s string) { logs.WriteString(s) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "sensitive-value-123") {
		t.Fatal("raw internal stdout was modified")
	}
	if strings.Contains(logs.String(), "sensitive-value") || strings.Contains(logs.String(), "abcdefghijklmnop") {
		t.Fatalf("secret in log: %q", logs.String())
	}
}

func TestRunProcessCancelsOwnedDescendants(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "child.pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		_, _, err := RunProcess(ctx, []string{"/bin/sh", "-c", "sleep 60 & echo $! > child.pid; wait"}, dir, "", nil)
		finished <- err
	}()
	child := awaitPID(t, pidPath)
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancel hung")
	}
	awaitDead(t, child)
}

func TestRunProcessCleansUpAfterParentExits(t *testing.T) {
	dir := t.TempDir()
	started := time.Now()
	_, _, err := RunProcess(context.Background(), []string{"/bin/sh", "-c", "sleep 60 & echo $! > child.pid; exit 0"}, dir, "", nil)
	// A retained pipe is an explicit execution error, never a fake successful result.
	if err == nil {
		t.Fatal("orphan holding a pipe should be reported")
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("parent exit hung")
	}
	child := awaitPID(t, filepath.Join(dir, "child.pid"))
	awaitDead(t, child)
}

func TestRunProcessBoundsOutput(t *testing.T) {
	_, _, err := RunProcess(context.Background(), []string{"/bin/sh", "-c", "while :; do printf 'abcdefghijklmnopqrstuvwxyz0123456789\\n'; done"}, t.TempDir(), "", nil)
	if !errors.Is(err, ErrOutputLimit) {
		t.Fatalf("expected output limit error: %v", err)
	}
}

func awaitPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err == nil {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("child did not start")
	return 0
}

func awaitDead(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("child %d is still alive", pid)
}

func TestRunProcessEscalatesIgnoredTermination(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		_, _, err := RunProcess(ctx, []string{"/bin/sh", "-c", "trap '' TERM; echo $$ > child.pid; while :; do sleep 60; done"}, dir, "", nil)
		finished <- err
	}()
	pid := awaitPID(t, filepath.Join(dir, "child.pid"))
	cancel()

	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ignored termination was not escalated")
	}
	awaitDead(t, pid)
}

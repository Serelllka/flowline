package process

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

const supervisorArgument = "--flowline-process-guard"

// The helper runs before the application's main (or testing main). The parent
// supplies a read-only liveness pipe as descriptor 3. No PID survives this
// invocation: the helper owns and reaps its exact child process group.
func init() {
	if len(os.Args) >= 2 && os.Args[1] == supervisorArgument {
		code := superviseProcess(os.Args[2:])
		// The helper has no application shutdown hooks to run. In particular,
		// this avoids invoking the testing main when the executable is a test.
		syscall.Exit(code)
	}
}

func superviseProcess(argv []string) int {
	if len(argv) == 0 || syscall.Getpgrp() != os.Getpid() {
		fmt.Fprintln(os.Stderr, "process guard requires an isolated group and a command")
		return 125
	}

	liveness := os.NewFile(3, "flowline-parent-liveness")
	if liveness == nil {
		fmt.Fprintln(os.Stderr, "process guard has no liveness descriptor")
		return 125
	}
	defer liveness.Close()
	info, err := liveness.Stat()
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		fmt.Fprintln(os.Stderr, "process guard requires a liveness pipe")
		return 125
	}
	syscall.CloseOnExec(3)

	parentGone := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, liveness)
		close(parentGone)
	}()
	stopping := make(chan os.Signal, 1)
	signal.Notify(stopping, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(stopping)

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "start guarded process:", err)
		return 127
	}

	finished := make(chan error, 1)
	go func() { finished <- cmd.Wait() }()
	select {
	case err := <-finished:
		// The main command can exit while grandchildren still hold output
		// pipes. Clean them here, before the guardian can disappear.
		remaining := syscall.Kill(-cmd.Process.Pid, 0) == nil
		stopProcessGroup(cmd.Process.Pid)
		if err != nil {
			return guardedExitCode(cmd.ProcessState)
		}
		if remaining {
			fmt.Fprintln(os.Stderr, "process exited while descendants were still running")
			return 125
		}

		return 0
	case <-parentGone:
		stopProcessGroup(cmd.Process.Pid)
		<-finished
		return 125
	case <-stopping:
		stopProcessGroup(cmd.Process.Pid)
		<-finished
		return 143
	}
}

func stopProcessGroup(pid int) {
	if err := syscall.Kill(-pid, syscall.SIGTERM); errors.Is(err, syscall.ESRCH) {
		return
	}

	timer := time.NewTimer(150 * time.Millisecond)
	defer timer.Stop()
	<-timer.C
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

func guardedExitCode(state *os.ProcessState) int {
	if code := state.ExitCode(); code >= 0 {
		return code
	}
	if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal())
	}

	return 125
}

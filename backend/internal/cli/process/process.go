package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

const processOutputLimit = 4 << 20

// ErrOutputLimit reports bounded process output truncation as an execution failure.
var ErrOutputLimit = errors.New("process output limit exceeded (4 MiB per stream or 64 KiB per line)")
var secretAssignment = regexp.MustCompile(`(?i)((?:password|passwd|secret|token|api[_-]?key|authorization)["']?\s*[:=]\s*)(?:"[^"\r\n]*"|'[^'\r\n]*'|[^\s,;]+)`)
var bearerSecret = regexp.MustCompile(`(?i)\bBearer\s+[^\s"',;]+`)
var urlCredential = regexp.MustCompile(`(https?://)[^/@\s]+@`)
var knownToken = regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9_]{16,}|github_pat_[A-Za-z0-9_]{16,}|sk-[A-Za-z0-9_-]{16,})\b`)
var privateKey = regexp.MustCompile(`(?s)-----BEGIN [^-]*PRIVATE KEY-----.*?(?:-----END [^-]*PRIVATE KEY-----|$)`)

// Redact removes recognizable credentials before text crosses the log/API boundary.
func Redact(text string) string {
	configuredSecrets.Range(func(key, _ any) bool { text = strings.ReplaceAll(text, key.(string), "[REDACTED]"); return true })
	for _, item := range os.Environ() {
		name, value, ok := strings.Cut(item, "=")
		upper := strings.ToUpper(name)
		sensitive := strings.Contains(upper, "TOKEN") || strings.Contains(upper, "SECRET") || strings.Contains(upper, "PASSWORD") || strings.Contains(upper, "API_KEY") || strings.Contains(upper, "PRIVATE_KEY")
		if ok && sensitive && len(value) >= 4 {
			text = strings.ReplaceAll(text, value, "[REDACTED]")
		}
	}
	text = privateKey.ReplaceAllString(text, "[REDACTED PRIVATE KEY]")
	text = urlCredential.ReplaceAllString(text, "${1}[REDACTED]@")
	text = bearerSecret.ReplaceAllString(text, "Bearer [REDACTED]")
	text = secretAssignment.ReplaceAllString(text, "${1}[REDACTED]")

	return knownToken.ReplaceAllString(text, "[REDACTED]")
}

type processStream struct {
	mu       sync.Mutex
	raw      bytes.Buffer
	pending  []byte
	overflow bool
	private  bool
	label    string
	emit     func(string)
	cancel   context.CancelFunc
}

func (w *processStream) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	original := len(p)
	remaining := processOutputLimit - w.raw.Len()
	if len(p) > remaining {
		p = p[:remaining]
		w.overflow = true
		w.cancel()
	}
	w.raw.Write(p)
	w.pending = append(w.pending, p...)
	for {
		index := bytes.IndexByte(w.pending, '\n')
		if index < 0 {
			break
		}
		w.line(string(w.pending[:index+1]))
		w.pending = w.pending[index+1:]
	}
	// Never emit fragments of an overlong line: a secret may span write chunks.
	if len(w.pending) > 64<<10 {
		w.pending = nil
		w.overflow = true
		w.cancel()
	}

	return original, nil
}

func (w *processStream) line(line string) {
	if strings.Contains(line, "-----BEGIN") && strings.Contains(line, "PRIVATE KEY-----") {
		w.private = true
		w.emit(w.label + "[REDACTED PRIVATE KEY]\n")
	}
	if w.private {
		if strings.Contains(line, "-----END") && strings.Contains(line, "PRIVATE KEY-----") {
			w.private = false
		}
		return
	}
	w.emit(w.label + Redact(line))
}

func (w *processStream) finish() (string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if len(w.pending) != 0 {
		w.line(string(w.pending))
	}
	w.pending = nil

	return w.raw.String(), w.overflow
}

// RunProcess owns a POSIX process group, including inherited descendants.
// It never invokes a shell implicitly. Callbacks receive redacted, complete lines.
func RunProcess(ctx context.Context, argv []string, cwd, input string, onOutput func(string)) (string, int, error) {
	if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
		return "", -1, errors.New("executor command is empty")
	}
	if err := ctx.Err(); err != nil {
		return "", -1, err
	}

	owned, cancel := context.WithCancel(ctx)
	defer cancel()
	var callbackMu sync.Mutex
	emit := func(s string) {
		if onOutput != nil {
			callbackMu.Lock()
			defer callbackMu.Unlock()
			onOutput(s)
		}
	}
	stdout := &processStream{emit: emit, cancel: cancel}
	stderr := &processStream{emit: emit, cancel: cancel, label: "[stderr] "}
	// Resolve the requested executable before starting the helper so missing
	// commands remain startup errors, rather than a successful helper launch.
	target := exec.Command(argv[0], argv[1:]...)
	if target.Err != nil {
		return "", -1, fmt.Errorf("start process: %w", target.Err)
	}
	executable, err := os.Executable()
	if err != nil {
		return "", -1, fmt.Errorf("locate process guard: %w", err)
	}
	livenessReader, livenessWriter, err := os.Pipe()
	if err != nil {
		return "", -1, fmt.Errorf("create process liveness pipe: %w", err)
	}
	defer livenessReader.Close()
	defer livenessWriter.Close()

	guardArgs := append([]string{supervisorArgument, target.Path}, argv[1:]...)
	cmd := exec.Command(executable, guardArgs...)
	cmd.Dir = cwd
	cmd.Stdin = strings.NewReader(input)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.ExtraFiles = []*os.File{livenessReader}
	// Parent exit must not hang on an orphan retaining a pipe.
	cmd.WaitDelay = 300 * time.Millisecond
	if err := cmd.Start(); err != nil {
		return "", -1, fmt.Errorf("start process: %w", err)
	}
	// Only the child keeps the read end. Only this parent holds the write end;
	// kernel closure on a crash therefore wakes the independent guardian.
	_ = livenessReader.Close()

	done := make(chan struct{})
	watcherDone := make(chan struct{})
	var terminateOnce sync.Once
	terminate := func() {
		terminateOnce.Do(func() {
			_ = livenessWriter.Close()
			if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil {
				return
			}
			timer := time.NewTimer(time.Second)
			defer timer.Stop()
			select {
			case <-done:
				return
			case <-timer.C:
			}
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		})
	}
	go func() {
		defer close(watcherDone)
		select {
		case <-owned.Done():
			terminate()
		case <-done:
		}
	}()

	err = cmd.Wait()
	close(done)
	// Also clean up children when the parent exits before cancellation.
	terminate()
	<-watcherDone
	out, outOverflow := stdout.finish()
	_, errOverflow := stderr.finish()
	code := cmd.ProcessState.ExitCode()
	if outOverflow || errOverflow {
		return out, code, ErrOutputLimit
	}
	if ctx.Err() != nil {
		return out, code, ctx.Err()
	}
	if err != nil {
		return out, code, fmt.Errorf("process failed (exit %d): %w", code, err)
	}

	return out, code, nil
}

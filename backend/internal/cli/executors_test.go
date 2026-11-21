package cli_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flowline/internal/cli/claude"
	"flowline/internal/cli/codex"
	"flowline/internal/model"
)

func fixtureCLI(t *testing.T, kind, body string) model.Executor {
	t.Helper()
	command := filepath.Join(t.TempDir(), "fixture CLI")
	if err := os.WriteFile(command, []byte("#!/bin/sh\nset -eu\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	config := model.ExecutorConfig{ID: "fixture", Name: "Fixture", Kind: kind, Command: command, Model: "test-model"}
	var executor model.Executor
	var err error
	if kind == "codex" {
		executor, err = codex.New(config)
	} else {
		executor, err = claude.New(config)
	}
	if err != nil {
		t.Fatal(err)
	}
	return executor
}

func outputNode() model.Node {
	return model.Node{Outputs: []model.Output{{ID: "result-id", Name: "result"}}}
}

func TestCodexCLIUsesRealProcessStdinAndFinalResult(t *testing.T) {
	executor := fixtureCLI(t, "codex", `
printf '%s\n' "$@" > args.txt
cat > received-prompt.txt
while [ "$#" -gt 0 ]; do
 case "$1" in
 --output-last-message) shift; final="$1" ;;
 --output-schema) shift; cat "$1" > received-schema.json ;;
 esac
 shift
done
printf '{"result":"actual final result"}' > "$final"
printf '%s\n' '{"type":"item.completed","item":{"text":"intermediate transcript must not be the result"}}' '{"type":"turn.completed"}'
`)
	cwd := t.TempDir()
	var logs strings.Builder
	outputs, code, err := executor.Execute(context.Background(), outputNode(), cwd, "private task prompt", func(line string) { logs.WriteString(line) })
	if err != nil || code != 0 || outputs["result"] != "actual final result" {
		t.Fatalf("outputs=%v code=%d err=%v", outputs, code, err)
	}
	prompt, err := os.ReadFile(filepath.Join(cwd, "received-prompt.txt"))
	if err != nil || string(prompt) != "private task prompt" {
		t.Fatal("stdin prompt not received")
	}
	args, err := os.ReadFile(filepath.Join(cwd, "args.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"--no-daemon\n", "never\n", "workspace-write\n", "--ephemeral\n", "--model\ntest-model\n"} {
		if !strings.Contains(string(args), required) {
			t.Fatalf("missing %q in %s", required, args)
		}
	}
	if strings.Contains(string(args), "private task prompt") || strings.Contains(string(args), "bypass") {
		t.Fatal("unsafe CLI args")
	}
	if logs.Len() == 0 {
		t.Fatal("real logs missing")
	}
}

func TestCodexCLINeverTreatsFailureOrTranscriptAsSuccess(t *testing.T) {
	cases := []struct{ name, body string }{
		{"nonzero", `printf '{"type":"turn.completed"}\n'; exit 9`},
		{"explicit failure", `printf '{"type":"turn.failed","error":{"message":"real failure"}}\n'`},
		{"missing final", `printf '{"type":"turn.completed"}\n'`},
		{"no completion", `printf '{"type":"item.completed","item":{"text":"looks successful"}}\n'`},
		{"malformed events", `printf 'plain transcript\n'`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			executor := fixtureCLI(t, "codex", tt.body)
			if _, _, err := executor.Execute(context.Background(), outputNode(), t.TempDir(), "task", nil); err == nil {
				t.Fatal("false success")
			}
		})
	}
}

func TestClaudeCLIUsesStructuredOutputAndSubscriptionCompatibleArgs(t *testing.T) {
	executor := fixtureCLI(t, "claude", `
printf '%s\n' "$@" > args.txt
cat > received-prompt.txt
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"wrapper text","structured_output":{"result":"real structured result"}}'
`)
	cwd := t.TempDir()
	outputs, code, err := executor.Execute(context.Background(), outputNode(), cwd, "task", nil)
	if err != nil || code != 0 || outputs["result"] != "real structured result" {
		t.Fatalf("outputs=%v code=%d err=%v", outputs, code, err)
	}
	args, err := os.ReadFile(filepath.Join(cwd, "args.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"--bare", "--dangerously-skip-permissions", "--allow-dangerously-skip-permissions"} {
		if strings.Contains(string(args), flag) {
			t.Fatalf("unsafe subscription/permissions flag %s", flag)
		}
	}
	if !strings.Contains(string(args), "--permission-prompts\nnone\n") {
		t.Fatal("approval prompt could hang unattended run")
	}
}

func TestClaudeCLIRejectsFailedMissingAndDeniedResults(t *testing.T) {
	for _, envelope := range []string{
		`{"type":"result","is_error":true,"result":"failure","structured_output":{"result":"bad"}}`,
		`{"type":"result","subtype":"error_max_turns","result":"incomplete"}`,
		`{"type":"result","subtype":"success","result":"unstructured response"}`,
		`{"type":"result","subtype":"success","structured_output":{"result":"bad"},"permission_denials":[{"tool_name":"Bash"}]}`,
		`{"type":"result","subtype":"success","structured_output":{"wrong":"bad"}}`,
	} {
		executor := fixtureCLI(t, "claude", "printf '%s\\n' '"+envelope+"'\n")
		if _, _, err := executor.Execute(context.Background(), outputNode(), t.TempDir(), "task", nil); err == nil {
			t.Fatalf("false success for %s", envelope)
		}
	}
}

func TestCLIExecutorCancellation(t *testing.T) {
	executor := fixtureCLI(t, "codex", `sleep 60 & wait`)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err := executor.Execute(ctx, outputNode(), t.TempDir(), "task", nil)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 3*time.Second {
		t.Fatalf("cancellation err=%v elapsed=%s", err, time.Since(start))
	}
}

func TestUnavailableCLIInfo(t *testing.T) {
	executor, err := codex.New(model.ExecutorConfig{ID: "missing", Name: "Missing", Kind: "codex", Command: filepath.Join(t.TempDir(), "missing")})
	if err != nil {
		t.Fatal(err)
	}
	if executor.Info().Available {
		t.Fatal("missing CLI advertised available")
	}
}

func TestParseExecutorOutputs(t *testing.T) {
	single := outputNode()
	outputs, err := model.ParseExecutorOutputs(single, "plain final result")
	if err != nil || outputs["result"] != "plain final result" {
		t.Fatalf("outputs=%v err=%v", outputs, err)
	}
	multi := model.Node{Outputs: []model.Output{{ID: "a", Name: "changes"}, {ID: "b", Name: "tests"}}}
	outputs, err = model.ParseExecutorOutputs(multi, `{"changes":"actual changes","tests":{"passed":2}}`)
	if err != nil || outputs["changes"] != "actual changes" || outputs["tests"] != `{"passed":2}` {
		t.Fatalf("outputs=%v err=%v", outputs, err)
	}
	for _, bad := range []string{"plain text", `{}`, `{"changes":"x"}`, `{"changes":"x","tests":null}`, `{"changes":"x","changes":"y","tests":"ok"}`, `{"changes":"x","tests":"ok","extra":true}`, `{"changes":"x","tests":"ok"} more`, ""} {
		if _, err := model.ParseExecutorOutputs(multi, bad); err == nil {
			t.Fatalf("invalid output accepted: %s", bad)
		}
	}
}

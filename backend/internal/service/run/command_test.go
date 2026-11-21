package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"flowline/internal/model"
	"flowline/internal/repository/sqlite"
)

func commandNode(id, script string) model.Node {
	return model.Node{ID: id, Type: "cmd", Name: id, Command: script, Outputs: []model.Output{
		{ID: id + "-out", Name: "stdout"},
		{ID: id + "-err", Name: "stderr"},
		{ID: id + "-exit", Name: "exitCode"},
	}}
}

func inputText(text string) *string { return &text }

func startPipeline(t *testing.T, engine *Engine, store *sqlite.Store, repo model.Repository, p model.Pipeline) model.Run {
	t.Helper()
	if err := store.SavePipeline(p); err != nil {
		t.Fatal(err)
	}

	run, err := engine.Start(context.Background(), p.ID, repo.ID, "main", 0)
	if err != nil {
		t.Fatal(err)
	}

	return run
}

func TestCMDWithoutExecutorsStreamsAndFailures(t *testing.T) {
	for _, tt := range []struct {
		name, script, stdout, stderr string
		code                         int
	}{
		{"streams", "word=hello\nprintf '%s' \"$word\" | tr a-z A-Z\nprintf 'warning\\n' >&2", "HELLO", "warning\n", 0},
		{"empty streams", ":", "", "", 0},
		{"nonzero", "printf partial; printf problem >&2; exit 7", "partial", "problem", 7},
		{"pipefail", "false | cat\nprintf must-not-run", "", "", 1},
		{"errexit", "false\nprintf must-not-run", "", "", 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			engine, store, repo, p := engineFixture(t)
			if err := engine.InitializeExecutors(nil); err != nil {
				t.Fatal(err)
			}
			p.Nodes = []model.Node{commandNode("cmd", tt.script), commandNode("next", ":")}
			p.Nodes[0].Outputs[0].Name, p.Nodes[0].Outputs[0].Source = "text", "stdout"
			p.Edges = []model.Edge{{From: "cmd", To: "next"}}

			started := startPipeline(t, engine, store, repo, p)
			result := waitRun(t, engine, started.ID)
			node := result.Nodes["cmd"]
			if node.ExitCode == nil || *node.ExitCode != tt.code || node.Stdout != tt.stdout || node.Stderr != tt.stderr || node.Command != tt.script {
				t.Fatalf("lost command details: %+v", node)
			}
			if out, ok := node.Outputs["text"]; !ok || out != tt.stdout || node.Outputs["exitCode"] != strconv.Itoa(tt.code) {
				t.Fatalf("stdout mapping/empty output lost: %v", node.Outputs)
			}
			if stderr, ok := node.Outputs["stderr"]; !ok || stderr != tt.stderr {
				t.Fatalf("stderr lost: %v", node.Outputs)
			}
			if tt.code != 0 {
				if node.Status != "error" || node.Error == "" || result.Nodes["next"].Status != "skipped" || result.Status != "error" {
					t.Fatalf("failure hidden: %+v", result)
				}
			} else if result.Status != "success" {
				t.Fatalf("command pipeline failed: %+v", result)
			}
			if tt.stderr != "" && !strings.Contains(node.Logs, "[stderr] "+tt.stderr) {
				t.Fatalf("stderr missing in logs: %q", node.Logs)
			}
		})
	}
}

func TestCMDJSONStdinIsDataAndHistoryIsFrozen(t *testing.T) {
	engine, store, repo, p := engineFixture(t)
	engine.executors = nil
	data := `{"message":"quotes \" and newline\n$(touch injected); ` + "`touch injected2`" + `","branch":"{{branch}}"}`
	script := "input=$(cat)\nprintf '%s\\n' \"$input\"\nprintf '{{missing}}' >&2"
	p.Nodes = []model.Node{commandNode("cmd", script)}
	p.Nodes[0].InputJSON = &data
	started := startPipeline(t, engine, store, repo, p)
	p.Nodes[0].Command, p.Nodes[0].InputJSON = "exit 99", inputText(`{"changed":true}`)
	if err := store.SavePipeline(p); err != nil {
		t.Fatal(err)
	}

	result := waitRun(t, engine, started.ID)
	node := result.Nodes["cmd"]
	if result.Status != "success" {
		t.Fatalf("command failed: %+v", node)
	}
	var stdin struct {
		Context model.GitContext
		Data    map[string]string
	}
	if err := json.Unmarshal([]byte(node.Stdout), &stdin); err != nil {
		t.Fatal(err)
	}
	if stdin.Data["message"] != "quotes \" and newline\n$(touch injected); `touch injected2`" || stdin.Data["branch"] != "main" || stdin.Context.SHA != started.Context.SHA {
		t.Fatalf("input changed: %s", node.Stdout)
	}
	var retained any
	if err := json.Unmarshal(node.InputJSON, &retained); err != nil {
		t.Fatalf("invalid retained JSON: %v", err)
	}
	var actual any
	if err := json.Unmarshal([]byte(node.Stdout), &actual); err != nil {
		t.Fatal(err)
	}
	actualJSON, _ := json.Marshal(actual)
	retainedJSON, _ := json.Marshal(retained)
	if string(actualJSON) != string(retainedJSON) || node.Stderr != "{{missing}}" || result.Snapshot.Nodes[0].Command != script || *result.Snapshot.Nodes[0].InputJSON != data {
		t.Fatal("resolved input, literal script or snapshot changed")
	}
	for _, file := range []string{"injected", "injected2"} {
		if _, err := os.Stat(filepath.Join(node.Worktree, file)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("input executed as shell code: %s (%v)", file, err)
		}
	}
}

// jsonFixtureExecutor is only a test model: it verifies the AI request and
// returns a named output using the same output protocol as real executors.
type jsonFixtureExecutor struct{}

func (jsonFixtureExecutor) Info() model.ExecutorInfo {
	return model.ExecutorInfo{ID: "json-fixture", Available: true}
}

func (jsonFixtureExecutor) Execute(_ context.Context, node model.Node, _, prompt string, _ func(string)) (map[string]string, int, error) {
	instructions, raw, ok := strings.Cut(prompt, "\n\nFlowline JSON input (data for these instructions):\n")
	if !ok || instructions != "Review hello from CMD\n" {
		return nil, 1, fmt.Errorf("missing instructions or automatic input: %q", prompt)
	}
	var input struct {
		Data         map[string]string
		Predecessors map[string]map[string]string
	}
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		return nil, 1, err
	}
	if input.Data["text"] != "hello from CMD\n" || input.Predecessors["root"]["stdout"] != input.Data["text"] {
		return nil, 1, errors.New("CMD to AI transfer lost")
	}

	encoded, err := json.Marshal(map[string]string{node.Outputs[0].Name: "AI reviewed \"CMD\"\n$(touch injected)"})
	if err != nil {
		return nil, 1, err
	}
	outputs, err := model.ParseExecutorOutputs(node, string(encoded))

	return outputs, 0, err
}

func TestCMDAndAIForkJoinTransfers(t *testing.T) {
	engine, store, repo, p := engineFixture(t)
	engine.executors = map[string]model.Executor{"json-fixture": jsonFixtureExecutor{}}
	p.Nodes = []model.Node{
		commandNode("root", "printf root > root.txt\nprintf 'hello from CMD\\n'"),
		{ID: "ai", Type: "ai", Name: "Review", Executor: "json-fixture", Prompt: "Review {{root.stdout}}", InputJSON: inputText(`{"text":"{{root.stdout}}"}`), Outputs: []model.Output{{ID: "review-id", Name: "review"}}},
		commandNode("branch", "test -f root.txt\nprintf branch > branch.txt\nprintf branch"),
		commandNode("join", "test -f root.txt\ntest -f branch.txt\ncat"),
	}
	p.Nodes[3].InputJSON = inputText(`{"review":"{{review}}","branch":"{{branch.stdout}}","empty":"{{branch.stderr}}"}`)
	p.Edges = []model.Edge{{From: "root", To: "ai"}, {From: "root", To: "branch"}, {From: "ai", To: "join"}, {From: "branch", To: "join"}}

	started := startPipeline(t, engine, store, repo, p)
	result := waitRun(t, engine, started.ID)
	if result.Status != "success" {
		for id, node := range result.Nodes {
			t.Logf("%s: %+v", id, node)
		}
		t.Fatal(result.Status)
	}
	var joined struct{ Data map[string]string }
	if err := json.Unmarshal([]byte(result.Nodes["join"].Stdout), &joined); err != nil {
		t.Fatal(err)
	}
	if joined.Data["review"] != result.Nodes["ai"].Outputs["review"] || joined.Data["branch"] != "branch" {
		t.Fatalf("AI to CMD transfer lost: %+v", joined)
	}
	if empty, ok := joined.Data["empty"]; !ok || empty != "" {
		t.Fatal("empty stream was not bound")
	}
	if !strings.Contains(result.Nodes["join"].Diff, "branch.txt") || !strings.Contains(result.Nodes["join"].Diff, "root.txt") {
		t.Fatal("fork/join lost parent code changes")
	}
}

func TestCMDCancellationKillsDescendants(t *testing.T) {
	engine, store, repo, p := engineFixture(t)
	engine.executors = nil
	p.Nodes = []model.Node{commandNode("slow", "sleep 60 &\necho $! > child.pid\necho started\nwait"), commandNode("next", "touch should-not-exist")}
	p.Edges = []model.Edge{{From: "slow", To: "next"}}
	started := startPipeline(t, engine, store, repo, p)
	var pid int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		current, err := engine.Run(started.ID)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(current.Nodes["slow"].Logs, "started") {
			data, err := os.ReadFile(filepath.Join(current.Nodes["slow"].Worktree, "child.pid"))
			if err != nil {
				t.Fatal(err)
			}
			pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil {
				t.Fatal(err)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("command descendant never started")
	}

	result, err := engine.Cancel(started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "cancelled" || result.Nodes["slow"].Status != "cancelled" || result.Nodes["next"].Status != "cancelled" {
		t.Fatalf("cancellation lost: %+v", result)
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("command descendant survived cancellation")
}

func TestCMDRedactsResolvedInputAndBothStreams(t *testing.T) {
	t.Setenv("FLOWLINE_TEST_TOKEN", "sensitive-command-value")
	engine, store, repo, p := engineFixture(t)
	engine.executors = nil
	p.Nodes = []model.Node{commandNode("cmd", "printf '%s' \"$FLOWLINE_TEST_TOKEN\"\nprintf '%s' \"$FLOWLINE_TEST_TOKEN\" >&2\nexit 4")}
	p.Nodes[0].InputJSON = inputText(`{"value":"sensitive-command-value","password":"hidden","number":9007199254740993}`)

	started := startPipeline(t, engine, store, repo, p)
	result := waitRun(t, engine, started.ID)
	node := result.Nodes["cmd"]
	if node.Status != "error" || node.Stdout != "[REDACTED]" || node.Stderr != "[REDACTED]" || strings.Contains(node.Logs, "sensitive-command-value") {
		t.Fatalf("unredacted streams: %+v", node)
	}
	if !json.Valid(node.InputJSON) || strings.Contains(string(node.InputJSON), "hidden") || strings.Contains(string(node.InputJSON), "sensitive-command-value") || !strings.Contains(string(node.InputJSON), "9007199254740993") {
		t.Fatalf("redaction corrupted input: %s", node.InputJSON)
	}
}

func TestLegacyRoleNodesKeepPromptOnlyExecution(t *testing.T) {
	engine, store, repo, p := engineFixture(t)
	for i, kind := range []string{"", "develop", "review", "test", "publish", "prompt"} {
		id := fmt.Sprintf("legacy-%d", i)
		node := processNode(id, "printf legacy", "result")
		node.Type = kind
		p.Nodes = append(p.Nodes, node)
	}

	started := startPipeline(t, engine, store, repo, p)
	result := waitRun(t, engine, started.ID)
	if result.Status != "success" {
		t.Fatal(result.Status)
	}
	for _, node := range result.Nodes {
		if node.Prompt != "printf legacy" || len(node.InputJSON) != 0 || node.Outputs["result"] != "legacy" {
			t.Fatalf("legacy semantics changed: %+v", node)
		}
	}
}

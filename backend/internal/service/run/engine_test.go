package run

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flowline/internal/cli/process"
	"flowline/internal/model"
	"flowline/internal/repository/sqlite"
	"flowline/internal/testutil"
)

type integrationExecutor struct{}

func (integrationExecutor) Info() model.ExecutorInfo {
	return model.ExecutorInfo{ID: "test-process", Name: "Integration process", Available: true}
}

func (integrationExecutor) Execute(ctx context.Context, node model.Node, cwd, prompt string, log func(string)) (map[string]string, int, error) {
	out, code, err := process.RunProcess(ctx, []string{"/bin/sh", "-c", prompt}, cwd, "", log)
	if err != nil {
		return nil, code, err
	}

	return map[string]string{node.Outputs[0].Name: strings.TrimSpace(out)}, code, nil
}

func engineFixture(t *testing.T) (*Engine, *sqlite.Store, model.Repository, model.Pipeline) {
	t.Helper()
	repo, _ := testutil.Repository(t)
	repo.ID = "repo"
	data := t.TempDir()
	store, err := sqlite.OpenStore(filepath.Join(data, "flowline.db"))
	if err != nil {
		t.Fatal(err)
	}

	if err := store.SaveRepository(repo); err != nil {
		t.Fatal(err)
	}

	engine := NewEngine(store, data, filepath.Join(data, "settings.json"), []model.Executor{integrationExecutor{}}, nil)
	t.Cleanup(func() { engine.Close(); _ = store.Close() })
	p := model.Pipeline{ID: "pipeline", Name: "Integration", Viewport: model.Viewport{Zoom: 1}, Nodes: []model.Node{}, Edges: []model.Edge{}}

	return engine, store, repo, p
}

func processNode(id, prompt, output string) model.Node {
	return model.Node{ID: id, Type: "prompt", Name: id, Executor: "test-process", Prompt: prompt, Outputs: []model.Output{{ID: "out-" + id, Name: output}}, Inputs: map[string]model.Source{}}
}

func waitRun(t *testing.T, engine *Engine, id string) model.Run {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		run, err := engine.Run(id)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status != "running" && run.Status != "waiting" {
			return run
		}
		time.Sleep(15 * time.Millisecond)
	}

	t.Fatal("run did not finish")
	return model.Run{}
}

func TestEngineRealProcessForkJoinAndSnapshot(t *testing.T) {
	engine, store, repo, p := engineFixture(t)
	if err := os.WriteFile(filepath.Join(repo.Path, "base.txt"), []byte("user changes\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p.Nodes = []model.Node{
		processNode("dev", "printf developed > feature.txt; printf 'changes'", "changes"),
		processNode("left", "test -f feature.txt; sleep 0.4; printf left > left.txt; printf 'reviewed {{changes}}'", "review"),
		processNode("right", "test -f feature.txt; sleep 0.4; printf right > right.txt; printf 'tested {{changes}}'", "tests"),
		processNode("join", "test -f left.txt && test -f right.txt; printf '{{review}} + {{tests}}'", "summary"),
	}
	p.Edges = []model.Edge{{From: "dev", To: "left"}, {From: "dev", To: "right"}, {From: "left", To: "join"}, {From: "right", To: "join"}}
	if err := store.SavePipeline(p); err != nil {
		t.Fatal(err)
	}

	started, err := engine.Start(context.Background(), p.ID, repo.ID, "main", 0)
	if err != nil {
		t.Fatal(err)
	}

	p.Nodes[0].Prompt = "exit 99"
	if err := store.SavePipeline(p); err != nil {
		t.Fatal(err)
	}

	result := waitRun(t, engine, started.ID)
	if result.Status != "success" {
		t.Fatalf("status %s: %+v", result.Status, result.Nodes)
	}
	if got := result.Nodes["join"].Outputs["summary"]; got != "reviewed changes + tested changes" {
		t.Fatalf("join result: %q", got)
	}
	if result.Snapshot.Nodes[0].Prompt == "exit 99" {
		t.Fatal("snapshot was edited")
	}
	leftStart, _ := time.Parse(time.RFC3339Nano, result.Nodes["left"].StartedAt)
	rightStart, _ := time.Parse(time.RFC3339Nano, result.Nodes["right"].StartedAt)
	leftEnd, _ := time.Parse(time.RFC3339Nano, result.Nodes["left"].FinishedAt)
	rightEnd, _ := time.Parse(time.RFC3339Nano, result.Nodes["right"].FinishedAt)
	if !leftStart.Before(rightEnd) || !rightStart.Before(leftEnd) {
		t.Fatal("branches did not execute concurrently")
	}
	if !strings.Contains(result.Nodes["join"].Diff, "feature.txt") || !strings.Contains(result.Nodes["join"].Diff, "left.txt") {
		t.Fatal("joined code diff missing")
	}
	data, _ := os.ReadFile(filepath.Join(repo.Path, "base.txt"))
	if string(data) != "user changes\n" {
		t.Fatal("source working tree changed")
	}
}

func TestEngineFailureSkipsDependencyButRunsIndependentBranch(t *testing.T) {
	engine, store, repo, p := engineFixture(t)
	p.Nodes = []model.Node{processNode("bad", "printf 'explicit failure' >&2; exit 7", "bad"), processNode("dependent", "touch should-not-exist", "child"), processNode("independent", "printf ok", "ok")}
	p.Edges = []model.Edge{{From: "bad", To: "dependent"}}
	if err := store.SavePipeline(p); err != nil {
		t.Fatal(err)
	}

	run, err := engine.Start(context.Background(), p.ID, repo.ID, "main", 0)
	if err != nil {
		t.Fatal(err)
	}
	result := waitRun(t, engine, run.ID)
	if result.Status != "error" || result.Nodes["bad"].Status != "error" || result.Nodes["dependent"].Status != "skipped" || result.Nodes["independent"].Status != "success" {
		t.Fatalf("unexpected statuses: %+v", result)
	}
	if !strings.Contains(result.Nodes["bad"].Logs, "explicit failure") || *result.Nodes["bad"].ExitCode != 7 {
		t.Fatal("actual executor failure was lost")
	}
}

func TestEngineCancellation(t *testing.T) {
	engine, store, repo, p := engineFixture(t)
	p.Nodes = []model.Node{processNode("slow", "echo started; sleep 30", "slow"), processNode("next", "printf next", "next")}
	p.Edges = []model.Edge{{From: "slow", To: "next"}}
	if err := store.SavePipeline(p); err != nil {
		t.Fatal(err)
	}

	run, err := engine.Start(context.Background(), p.ID, repo.ID, "main", 0)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		current, _ := engine.Run(run.ID)
		if strings.Contains(current.Nodes["slow"].Logs, "started") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	result, err := engine.Cancel(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "cancelled" || result.Nodes["slow"].Status != "cancelled" || result.Nodes["next"].Status != "cancelled" {
		t.Fatalf("cancellation not persisted: %+v", result)
	}
}

func TestEngineRejectsInvalidGraphAndMissingExecutor(t *testing.T) {
	engine, store, repo, p := engineFixture(t)
	p.Nodes = []model.Node{processNode("node", "echo {{missing}}", "out")}
	if err := store.SavePipeline(p); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Start(context.Background(), p.ID, repo.ID, "main", 0); err == nil {
		t.Fatal("missing input accepted")
	}

	p.Nodes[0].Prompt = "echo ok"
	p.Nodes[0].Executor = "missing"
	if err := store.SavePipeline(p); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Start(context.Background(), p.ID, repo.ID, "main", 0); err == nil || errors.Is(err, context.Canceled) {
		t.Fatal("missing executor accepted")
	}
}

type replacedExecutor struct{ integrationExecutor }

func (replacedExecutor) Execute(context.Context, model.Node, string, string, func(string)) (map[string]string, int, error) {
	return nil, 9, errors.New("replacement must not affect active run")
}

func TestEngineFreezesExecutorDefinitions(t *testing.T) {
	engine, store, repo, p := engineFixture(t)
	p.Nodes = []model.Node{processNode("first", "sleep 0.2; printf one", "one"), processNode("second", "printf '{{one}}-two'", "two")}
	p.Edges = []model.Edge{{From: "first", To: "second"}}
	if err := store.SavePipeline(p); err != nil {
		t.Fatal(err)
	}
	engine.configs = []model.ExecutorConfig{{ID: "test-process", Name: "Original", Kind: "codex", Command: "fixture"}}

	run, err := engine.Start(context.Background(), p.ID, repo.ID, "main", 0)
	if err != nil {
		t.Fatal(err)
	}
	engine.mu.Lock()
	engine.executors["test-process"] = replacedExecutor{}
	engine.configs[0].Name = "Changed"
	engine.mu.Unlock()
	result := waitRun(t, engine, run.ID)

	if result.Status != "success" || result.Nodes["second"].Outputs["two"] != "one-two" || result.ExecutorConfigs[0].Name != "Original" {
		t.Fatalf("active executor changed: %+v", result)
	}
	result.ExecutorConfigs[0].Name = "tampered"
	if err := store.SaveRun(result); err == nil {
		t.Fatal("historical executor definition was changed")
	}
}

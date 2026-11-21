package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"flowline/internal/model"
	"flowline/internal/testutil"
)

func testRun() model.Run {
	p := testutil.Pipeline()
	return model.Run{
		ID: "run-1", PipelineID: p.ID, PipelineName: p.Name, Status: "running", CreatedAt: "2026-11-21T10:00:00Z",
		Snapshot: p, Context: model.GitContext{Repository: model.Repository{ID: "repo-1", Path: "/tmp/repo", Name: "Repo"}, Branch: "feature", SHA: "abc"},
		Nodes: map[string]*model.NodeRun{
			"dev":    {NodeID: "dev", Status: "running", Inputs: map[string]string{"branch": "feature"}},
			"review": {NodeID: "review", Status: "waiting"},
		},
	}
}

func openTestStore(t *testing.T, path string) *Store {
	t.Helper()
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})

	return s
}

func TestStorePersistsGraphContextAndOutputsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "flowline.sqlite")
	s := openTestStore(t, path)
	p := testutil.Pipeline()
	p.Nodes[0].X, p.Nodes[0].Y = 123, 456
	p.Nodes[0].Prompt = "quoted ' text\x00Русский текст {{ branch }}"
	p.Viewport = model.Viewport{X: -15, Y: 25, Zoom: 0.75}
	p.UpdatedAt = "2026-11-21T10:00:00Z"
	repository := model.Repository{ID: "repo-1", Name: "Repo", Path: "/tmp/repo with ' quotes", Remote: "git@github.com:owner/repo.git"}
	run := testRun()
	run.Snapshot = p
	run.Context.Repository = repository
	run.Context.PR = &model.PullRequest{Number: 42, Head: "feature", Base: "main", HeadSHA: "abc", BaseSHA: "def"}
	run.Status = "success"
	run.Nodes["dev"].Status = "success"
	run.Nodes["dev"].Outputs = map[string]string{"changes": "a real result\x00終"}
	run.Nodes["dev"].Logs = "a real log"
	run.Nodes["review"].Status = "success"

	if err := s.SaveRepository(repository); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePipeline(p); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveRun(run); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTestStore(t, path)

	storedRepository, err := s.Repository(repository.ID)
	if err != nil || !reflect.DeepEqual(storedRepository, repository) {
		t.Fatalf("repository=%+v err=%v", storedRepository, err)
	}
	storedPipeline, err := s.Pipeline(p.ID)
	if err != nil || !reflect.DeepEqual(storedPipeline, p) {
		t.Fatalf("pipeline=%+v err=%v", storedPipeline, err)
	}
	storedRun, err := s.Run(run.ID)
	if err != nil || !reflect.DeepEqual(storedRun, run) {
		t.Fatalf("run=%+v err=%v", storedRun, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("database permissions: info=%v err=%v", info, err)
	}
}

func TestStoreImmutableRunSnapshotAndContext(t *testing.T) {
	s := openTestStore(t, filepath.Join(t.TempDir(), "flowline.sqlite"))
	original := testRun()
	if err := s.SaveRun(original); err != nil {
		t.Fatal(err)
	}

	for _, mutation := range []struct {
		name  string
		apply func(*model.Run)
	}{
		{"snapshot prompt", func(r *model.Run) { r.Snapshot.Nodes[0].Prompt = "changed" }},
		{"git SHA", func(r *model.Run) { r.Context.SHA = "changed" }},
		{"branch", func(r *model.Run) { r.Context.Branch = "other" }},
		{"repository", func(r *model.Run) { r.Context.Repository.Path = "/different" }},
		{"pipeline name", func(r *model.Run) { r.PipelineName = "other" }},
		{"start time", func(r *model.Run) { r.CreatedAt = "other" }},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			changed, err := s.Run(original.ID)
			if err != nil {
				t.Fatal(err)
			}
			mutation.apply(&changed)

			if err := s.SaveRun(changed); err == nil {
				t.Fatal("immutable record accepted")
			}
			stored, err := s.Run(original.ID)
			if err != nil || !reflect.DeepEqual(stored, original) {
				t.Fatalf("failed update changed history: %+v %v", stored, err)
			}
		})
	}

	completed, err := s.Run(original.ID)
	if err != nil {
		t.Fatal(err)
	}
	completed.Status = "success"
	completed.Nodes["dev"].Status = "success"
	completed.Nodes["dev"].Outputs = map[string]string{"changes": "real output"}
	if err := s.SaveRun(completed); err != nil {
		t.Fatalf("legitimate result update failed: %v", err)
	}

	p := testutil.Pipeline()
	p.Nodes[0].Prompt = "Edited after execution"
	if err := s.SavePipeline(p); err != nil {
		t.Fatal(err)
	}
	stored, err := s.Run(original.ID)
	if err != nil || !reflect.DeepEqual(stored.Snapshot, original.Snapshot) {
		t.Fatalf("editing pipeline mutated run snapshot: %+v %v", stored.Snapshot, err)
	}
}

func TestStoreRecoversInterruptedRunsOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flowline.sqlite")
	s := openTestStore(t, path)
	active := testRun()
	active.Nodes["done"] = &model.NodeRun{NodeID: "done", Status: "success", Outputs: map[string]string{"result": "keep me"}}
	if err := s.SaveRun(active); err != nil {
		t.Fatal(err)
	}
	completed := testRun()
	completed.ID, completed.Status = "completed", "success"
	if err := s.SaveRun(completed); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTestStore(t, path)

	if err := s.RecoverRuns(); err != nil {
		t.Fatal(err)
	}

	stored, err := s.Run(active.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "interrupted" || stored.FinishedAt == "" || stored.Error == "" {
		t.Fatalf("run not recovered: %+v", stored)
	}
	if stored.Nodes["dev"].Status != "interrupted" || stored.Nodes["review"].Status != "skipped" {
		t.Fatal("wrong node recovery states")
	}
	if stored.Nodes["done"].Status != "success" || stored.Nodes["done"].Outputs["result"] != "keep me" {
		t.Fatal("completed output lost")
	}
	if !reflect.DeepEqual(stored.Context, active.Context) || !reflect.DeepEqual(stored.Snapshot, active.Snapshot) {
		t.Fatal("recovery changed snapshot")
	}
	storedCompleted, err := s.Run(completed.ID)
	if err != nil || !reflect.DeepEqual(storedCompleted, completed) {
		t.Fatalf("terminal run changed: %+v %v", storedCompleted, err)
	}
	if err := s.RecoverRuns(); err != nil {
		t.Fatal(err)
	}
	twice, err := s.Run(active.ID)
	if err != nil || !reflect.DeepEqual(twice, stored) {
		t.Fatalf("recovery not idempotent: %v", err)
	}
}

func TestStoreMigrationsAndWAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flowline.sqlite")
	s := openTestStore(t, path)
	rows, err := s.query("SELECT name FROM schema_migrations ORDER BY name")
	if err != nil || len(rows) != 3 {
		t.Fatalf("migrations=%v err=%v", rows, err)
	}
	mode, err := s.query("PRAGMA journal_mode")
	if err != nil || mode[0][0] != "wal" {
		t.Fatalf("journal=%v err=%v", mode, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTestStore(t, path)
	rows, err = s.query("SELECT name FROM schema_migrations ORDER BY name")
	if err != nil || len(rows) != 3 {
		t.Fatalf("migrations reran: %v %v", rows, err)
	}
}

func TestStoreUpgradeAppliesNewMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flowline.sqlite")
	s := openTestStore(t, path)
	if err := s.SavePipeline(testutil.Pipeline()); err != nil {
		t.Fatal(err)
	}
	if err := s.execScript("DROP TRIGGER runs_immutable_context; DELETE FROM schema_migrations WHERE name='002_immutable_runs.sql';"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s = openTestStore(t, path)

	rows, err := s.query("SELECT name FROM sqlite_master WHERE type='trigger' AND name='runs_immutable_context'")
	if err != nil || len(rows) != 1 {
		t.Fatalf("new migration missing: %v %v", rows, err)
	}
	if _, err := s.Pipeline("pipeline-1"); err != nil {
		t.Fatalf("migration lost existing data: %v", err)
	}
}

func TestStoreRejectsFutureSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flowline.sqlite")
	s := openTestStore(t, path)
	if err := s.exec("INSERT INTO schema_migrations VALUES (?, ?)", "999_future.sql", "now"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	future, err := OpenStore(path)
	if err == nil {
		future.Close()
		t.Fatal("opened database with unsupported future schema")
	}
}

func TestStoreMissingRecordsAndSQLParameters(t *testing.T) {
	s := openTestStore(t, ":memory:")
	if _, err := s.Repository("missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	if _, err := s.Pipeline("missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	if _, err := s.Run("missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}

	if err := s.SavePipeline(testutil.Pipeline()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pipeline("' OR 1=1; DROP TABLE pipelines;--"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("SQL input escaped query: %v", err)
	}
	pipelines, err := s.Pipelines()
	if err != nil || len(pipelines) != 1 {
		t.Fatal("SQL parameters corrupted database")
	}
}

func TestStoreConcurrentWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flowline.sqlite")
	a, b := openTestStore(t, path), openTestStore(t, path)
	var wg sync.WaitGroup
	failures := make(chan error, 40)
	for i := range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := testutil.Pipeline()
			p.ID = fmt.Sprintf("pipeline-%d", i)
			store := a
			if i%2 == 0 {
				store = b
			}
			failures <- store.SavePipeline(p)
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Error(err)
		}
	}

	pipelines, err := a.Pipelines()
	if err != nil || len(pipelines) != 40 {
		t.Fatalf("lost writes: count=%d err=%v", len(pipelines), err)
	}
}

func TestStoreDraftSafety(t *testing.T) {
	s := openTestStore(t, ":memory:")
	draft := model.Pipeline{ID: "draft", Name: "Draft", Nodes: []model.Node{}, Edges: []model.Edge{}}
	if err := s.SavePipeline(draft); err != nil {
		t.Fatal("empty draft should save", err)
	}

	for _, invalid := range []string{"../path", "x/y", "", strings.Repeat("a", 101)} {
		p := testutil.Pipeline()
		p.Nodes[0].ID = invalid
		if err := s.SavePipeline(p); err == nil {
			t.Fatalf("unsafe draft ID %q accepted", invalid)
		}
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pipelines(); err == nil {
		t.Fatal("closed database should return error")
	}
}

func TestStorePersistsExecutionFieldsWithoutMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flowline.sqlite")
	s := openTestStore(t, path)
	p := testutil.Pipeline()
	input := `{"message":"{{branch}}","quoted":"line\n\"value\""}`
	p.Nodes[0].Type, p.Nodes[0].InputJSON = "ai", &input
	p.Nodes[1].Type, p.Nodes[1].Command, p.Nodes[1].InputJSON = "cmd", "input=$(cat)\nprintf '%s' \"$input\"", &input
	p.Nodes[1].Outputs = []model.Output{{ID: "review-id", Name: "renamed", Source: "stdout"}}
	run := testRun()
	run.Snapshot = p
	run.Status = "success"
	run.Nodes["review"].InputJSON = []byte(`{"context":{},"predecessors":{},"inputs":{},"data":{"message":"main"}}`)
	run.Nodes["review"].Command = p.Nodes[1].Command
	run.Nodes["review"].Stdout, run.Nodes["review"].Stderr = "result", "warning"
	run.Nodes["review"].Outputs = map[string]string{"renamed": "result", "empty": ""}
	code := 0
	run.Nodes["review"].ExitCode = &code
	if err := s.SavePipeline(p); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveRun(run); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s = openTestStore(t, path)
	stored, err := s.Pipeline(p.ID)
	if err != nil || !reflect.DeepEqual(stored, p) {
		t.Fatalf("new pipeline fields lost: %+v %v", stored, err)
	}
	storedRun, err := s.Run(run.ID)
	if err != nil || !reflect.DeepEqual(storedRun, run) {
		t.Fatalf("new run fields lost: %+v %v", storedRun, err)
	}
	storedRun.Snapshot.Nodes[1].Command = "changed"
	if err := s.SaveRun(storedRun); err == nil {
		t.Fatal("CMD snapshot is mutable")
	}
	storedRun, err = s.Run(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	changed := "{}"
	storedRun.Snapshot.Nodes[0].InputJSON = &changed
	if err := s.SaveRun(storedRun); err == nil {
		t.Fatal("JSON snapshot is mutable")
	}
}

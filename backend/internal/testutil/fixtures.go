package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"flowline/internal/model"
)

func Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@localhost", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@localhost")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func Repository(t *testing.T) (model.Repository, string) {
	t.Helper()
	dir := t.TempDir()
	Git(t, dir, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "base.txt"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	Git(t, dir, "add", ".")
	Git(t, dir, "commit", "-m", "initial")
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	repo := model.Repository{Name: filepath.Base(canonical), Path: canonical}

	return repo, Git(t, dir, "rev-parse", "HEAD")
}

func Pipeline() model.Pipeline {
	return model.Pipeline{
		ID: "pipeline-1", Name: "Delivery", Viewport: model.Viewport{Zoom: 1},
		Nodes: []model.Node{
			{ID: "dev", Name: "Development", Executor: "test", Prompt: "Build {{branch}}", Outputs: []model.Output{{ID: "changes-id", Name: "changes"}}},
			{ID: "review", Name: "Review", Executor: "test", Prompt: "Review {{ changes }}", Outputs: []model.Output{{ID: "review-id", Name: "review"}}},
		},
		Edges: []model.Edge{{From: "dev", To: "review"}},
	}
}

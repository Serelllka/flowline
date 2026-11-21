package codex

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"flowline/internal/cli/process"
	"flowline/internal/model"
	"flowline/internal/testutil"
)

// Opt in only when local Codex authentication and outbound access are available.
func TestLiveCodex(t *testing.T) {
	if os.Getenv("FLOWLINE_LIVE_CODEX") != "1" {
		t.Skip("set FLOWLINE_LIVE_CODEX=1 to call the installed authenticated Codex CLI")
	}
	repo, _ := testutil.Repository(t)
	executor, err := New(model.ExecutorConfig{ID: "codex", Name: "Codex", Kind: "codex", Command: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	node := model.Node{ID: "live", Outputs: []model.Output{{ID: "result", Name: "result"}}}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	var logs strings.Builder
	outputs, code, err := executor.Execute(ctx, node, repo.Path, "Do not call tools or change files. Return the named output result with the exact string flowline-live-ok.", func(line string) {
		if logs.Len() < 8192 {
			logs.WriteString(process.Redact(line))
		}
	})
	if err != nil {
		t.Fatalf("actual Codex call failed (exit %d): %v\n%s", code, err, logs.String())
	}
	if outputs["result"] != "flowline-live-ok" {
		t.Fatalf("unexpected real response: %+v", outputs)
	}
}

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flowline/internal/cli/process"
	"flowline/internal/model"
	"flowline/internal/repository/sqlite"
	"flowline/internal/service/run"
	"flowline/internal/testutil"
)

func requestAPI(t *testing.T, handler http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(method, "http://127.0.0.1:4173"+path, bytes.NewReader(data))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Flowline-Request", "1")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	return w
}

func TestHTTPRealRepositorySaveRunAndHistoricalSnapshot(t *testing.T) {
	engine, store, repo, p := httpFixture(t)
	handler := NewServer(store, engine, nil, t.TempDir())
	p.Nodes = []model.Node{processNode("node", "printf actual-result", "result")}

	response := requestAPI(t, handler, "POST", "/api/repositories", map[string]string{"path": repo.Path})
	var existing model.Repository
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &existing) != nil || existing.ID != repo.ID {
		t.Fatal("repository added twice instead of reused:", response.Code, response.Body.String())
	}

	response = requestAPI(t, handler, "GET", "/api/bootstrap", nil)
	var bootstrap struct {
		Repositories []model.Repository `json:"repositories"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &bootstrap) != nil || len(bootstrap.Repositories) != 1 || bootstrap.Repositories[0].ID != repo.ID {
		t.Fatal("added repository missing from bootstrap:", response.Code, response.Body.String())
	}

	response = requestAPI(t, handler, "PUT", "/api/pipelines/"+p.ID, p)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	response = requestAPI(t, handler, "GET", "/api/repositories/"+repo.ID+"/context", nil)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"name":"main"`) {
		t.Fatal(response.Code, response.Body.String())
	}
	response = requestAPI(t, handler, "POST", "/api/runs", map[string]any{"pipelineId": p.ID, "repositoryId": repo.ID, "branch": "main"})
	if response.Code != 201 {
		t.Fatal(response.Code, response.Body.String())
	}
	var created model.Run
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	result := waitRun(t, engine, created.ID)
	if result.Status != "success" || result.Nodes["node"].Outputs["result"] != "actual-result" {
		t.Fatalf("actual process result missing: %+v", result)
	}

	p.Name = "New draft"
	p.Nodes[0].Prompt = "printf edited"
	response = requestAPI(t, handler, "PUT", "/api/pipelines/"+p.ID, p)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	response = requestAPI(t, handler, "GET", "/api/runs/"+result.ID, nil)
	if strings.Contains(response.Body.String(), "printf edited") || !strings.Contains(response.Body.String(), "printf actual-result") {
		t.Fatal("run snapshot changed after pipeline edit")
	}
}

func TestHTTPRejectsCrossOriginDNSRebindingAndUnsafeMutations(t *testing.T) {
	engine, store, _, _ := httpFixture(t)
	handler := NewServer(store, engine, nil, t.TempDir())
	for _, scenario := range []struct {
		name, host, origin, token string
	}{
		{"cross origin", "127.0.0.1:4173", "https://evil.example", "1"},
		{"DNS rebinding", "evil.example:4173", "", "1"},
		{"missing custom header", "127.0.0.1:4173", "", ""},
		{"different local port", "127.0.0.1:4173", "http://127.0.0.1:9000", "1"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "http://"+scenario.host+"/api/repositories", strings.NewReader(`{"path":"/tmp"}`))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", scenario.origin)
			r.Header.Set("X-Flowline-Request", scenario.token)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != 403 {
				t.Fatalf("got %d", w.Code)
			}
		})
	}
}

func TestHTTPServesOnlyExplicitFrontendAssets(t *testing.T) {
	engine, store, _, _ := httpFixture(t)
	assets := t.TempDir()
	if err := os.WriteFile(filepath.Join(assets, "index.html"), []byte("Flowline"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assets, "flowline.db"), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	handler := NewServer(store, engine, nil, assets)
	if response := requestAPI(t, handler, "GET", "/", nil); response.Code != 200 || response.Body.String() != "Flowline" {
		t.Fatal("index not served")
	}
	for _, path := range []string{"/flowline.db", "/api/missing", "/etc/passwd"} {
		if response := requestAPI(t, handler, "GET", path, nil); response.Code != 404 {
			t.Fatalf("unexpected asset %s: %d", path, response.Code)
		}
	}
}

func TestHTTPServesBundledFrontend(t *testing.T) {
	engine, store, _, _ := httpFixture(t)
	assets := filepath.Join("..", "..", "..", "..", "frontend")
	handler := NewServer(store, engine, nil, assets)
	for _, asset := range []string{"/", "/styles.css", "/icons.js", "/graph.js", "/executor-config.js", "/app.js"} {
		response := requestAPI(t, handler, "GET", asset, nil)
		if response.Code != http.StatusOK || response.Body.Len() == 0 {
			t.Fatalf("bundled asset %s unavailable: %d %s", asset, response.Code, response.Body.String())
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("bundled asset %s could retain stale UI after an update", asset)
		}
	}
}

// A real local subprocess fixture, confined to tests. Production never uses it.
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

func httpFixture(t *testing.T) (*run.Engine, *sqlite.Store, model.Repository, model.Pipeline) {
	t.Helper()
	repo, _ := testutil.Repository(t)
	data := t.TempDir()
	store, err := sqlite.OpenStore(filepath.Join(data, "flowline.db"))
	if err != nil {
		t.Fatal(err)
	}

	engine := run.NewEngine(store, data, filepath.Join(data, "settings.json"), []model.Executor{integrationExecutor{}}, nil)
	t.Cleanup(func() { engine.Close(); _ = store.Close() })
	handler := NewServer(store, engine, nil, t.TempDir())
	response := requestAPI(t, handler, "POST", "/api/repositories", map[string]string{"path": repo.Path})
	if response.Code != http.StatusCreated {
		t.Fatal("add real repository:", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &repo); err != nil {
		t.Fatal(err)
	}

	p := model.Pipeline{ID: "pipeline", Name: "Integration", Viewport: model.Viewport{Zoom: 1}, Nodes: []model.Node{}, Edges: []model.Edge{}}

	return engine, store, repo, p
}

func processNode(id, prompt, output string) model.Node {
	return model.Node{ID: id, Type: "prompt", Name: id, Executor: "test-process", Prompt: prompt, Outputs: []model.Output{{ID: "out-" + id, Name: output}}, Inputs: map[string]model.Source{}}
}

func waitRun(t *testing.T, engine *run.Engine, id string) model.Run {
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

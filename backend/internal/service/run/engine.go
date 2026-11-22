package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"flowline/internal/model"
	"flowline/internal/repository/git"
	"flowline/internal/repository/sqlite"
	"flowline/internal/service/graph"
)

type activeRun struct {
	run       model.Run
	executors map[string]model.Executor
	cancel    context.CancelFunc
	done      chan struct{}
}

type Engine struct {
	mu           sync.Mutex
	store        *sqlite.Store
	dataDir      string
	settingsPath string
	executors    map[string]model.Executor
	configs      []model.ExecutorConfig
	provider     model.PRProvider
	active       map[string]*activeRun
	parallel     int
	closing      bool
}

func NewEngine(store *sqlite.Store, dataDir, settingsPath string, executors []model.Executor, provider model.PRProvider) *Engine {
	registry := make(map[string]model.Executor, len(executors))
	for _, executor := range executors {
		registry[executor.Info().ID] = executor
	}

	return &Engine{store: store, dataDir: dataDir, settingsPath: settingsPath, executors: registry, provider: provider, active: make(map[string]*activeRun), parallel: 4}
}

func timestamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func (e *Engine) Runs() ([]model.Run, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	runs, err := e.store.Runs()
	if err != nil {
		return nil, err
	}
	for i, run := range runs {
		if active := e.active[run.ID]; active != nil {
			runs[i] = copyRun(active.run)
		}
	}
	return runs, nil
}

func (e *Engine) Start(ctx context.Context, pipelineID, repositoryID, branch string, prNumber int) (model.Run, error) {
	p, err := e.store.Pipeline(pipelineID)
	if err != nil {
		return model.Run{}, err
	}

	if issues := graph.ValidateGraph(p); len(issues) != 0 {
		return model.Run{}, fmt.Errorf("граф не готов к запуску: %s", strings.Join(issues, "; "))
	}

	e.mu.Lock()
	registry := make(map[string]model.Executor, len(e.executors))
	for id, executor := range e.executors {
		registry[id] = executor
	}
	configs := append([]model.ExecutorConfig{}, e.configs...)
	e.mu.Unlock()

	for _, node := range p.Nodes {
		if node.ExecutionType() == "cmd" {
			continue
		}

		executor := registry[node.Executor]
		if executor == nil || !executor.Info().Available {
			return model.Run{}, fmt.Errorf("%s: исполнитель %q недоступен", node.Name, node.Executor)
		}
	}

	repo, err := e.store.Repository(repositoryID)
	if err != nil {
		return model.Run{}, err
	}

	sha, err := git.ResolveBranch(ctx, repo, branch)
	if err != nil {
		return model.Run{}, err
	}

	gitContext := model.GitContext{Repository: repo, Branch: branch, SHA: sha}
	if prNumber != 0 {
		if e.provider == nil {
			return model.Run{}, errors.New("Git-провайдер ещё не настроен")
		}

		pr, err := e.provider.Validate(ctx, repo, branch, sha, prNumber)
		if err != nil {
			return model.Run{}, err
		}

		gitContext.PR = pr
	}

	run := model.Run{ID: model.NewID(), PipelineID: p.ID, PipelineName: p.Name, Status: "running", CreatedAt: timestamp(), Snapshot: p, Context: gitContext, Nodes: make(map[string]*model.NodeRun), ExecutorConfigs: configs}
	for _, node := range p.Nodes {
		run.Nodes[node.ID] = &model.NodeRun{NodeID: node.ID, Status: "waiting", Inputs: map[string]string{}, Outputs: map[string]string{}}
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closing {
		return model.Run{}, errors.New("приложение завершает работу")
	}

	if err := e.store.SaveRun(run); err != nil {
		return model.Run{}, err
	}

	runCtx, cancel := context.WithCancel(context.Background())
	active := &activeRun{run: run, executors: registry, cancel: cancel, done: make(chan struct{})}
	e.active[run.ID] = active
	go e.schedule(runCtx, active)

	return copyRun(run), nil
}

func copyRun(run model.Run) model.Run {
	data, _ := json.Marshal(run)
	var result model.Run
	_ = json.Unmarshal(data, &result)

	return result
}

func (e *Engine) Run(id string) (model.Run, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if active := e.active[id]; active != nil {
		return copyRun(active.run), nil
	}

	return e.store.Run(id)
}

func (e *Engine) Cancel(id string) (model.Run, error) {
	e.mu.Lock()
	active := e.active[id]
	if active != nil {
		active.cancel()
	}
	e.mu.Unlock()

	if active != nil {

		<-active.done
	}

	return e.Run(id)
}

func (e *Engine) Close() {
	e.mu.Lock()
	e.closing = true
	pending := make([]*activeRun, 0, len(e.active))
	for _, active := range e.active {
		active.cancel()
		pending = append(pending, active)
	}
	e.mu.Unlock()

	for _, active := range pending {
		<-active.done
	}
}

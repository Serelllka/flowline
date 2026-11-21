package run

import (
	"os"
	"sort"

	"flowline/internal/cli/claude"
	"flowline/internal/cli/codex"
	"flowline/internal/cli/process"
	"flowline/internal/client/chatcompletion"
	"flowline/internal/model"
	"flowline/internal/repository/settings"
)

func (e *Engine) ExecutorInfos() []model.ExecutorInfo {
	e.mu.Lock()
	defer e.mu.Unlock()

	infos := make([]model.ExecutorInfo, 0, len(e.executors))
	for _, executor := range e.executors {
		infos = append(infos, executor.Info())
	}

	sort.Slice(infos, func(i, j int) bool { return infos[i].ID < infos[j].ID })
	return infos
}

func BuildExecutors(configs []model.ExecutorConfig) ([]model.Executor, error) {
	if err := model.ValidateExecutorConfigs(configs); err != nil {
		return nil, err
	}
	executors := make([]model.Executor, 0, len(configs))
	for _, config := range configs {
		if config.APIKeyEnv != "" {
			process.RegisterSecret(os.Getenv(config.APIKeyEnv))
		}
		var executor model.Executor
		var err error
		if config.Kind == "openai-compatible" {
			executor, err = chatcompletion.New(config)
		} else {
			switch config.Kind {
			case "codex":
				executor, err = codex.New(config)
			case "claude":
				executor, err = claude.New(config)
			}
		}
		if err != nil {
			return nil, err
		}
		executors = append(executors, executor)
	}
	return executors, nil
}

func (e *Engine) ConfigureExecutors(configs []model.ExecutorConfig) error {
	return e.configureExecutors(configs, true)
}

// InitializeExecutors loads startup profiles without rewriting user settings.
func (e *Engine) InitializeExecutors(configs []model.ExecutorConfig) error {
	return e.configureExecutors(configs, false)
}

func (e *Engine) configureExecutors(configs []model.ExecutorConfig, persist bool) error {
	executors, err := BuildExecutors(configs)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if persist {
		if err := settings.SaveExecutors(e.settingsPath, configs); err != nil {
			return err
		}
	}
	e.executors = make(map[string]model.Executor, len(executors))
	for _, executor := range executors {
		e.executors[executor.Info().ID] = executor
	}
	e.configs = append([]model.ExecutorConfig{}, configs...)
	return nil
}

func (e *Engine) ExecutorConfigs() []model.ExecutorConfig {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]model.ExecutorConfig{}, e.configs...)
}

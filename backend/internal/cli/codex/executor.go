package codex

import (
	"errors"
	"os/exec"

	"flowline/internal/model"
)

type Executor struct{ config model.ExecutorConfig }

func New(config model.ExecutorConfig) (model.Executor, error) {
	if err := model.ValidateExecutorConfigs([]model.ExecutorConfig{config}); err != nil {
		return nil, err
	}
	if config.Kind != "codex" {
		return nil, errors.New("ожидался исполнитель codex")
	}
	if config.Command == "" {
		config.Command = "codex"
	}
	return &Executor{config: config}, nil
}

func (e *Executor) Info() model.ExecutorInfo {
	info := model.ExecutorInfo{ID: e.config.ID, Name: e.config.Name}
	if _, err := exec.LookPath(e.config.Command); err != nil {
		info.Detail = "Команда CLI не найдена или не исполняется: " + e.config.Command
		return info
	}
	info.Available = true
	info.Detail = "Используется сохранённая авторизация CLI; её доступность проверяется при запуске"
	return info
}

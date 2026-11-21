package executorconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"flowline/internal/model"
)

func LoadExecutorConfigs(path string) ([]model.ExecutorConfig, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return model.DefaultExecutorConfigs(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("открытие конфигурации исполнителей: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > 1024*1024 {
		return nil, errors.New("конфигурация превышает 1 MiB")
	}
	decoder := json.NewDecoder(io.LimitReader(file, 1024*1024+1))
	decoder.DisallowUnknownFields()
	var configs []model.ExecutorConfig
	if err := decoder.Decode(&configs); err != nil {
		return nil, fmt.Errorf("конфигурация исполнителей: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("конфигурация должна содержать один JSON-массив")
	}
	if err := model.ValidateExecutorConfigs(configs); err != nil {
		return nil, err
	}
	if configs == nil {
		configs = []model.ExecutorConfig{}
	}
	return configs, nil
}

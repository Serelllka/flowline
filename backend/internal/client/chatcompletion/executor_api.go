package chatcompletion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"flowline/internal/cli/process"
	"flowline/internal/model"
)

// Executor uses the chat-completions function-tool protocol. Credentials are
// resolved only on the backend and never form part of a persisted configuration.
type Executor struct {
	config model.ExecutorConfig
	client *http.Client
}

func New(config model.ExecutorConfig) (model.Executor, error) {
	if err := model.ValidateExecutorConfigs([]model.ExecutorConfig{config}); err != nil {
		return nil, err
	}

	return &Executor{config: config, client: &http.Client{Timeout: 3 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (e *Executor) Info() model.ExecutorInfo {
	available := os.Getenv(e.config.APIKeyEnv) != ""
	detail := "API · файлы рабочего дерева и изолированные проверки (macOS)"
	if !available {
		detail = "Задайте " + e.config.APIKeyEnv + " в окружении backend и перезапустите Flowline"
	}

	return model.ExecutorInfo{ID: e.config.ID, Name: e.config.Name, Available: available, Detail: detail}
}

func (e *Executor) Execute(ctx context.Context, node model.Node, cwd, prompt string, log func(string)) (map[string]string, int, error) {
	key := os.Getenv(e.config.APIKeyEnv)
	if key == "" {
		return nil, -1, fmt.Errorf("переменная %s не задана", e.config.APIKeyEnv)
	}
	process.RegisterSecret(key)
	failedChecks := make(map[string]string)

	var outputNames []string
	for _, output := range node.Outputs {
		outputNames = append(outputNames, output.Name)
	}
	names, _ := json.Marshal(outputNames)
	system := "You are a development pipeline executor in an isolated Git worktree. Use the tools to inspect/edit code and run checks. Follow repository AGENTS.md when present. Never publish, push, change remote PRs, merge remote branches or create releases. Never read or disclose secrets. A tool failure is a real failure; never claim unexecuted checks passed. Your final response must be a JSON object with exactly these named output keys and string values: " + string(names) + ". Do not include markdown fences."
	messages := []apiMessage{{Role: "system", Content: system}, {Role: "user", Content: prompt}}
	u, _ := url.Parse(e.config.BaseURL)
	for round := 0; round < 30; round++ {
		if err := ctx.Err(); err != nil {
			return nil, -1, err
		}

		payload := map[string]any{"model": e.config.Model, "messages": messages, "tools": apiTools(), "stream": false}
		if strings.EqualFold(u.Hostname(), "api.deepseek.com") {
			payload["thinking"] = map[string]string{"type": "disabled"}
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return nil, -1, err
		}
		if len(body) > 4<<20 {
			return nil, -1, errors.New("контекст API превысил 4 MiB; разбейте задачу на блоки")
		}

		request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(e.config.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			return nil, -1, err
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+key)
		response, err := e.client.Do(request)
		if err != nil {
			return nil, -1, fmt.Errorf("запрос к API: %w", err)
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
		response.Body.Close()
		if readErr != nil {
			return nil, -1, readErr
		}
		if len(data) > 2<<20 {
			return nil, -1, errors.New("ответ API превысил 2 MiB")
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return nil, response.StatusCode, fmt.Errorf("API HTTP %d: %s", response.StatusCode, process.Redact(string(data)))
		}

		var result struct {
			Choices []struct {
				Message      apiMessage `json:"message"`
				FinishReason string     `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(data, &result); err != nil || len(result.Choices) != 1 {
			return nil, -1, errors.New("API не вернул единственный корректный ответ choices")
		}

		choice := result.Choices[0]
		if choice.FinishReason == "stop" && len(choice.Message.ToolCalls) == 0 {
			if len(failedChecks) != 0 {
				return nil, 1, errors.New("проверка завершилась ошибкой и не была успешно повторена: " + strings.Join(mapValues(failedChecks), "; "))
			}
			content, ok := choice.Message.Content.(string)
			if !ok || strings.TrimSpace(content) == "" {
				return nil, -1, errors.New("API вернул пустой результат")
			}
			outputs, err := model.ParseExecutorOutputs(node, content)
			if err != nil {
				return nil, -1, err
			}
			if log != nil {
				log("API: получен финальный ответ\n")
			}

			return redactOutputs(outputs), 0, nil
		}
		if choice.FinishReason != "tool_calls" || len(choice.Message.ToolCalls) == 0 || len(choice.Message.ToolCalls) > 20 {
			return nil, -1, fmt.Errorf("API не завершил блок: finish_reason=%s", choice.FinishReason)
		}

		messages = append(messages, choice.Message)
		seen := make(map[string]bool)
		for _, call := range choice.Message.ToolCalls {
			if call.ID == "" || seen[call.ID] || call.Type != "function" {
				return nil, -1, errors.New("API вернул неверный вызов инструмента")
			}
			seen[call.ID] = true
			if log != nil {
				log("API: " + call.Function.Name + "\n")
			}
			output, toolErr := executeAPITool(ctx, cwd, call, log)
			if call.Function.Name == "run_check" {
				identity := checkIdentity(call.Function.Arguments)
				if toolErr != nil {
					failedChecks[identity] = process.Redact(toolErr.Error())
				} else {
					delete(failedChecks, identity)
				}
			}
			if toolErr != nil {
				output = "ERROR: " + toolErr.Error()
			}
			output = process.Redact(output)
			if log != nil && toolErr != nil {
				log(output + "\n")
			}
			messages = append(messages, apiMessage{Role: "tool", ToolCallID: call.ID, Content: output})
		}
	}

	return nil, -1, errors.New("API превысил лимит 30 шагов; результат не считается успешным")
}

func mapValues(values map[string]string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}

func redactOutputs(values map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = process.Redact(value)
	}

	return result
}

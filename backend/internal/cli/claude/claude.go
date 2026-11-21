package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"flowline/internal/cli/process"
	"flowline/internal/model"
)

func (e *Executor) Execute(ctx context.Context, node model.Node, cwd, prompt string, log func(string)) (map[string]string, int, error) {
	schema, err := model.ExecutorOutputSchema(node)
	if err != nil {
		return nil, -1, err
	}

	// acceptEdits applies to edits in this isolated worktree. Tool calls needing
	// further approval are denied; the user's existing allow rules still apply.
	argv := []string{e.config.Command, "-p", "--output-format", "json", "--no-session-persistence", "--permission-mode", "acceptEdits", "--permission-prompts", "none", "--json-schema", schema}
	if e.config.Model != "" {
		argv = append(argv, "--model", e.config.Model)
	}
	text, code, err := process.RunProcess(ctx, argv, cwd, prompt, log)
	if err != nil {
		return nil, code, fmt.Errorf("Claude Code: %w", err)
	}
	var result struct {
		Type              string            `json:"type"`
		Subtype           string            `json:"subtype"`
		IsError           bool              `json:"is_error"`
		Result            string            `json:"result"`
		StructuredOutput  json.RawMessage   `json:"structured_output"`
		PermissionDenials []json.RawMessage `json:"permission_denials"`
	}
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		return nil, code, errors.New("Claude Code вернул некорректный JSON-результат")
	}
	if result.IsError || (result.Subtype != "" && result.Subtype != "success") {
		return nil, code, fmt.Errorf("Claude Code: %s %s", result.Subtype, process.Redact(result.Result))
	}
	if len(result.PermissionDenials) > 0 {
		return nil, code, errors.New("Claude Code: необходимые действия отклонены политикой разрешений CLI; настройте разрешённые инструменты в CLI")
	}
	if result.Type != "result" || len(result.StructuredOutput) == 0 || string(result.StructuredOutput) == "null" {
		return nil, code, errors.New("Claude Code не вернул структурированный финальный результат")
	}
	outputs, err := model.ParseNamedOutputs(node, string(result.StructuredOutput), false)
	return outputs, code, err
}

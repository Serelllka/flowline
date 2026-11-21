package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"flowline/internal/cli/process"
	"flowline/internal/model"
)

const resultSizeLimit = 4 << 20

func (e *Executor) Execute(ctx context.Context, node model.Node, cwd, prompt string, log func(string)) (map[string]string, int, error) {
	schema, err := model.ExecutorOutputSchema(node)
	if err != nil {
		return nil, -1, err
	}

	dir, err := os.MkdirTemp("", "flowline-codex-")
	if err != nil {
		return nil, -1, err
	}
	defer os.RemoveAll(dir)
	schemaPath, outputPath := filepath.Join(dir, "schema.json"), filepath.Join(dir, "result.json")
	if err := os.WriteFile(schemaPath, []byte(schema), 0600); err != nil {
		return nil, -1, err
	}
	// Owned foreground process, retained workspace sandbox and denied escalations.
	// No shared daemon and no approval/sandbox bypass flags.
	argv := []string{e.config.Command, "--no-daemon", "-a", "never", "exec", "--sandbox", "workspace-write", "--ephemeral", "--json", "--color", "never", "--output-schema", schemaPath, "--output-last-message", outputPath}
	if e.config.Model != "" {
		argv = append(argv, "--model", e.config.Model)
	}
	argv = append(argv, "-")
	transcript, code, err := process.RunProcess(ctx, argv, cwd, prompt, log)
	if err != nil {
		return nil, code, fmt.Errorf("Codex CLI: %w", err)
	}
	if err := validateCodexEvents(transcript); err != nil {
		return nil, code, err
	}
	contents, err := readCLIResult(outputPath)
	if err != nil {
		return nil, code, fmt.Errorf("Codex не вернул финальный результат: %w", err)
	}
	outputs, err := model.ParseNamedOutputs(node, contents, false)
	return outputs, code, err
}

func validateCodexEvents(transcript string) error {
	completed := false
	for _, line := range strings.Split(transcript, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event struct {
			Type    string          `json:"type"`
			Error   json.RawMessage `json:"error"`
			Message string          `json:"message"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return errors.New("Codex вернул некорректный поток JSON-событий")
		}
		switch event.Type {
		case "error", "turn.failed":
			return fmt.Errorf("Codex CLI сообщил об ошибке: %s", process.Redact(strings.TrimSpace(event.Message+" "+string(event.Error))))
		case "turn.completed":
			completed = true
		}
	}
	if !completed {
		return errors.New("Codex не подтвердил завершение шага")
	}
	return nil
}

func readCLIResult(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, resultSizeLimit+1))
	if err != nil {
		return "", err
	}
	if len(contents) > resultSizeLimit {
		return "", errors.New("финальный результат превышает 4 MiB")
	}
	return string(contents), nil
}

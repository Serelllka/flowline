package graph

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"flowline/internal/model"
)

// transformInputJSON visits string values, never keys or serialized JSON text.
// Numbers retain their original precision. A missing field defaults to {}.
func transformInputJSON(input *string, transform func(string) (string, error)) (map[string]any, error) {
	text := "{}"
	if input != nil {
		text = *input
	}

	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	var data map[string]any
	if err := decoder.Decode(&data); err != nil {
		return nil, fmt.Errorf("inputJson: требуется JSON-объект: %w", err)
	}
	if data == nil {
		return nil, errors.New("inputJson: требуется JSON-объект, не null")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("inputJson: лишние данные после JSON-объекта")
	}

	var visit func(any) (any, error)
	visit = func(value any) (any, error) {
		switch value := value.(type) {
		case string:
			return transform(value)
		case []any:
			for i, item := range value {
				updated, err := visit(item)
				if err != nil {
					return nil, err
				}
				value[i] = updated
			}
		case map[string]any:
			for key, item := range value {
				updated, err := visit(item)
				if err != nil {
					return nil, err
				}
				value[key] = updated
			}
		}

		return value, nil
	}
	if _, err := visit(data); err != nil {
		return nil, fmt.Errorf("inputJson: %w", err)
	}

	return data, nil
}

func nodeTokens(n model.Node) ([]string, error) {
	var tokens []string
	if n.ExecutionType() == "ai" {
		var err error
		tokens, err = promptTokens(n.Prompt)
		if err != nil {
			return nil, err
		}
	}

	if n.UsesJSONInput() {
		_, err := transformInputJSON(n.InputJSON, func(value string) (string, error) {
			found, err := promptTokens(value)
			tokens = append(tokens, found...)

			return value, err
		})
		if err != nil {
			return nil, err
		}
	}

	return tokens, nil
}

// ResolveInputJSON builds the envelope sent to AI instructions or CMD stdin.
// Predecessors are keyed by node ID so duplicate output names remain distinct.
func ResolveInputJSON(p model.Pipeline, n model.Node, gitContext model.GitContext, runs map[string]*model.NodeRun, inputs map[string]string) (json.RawMessage, error) {
	data, err := transformInputJSON(n.InputJSON, func(value string) (string, error) {
		return RenderPrompt(value, inputs)
	})
	if err != nil {
		return nil, err
	}

	predecessors := map[string]map[string]string{}
	for _, edge := range p.Edges {
		if edge.To != n.ID {
			continue
		}

		previous := runs[edge.From]
		if previous == nil || previous.Status != "success" {
			return nil, fmt.Errorf("блок %q ещё не завершён успешно", edge.From)
		}
		predecessors[edge.From] = previous.Outputs
	}

	return json.MarshalIndent(struct {
		Context      model.GitContext             `json:"context"`
		Predecessors map[string]map[string]string `json:"predecessors"`
		Inputs       map[string]string            `json:"inputs"`
		Data         map[string]any               `json:"data"`
	}{gitContext, predecessors, inputs, data}, "", "  ")
}

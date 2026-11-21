package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

func ExecutorOutputSchema(node Node) (string, error) {
	if len(node.Outputs) == 0 {
		return "", errors.New("у блока нет объявленных результатов")
	}
	properties := map[string]any{}
	required := make([]string, 0, len(node.Outputs))
	for _, output := range node.Outputs {
		if !ValidIdentifier(output.Name) {
			return "", errors.New("некорректное имя результата")
		}
		if _, exists := properties[output.Name]; exists {
			return "", errors.New("повторяющееся имя результата")
		}
		properties[output.Name] = map[string]string{"type": "string"}
		required = append(required, output.Name)
	}
	data, err := json.Marshal(map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false})
	return string(data), err
}

// ParseExecutorOutputs accepts plain final text for a single output. Multiple
// outputs require an exact JSON object; structured values are compact JSON text.
func ParseExecutorOutputs(node Node, text string) (map[string]string, error) {
	return ParseNamedOutputs(node, text, true)
}

func ParseNamedOutputs(node Node, text string, allowPlain bool) (map[string]string, error) {
	if _, err := ExecutorOutputSchema(node); err != nil {
		return nil, err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("исполнитель вернул пустой результат")
	}
	if !strings.HasPrefix(text, "{") {
		if allowPlain && len(node.Outputs) == 1 {
			return map[string]string{node.Outputs[0].Name: text}, nil
		}
		return nil, errors.New("исполнитель должен вернуть JSON-объект с именованными результатами")
	}

	decoder := json.NewDecoder(strings.NewReader(text))
	if _, err := decoder.Token(); err != nil {
		return nil, errors.New("некорректный JSON результатов")
	}
	values := map[string]json.RawMessage{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, errors.New("некорректный JSON результатов")
		}
		name, ok := token.(string)
		if !ok {
			return nil, errors.New("некорректное имя результата")
		}
		if _, exists := values[name]; exists {
			return nil, fmt.Errorf("повторяющийся результат %q", name)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, errors.New("некорректный JSON результатов")
		}
		values[name] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, errors.New("незакрытый JSON результатов")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("после JSON результатов обнаружены лишние данные")
	}
	if len(values) != len(node.Outputs) {
		return nil, errors.New("набор результатов не соответствует объявленным выходам блока")
	}
	outputs := make(map[string]string, len(values))
	for _, output := range node.Outputs {
		value, exists := values[output.Name]
		if !exists || string(value) == "null" {
			return nil, fmt.Errorf("отсутствует результат %q", output.Name)
		}
		var decoded string
		if err := json.Unmarshal(value, &decoded); err != nil {
			var compact bytes.Buffer
			if err := json.Compact(&compact, value); err != nil {
				return nil, errors.New("некорректный JSON результата")
			}
			decoded = compact.String()
		}
		if strings.TrimSpace(decoded) == "" {
			return nil, fmt.Errorf("пустой результат %q", output.Name)
		}
		outputs[output.Name] = decoded
	}
	return outputs, nil
}

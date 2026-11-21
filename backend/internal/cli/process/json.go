package process

import (
	"encoding/json"
	"strings"
)

// RedactJSON preserves valid JSON while redacting credentials in string values
// and values under recognizable credential keys. Input must be valid JSON.
func RedactJSON(input json.RawMessage) (json.RawMessage, error) {
	decoder := json.NewDecoder(strings.NewReader(string(input)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}

	var redact func(any) any
	redact = func(value any) any {
		switch value := value.(type) {
		case string:
			return Redact(value)
		case []any:
			for i, item := range value {
				value[i] = redact(item)
			}
		case map[string]any:
			for key, item := range value {
				if secretAssignment.MatchString(key + "=value") {
					value[key] = "[REDACTED]"
				} else {
					value[key] = redact(item)
				}
			}
		}

		return value
	}

	return json.Marshal(redact(value))
}

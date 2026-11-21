package chatcompletion

import "encoding/json"

type apiMessage struct {
	Role             string        `json:"role"`
	Content          any           `json:"content,omitempty"`
	ReasoningContent string        `json:"reasoning_content,omitempty"`
	ToolCalls        []apiToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string        `json:"tool_call_id,omitempty"`
}

type apiToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func apiTool(name, description string, properties map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "function", "function": map[string]any{"name": name, "description": description, "parameters": map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}}}
}

func apiTools() []map[string]any {
	text := map[string]any{"type": "string"}

	return []map[string]any{
		apiTool("list_files", "List repository tracked and untracked non-ignored files", map[string]any{}),
		apiTool("read_file", "Read UTF-8 file relative to the isolated working tree", map[string]any{"path": text}, "path"),
		apiTool("write_file", "Write UTF-8 file relative to isolated working tree; never write secrets", map[string]any{"path": text, "content": text}, "path", "content"),
		apiTool("run_check", "Run a check as argv (no shell expansion). macOS sandbox denies network and writes outside working tree/temp. Report actual exit status.", map[string]any{"argv": map[string]any{"type": "array", "items": text, "minItems": 1, "maxItems": 100}}, "argv"),
	}
}

// A retry is the same check even if the model changes JSON formatting.
func checkIdentity(arguments string) string {
	var args struct {
		Argv []string `json:"argv"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil || len(args.Argv) == 0 {
		return "invalid:" + arguments
	}
	canonical, _ := json.Marshal(args.Argv)

	return string(canonical)
}

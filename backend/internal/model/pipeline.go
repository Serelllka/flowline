package model

type Output struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Source string `json:"source,omitempty"`
}

type Source struct {
	NodeID   string `json:"nodeId"`
	OutputID string `json:"outputId"`
}

type Node struct {
	ID        string            `json:"id"`
	Type      string            `json:"type"`
	Name      string            `json:"name"`
	Executor  string            `json:"executor"`
	Prompt    string            `json:"prompt"`
	Command   string            `json:"command,omitempty"`
	InputJSON *string           `json:"inputJson,omitempty"`
	X         float64           `json:"x"`
	Y         float64           `json:"y"`
	Outputs   []Output          `json:"outputs"`
	Inputs    map[string]Source `json:"inputs"`
}

// ExecutionType keeps role-based and empty legacy types AI-compatible.
func (n Node) ExecutionType() string {
	switch n.Type {
	case "ai", "", "develop", "review", "test", "publish", "prompt":
		return "ai"
	case "cmd":
		return "cmd"
	default:
		return ""
	}
}

func (n Node) UsesJSONInput() bool {
	return n.Type == "ai" || n.Type == "cmd" || n.InputJSON != nil
}

// StreamSource permits older CMD definitions to use the standard stream name.
func (o Output) StreamSource() string {
	if o.Source != "" {
		return o.Source
	}

	return o.Name
}

func (n Node) ValidOutputSource(o Output) bool {
	if n.ExecutionType() != "cmd" {
		return o.Source == ""
	}

	switch o.StreamSource() {
	case "stdout", "stderr", "exitCode":
		return true
	default:
		return false
	}
}

type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type Viewport struct {
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	Zoom float64 `json:"zoom"`
}

type Pipeline struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Nodes       []Node   `json:"nodes"`
	Edges       []Edge   `json:"edges"`
	Viewport    Viewport `json:"viewport"`
	UpdatedAt   string   `json:"updatedAt"`
}

package model

type Output struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Source struct {
	NodeID   string `json:"nodeId"`
	OutputID string `json:"outputId"`
}

type Node struct {
	ID       string            `json:"id"`
	Type     string            `json:"type"`
	Name     string            `json:"name"`
	Executor string            `json:"executor"`
	Prompt   string            `json:"prompt"`
	X        float64           `json:"x"`
	Y        float64           `json:"y"`
	Outputs  []Output          `json:"outputs"`
	Inputs   map[string]Source `json:"inputs"`
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

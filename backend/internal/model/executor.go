package model

import "context"

// Executor performs a block in its own worktree. Implementations return actual
// results; an unavailable executable or invalid response is always an error.
type Executor interface {
	Info() ExecutorInfo
	Execute(context.Context, Node, string, string, func(string)) (map[string]string, int, error)
}

type ExecutorInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Detail    string `json:"detail"`
}

type PRProvider interface {
	List(context.Context, Repository) ([]PullRequest, error)
	Validate(context.Context, Repository, string, string, int) (*PullRequest, error)
}

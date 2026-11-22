package model

import "encoding/json"

type NodeRun struct {
	NodeID     string            `json:"nodeId"`
	Status     string            `json:"status"`
	StartedAt  string            `json:"startedAt"`
	FinishedAt string            `json:"finishedAt"`
	Error      string            `json:"error"`
	Prompt     string            `json:"prompt"`
	Command    string            `json:"command,omitempty"`
	InputJSON  json.RawMessage   `json:"inputJson,omitempty"`
	Stdout     string            `json:"stdout,omitempty"`
	Stderr     string            `json:"stderr,omitempty"`
	Logs       string            `json:"logs"`
	Worktree   string            `json:"worktree"`
	Commit     string            `json:"commit"`
	Diff       string            `json:"diff"`
	Inputs     map[string]string `json:"inputs"`
	Outputs    map[string]string `json:"outputs"`
	ExitCode   *int              `json:"exitCode"`
}

type Run struct {
	ExecutorConfigs []ExecutorConfig    `json:"executorConfigs"`
	ID              string              `json:"id"`
	PipelineID      string              `json:"pipelineId"`
	PipelineName    string              `json:"pipelineName"`
	Status          string              `json:"status"`
	CreatedAt       string              `json:"createdAt"`
	FinishedAt      string              `json:"finishedAt"`
	Error           string              `json:"error"`
	Diff            string              `json:"diff"`
	Snapshot        Pipeline            `json:"snapshot"`
	Context         GitContext          `json:"context"`
	Nodes           map[string]*NodeRun `json:"nodes"`
}

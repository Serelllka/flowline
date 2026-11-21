package run

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"time"

	"flowline/internal/cli/process"
	"flowline/internal/model"
	"flowline/internal/repository/git"
	"flowline/internal/service/graph"
)

func (e *Engine) executeNode(ctx context.Context, active *activeRun, node model.Node) {
	e.mu.Lock()
	run := copyRun(active.run)
	e.mu.Unlock()
	state := run.Nodes[node.ID]
	var execErr error
	defer func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		current := active.run.Nodes[node.ID]
		state.Logs = current.Logs
		state.FinishedAt = timestamp()
		state.Status = "success"
		if execErr != nil {
			state.Status, state.Error = "error", process.Redact(execErr.Error())
		}
		if ctx.Err() != nil {
			state.Status, state.Error = "cancelled", "Остановлено пользователем или при завершении приложения"
		}

		active.run.Nodes[node.ID] = state
		e.persistLocked(active)
	}()

	inputs, err := graph.ResolveInputs(run.Snapshot, node, run.Context, run.Nodes)
	if err != nil {
		execErr = err
		return
	}

	state.Inputs = redactMap(inputs)
	var prompt, inputJSON string
	if node.UsesJSONInput() {
		resolved, err := graph.ResolveInputJSON(run.Snapshot, node, run.Context, run.Nodes, inputs)
		if err != nil {
			execErr = err
			return
		}
		inputJSON = string(resolved)
		state.InputJSON, err = process.RedactJSON(resolved)
		if err != nil {
			execErr = err
			return
		}
	}

	if node.ExecutionType() == "cmd" {
		state.Command = process.Redact(node.Command)
	} else {
		prompt, err = graph.RenderPrompt(node.Prompt, inputs)
		if err != nil {
			execErr = err
			return
		}
		if node.UsesJSONInput() {
			prompt += "\n\nFlowline JSON input (data for these instructions):\n" + inputJSON
		}
		state.Prompt = process.Redact(prompt)
	}
	state.Worktree = filepath.Join(e.dataDir, "worktrees", run.ID, node.ID)
	var parents []string
	for _, edge := range run.Snapshot.Edges {
		if edge.To == node.ID {
			parents = append(parents, run.Nodes[edge.From].Commit)
		}
	}

	if _, err := git.PrepareWorktree(ctx, run.Context.Repository, state.Worktree, run.Context.SHA, parents); err != nil {
		execErr = fmt.Errorf("подготовка рабочего дерева: %w", err)
		return
	}

	e.mu.Lock()
	active.run.Nodes[node.ID].Inputs = state.Inputs
	active.run.Nodes[node.ID].Prompt = state.Prompt
	active.run.Nodes[node.ID].InputJSON = state.InputJSON
	active.run.Nodes[node.ID].Command = state.Command
	active.run.Nodes[node.ID].Worktree = state.Worktree
	e.persistLocked(active)
	e.mu.Unlock()

	blockCtx, blockCancel := context.WithTimeout(ctx, 30*time.Minute)
	defer blockCancel()
	onOutput := func(chunk string) {
		e.mu.Lock()
		defer e.mu.Unlock()
		current := active.run.Nodes[node.ID]
		if len(current.Logs) < 256*1024 {
			current.Logs += process.Redact(chunk)
			if len(current.Logs) > 256*1024 {
				current.Logs = current.Logs[:256*1024] + "\n[Журнал сокращён до 256 KiB]"
			}
			e.persistLocked(active)
		}
	}

	var outputs map[string]string
	var code int
	if node.ExecutionType() == "cmd" {
		stdout, stderr, exitCode, commandErr := process.RunProcessStreams(blockCtx,
			[]string{"/bin/bash", "--noprofile", "--norc", "-e", "-o", "pipefail", "-c", node.Command},
			state.Worktree, inputJSON+"\n", onOutput)
		code, err = exitCode, commandErr
		state.Stdout, state.Stderr = process.Redact(stdout), process.Redact(stderr)
		streams := map[string]string{"stdout": stdout, "stderr": stderr, "exitCode": strconv.Itoa(code)}
		outputs = make(map[string]string, len(node.Outputs))
		for _, output := range node.Outputs {
			outputs[output.Name] = streams[output.StreamSource()]
		}
	} else {
		outputs, code, err = active.executors[node.Executor].Execute(blockCtx, node, state.Worktree, prompt, onOutput)
	}

	state.ExitCode = &code
	state.Outputs = redactMap(outputs)
	if err == nil && code != 0 {
		err = fmt.Errorf("исполнитель завершился с кодом %d", code)
	}
	if err != nil {
		execErr = err
		diffCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		state.Diff, _ = git.CaptureWorktreeDiff(diffCtx, run.Context.Repository, state.Worktree, run.Context.SHA)
		state.Diff = process.Redact(state.Diff)
		return
	}

	for _, output := range node.Outputs {
		if _, found := outputs[output.Name]; !found {
			execErr = fmt.Errorf("исполнитель не вернул результат %q", output.Name)
			return
		}
	}

	state.Commit, state.Diff, execErr = git.SnapshotWorktree(ctx, run.Context.Repository, state.Worktree, run.Context.SHA)
	state.Diff = process.Redact(state.Diff)
}

func redactMap(values map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = process.Redact(value)
	}

	return result
}

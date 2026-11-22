package command

import (
	"context"
	"strconv"

	"flowline/internal/cli/process"
	"flowline/internal/model"
)

// Result contains raw command streams. Callers redact them before persistence.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Outputs  map[string]string
}

// Execute runs literal Bash source with the JSON envelope on stdin.
func Execute(ctx context.Context, node model.Node, cwd, inputJSON string, log func(string)) (Result, error) {
	stdout, stderr, code, err := process.RunProcessStreams(ctx,
		[]string{"/bin/bash", "--noprofile", "--norc", "-e", "-o", "pipefail", "-c", node.Command},
		cwd, inputJSON+"\n", log)
	streams := map[string]string{"stdout": stdout, "stderr": stderr, "exitCode": strconv.Itoa(code)}
	outputs := make(map[string]string, len(node.Outputs))
	for _, output := range node.Outputs {
		outputs[output.Name] = streams[output.StreamSource()]
	}

	return Result{Stdout: stdout, Stderr: stderr, ExitCode: code, Outputs: outputs}, err
}

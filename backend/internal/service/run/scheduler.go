package run

import (
	"context"
	"strings"

	"flowline/internal/cli/process"
	"flowline/internal/model"
)

func (e *Engine) persistLocked(active *activeRun) bool {
	if err := e.store.SaveRun(active.run); err != nil {
		active.run.Error = "Не удалось сохранить историю: " + process.Redact(err.Error())
		active.cancel()
		if active.run.FinishedAt != "" {
			active.run.Status = "error"
		}
		return false
	}

	return true
}

func (e *Engine) schedule(ctx context.Context, active *activeRun) {
	defer close(active.done)
	completed := make(chan string, len(active.run.Snapshot.Nodes))
	inflight := 0

	for {
		e.mu.Lock()
		pending := false
		for _, node := range active.run.Snapshot.Nodes {
			state := active.run.Nodes[node.ID]
			if state.Status != "waiting" {
				continue
			}

			if ctx.Err() != nil {
				state.Status, state.FinishedAt = "cancelled", timestamp()
				continue
			}

			ready, failed := true, false
			for _, edge := range active.run.Snapshot.Edges {
				if edge.To != node.ID {
					continue
				}

				status := active.run.Nodes[edge.From].Status
				if status == "error" || status == "skipped" || status == "cancelled" || status == "interrupted" {
					failed = true
				}
				if status != "success" {
					ready = false
				}
			}

			if failed {
				state.Status, state.FinishedAt, state.Error = "skipped", timestamp(), "Предшествующий блок не завершился успешно"
				continue
			}

			pending = true
			if ready && inflight < e.parallel {
				state.Status, state.StartedAt = "running", timestamp()
				inflight++
				go func(node model.Node) {
					e.executeNode(ctx, active, node)
					completed <- node.ID
				}(node)
			}
		}
		e.persistLocked(active)

		if inflight == 0 && !pending {
			active.run.Status = "success"
			for _, node := range active.run.Nodes {
				if node.Status == "error" || node.Status == "skipped" {
					active.run.Status = "error"
				}
			}

			if ctx.Err() != nil {
				active.run.Status = "cancelled"
			}
			if active.run.Error != "" {
				active.run.Status = "error"
			}

			active.run.FinishedAt = timestamp()
			var diffs []string
			for _, node := range active.run.Snapshot.Nodes {
				state := active.run.Nodes[node.ID]
				if state.Diff != "" {
					diffs = append(diffs, "--- "+node.Name+" ("+node.ID+") ---\n"+state.Diff)
				}
			}
			active.run.Diff = strings.Join(diffs, "\n")
			if e.persistLocked(active) {
				delete(e.active, active.run.ID)
			}
			e.mu.Unlock()
			return
		}
		e.mu.Unlock()

		if inflight != 0 {
			<-completed
			inflight--
		}
	}
}

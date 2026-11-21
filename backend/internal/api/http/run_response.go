package httpapi

import "flowline/internal/model"

type nodeRunResponse struct {
	*model.NodeRun
	InputJSONText string `json:"inputJsonText,omitempty"`
}

type runResponse struct {
	model.Run
	Nodes map[string]*nodeRunResponse `json:"nodes"`
}

// Keep exact, already-redacted JSON text at the HTTP boundary for history views.
// JavaScript numbers cannot represent every valid JSON number. The original
// object remains available to API consumers; storage keeps only InputJSON.
func newRunResponse(run model.Run) runResponse {
	response := runResponse{Run: run}
	if run.Nodes != nil {
		response.Nodes = make(map[string]*nodeRunResponse, len(run.Nodes))
		for id, node := range run.Nodes {
			if node == nil {
				response.Nodes[id] = nil
				continue
			}

			response.Nodes[id] = &nodeRunResponse{NodeRun: node, InputJSONText: string(node.InputJSON)}
		}
	}

	return response
}

func runResponses(runs []model.Run) []runResponse {
	if runs == nil {
		return nil
	}

	responses := make([]runResponse, len(runs))
	for i, run := range runs {
		responses[i] = newRunResponse(run)
	}

	return responses
}

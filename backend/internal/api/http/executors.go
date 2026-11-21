package httpapi

import (
	"net/http"

	"flowline/internal/model"
)

func (s *Server) executorSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"configs": s.engine.ExecutorConfigs(), "executors": s.engine.ExecutorInfos()})
}

func (s *Server) saveExecutors(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Configs []model.ExecutorConfig `json:"configs"`
	}
	if !readJSON(w, r, &request) {
		return
	}
	if err := s.engine.ConfigureExecutors(request.Configs); err != nil {
		fail(w, 400, err)
		return
	}
	s.executorSettings(w, r)
}

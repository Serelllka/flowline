package httpapi

import (
	"net/http"
)

func (s *Server) bootstrap(w http.ResponseWriter, r *http.Request) {
	repositories, err := s.store.Repositories()
	if err != nil {
		fail(w, 500, err)
		return
	}

	pipelines, err := s.store.Pipelines()
	if err != nil {
		fail(w, 500, err)
		return
	}

	runs, err := s.engine.Runs()
	if err != nil {
		fail(w, 500, err)
		return
	}

	writeJSON(w, 200, map[string]any{"repositories": repositories, "pipelines": pipelines, "runs": runResponses(runs), "executors": s.engine.ExecutorInfos(), "publication": map[string]any{"enabled": true, "detail": "Локальный пакет: diff, описание PR, проверки и релизные заметки. Без push, изменения PR, merge и релиза."}})
}

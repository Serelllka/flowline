package httpapi

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.NotFound(w, r)
		return
	}

	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "" {
		name = "index.html"
	}

	if name != "index.html" && name != "styles.css" && name != "app.js" && name != "icons.js" && name != "graph.js" && name != "executor-config.js" {
		http.NotFound(w, r)
		return
	}

	path := filepath.Join(s.frontend, name)
	if _, err := os.Stat(path); err != nil {
		fail(w, 503, errors.New("файлы интерфейса не найдены; проверьте --frontend"))
		return
	}

	http.ServeFile(w, r, path)
}

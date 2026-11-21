package httpapi

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"

	"flowline/internal/model"
	"flowline/internal/repository/sqlite"
	"flowline/internal/service/run"
)

type Server struct {
	store    *sqlite.Store
	engine   *run.Engine
	frontend string
	provider model.PRProvider
}

func NewServer(store *sqlite.Store, engine *run.Engine, provider model.PRProvider, frontend string) http.Handler {
	s := &Server{store: store, engine: engine, frontend: frontend, provider: provider}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/bootstrap", s.bootstrap)
	mux.HandleFunc("GET /api/executors", s.executorSettings)
	mux.HandleFunc("POST /api/executors", s.saveExecutors)
	mux.HandleFunc("POST /api/repositories", s.addRepository)
	mux.HandleFunc("GET /api/repositories/{id}/context", s.repositoryContext)
	mux.HandleFunc("PUT /api/pipelines/{id}", s.savePipeline)
	mux.HandleFunc("POST /api/pipelines/{id}/validate", s.validatePipeline)
	mux.HandleFunc("POST /api/pipelines/{id}/rename-output", s.renameOutput)
	mux.HandleFunc("POST /api/runs", s.startRun)
	mux.HandleFunc("GET /api/runs", s.runs)
	mux.HandleFunc("GET /api/runs/{id}", s.run)
	mux.HandleFunc("POST /api/runs/{id}/cancel", s.cancelRun)
	mux.HandleFunc("/", s.static)

	return s.localOnly(mux)
}

func (s *Server) localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		w.Header().Set("Cache-Control", "no-store")
		host := r.Host
		if parsed, _, err := net.SplitHostPort(host); err == nil {
			host = parsed
		}

		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			fail(w, http.StatusForbidden, errors.New("доступ разрешён только через loopback"))
			return
		}

		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || u.Scheme != "http" || u.Host != r.Host {
				fail(w, http.StatusForbidden, errors.New("запрос с другого сайта запрещён"))
				return
			}
		}

		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if r.Header.Get("X-Flowline-Request") != "1" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				fail(w, http.StatusForbidden, errors.New("ожидается локальный JSON-запрос Flowline"))
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

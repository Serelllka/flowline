package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"flowline/internal/cli/process"
)

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func fail(w http.ResponseWriter, status int, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		status = http.StatusNotFound
	}

	writeJSON(w, status, map[string]string{"error": process.Redact(err.Error())})
}

func readJSON(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		fail(w, http.StatusBadRequest, fmt.Errorf("неверный JSON: %w", err))
		return false
	}

	if err := decoder.Decode(new(any)); err != io.EOF {
		fail(w, http.StatusBadRequest, errors.New("ожидается один JSON-объект"))
		return false
	}

	return true
}

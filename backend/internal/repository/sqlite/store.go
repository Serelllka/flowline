package sqlite

/*
#cgo LDFLAGS: -lsqlite3
#include <sqlite3.h>
#include <stdlib.h>

static int flowline_bind_text(sqlite3_stmt *stmt, int column, const char *value, int length) {
    return sqlite3_bind_text(stmt, column, value, length, SQLITE_TRANSIENT);
}
*/
import "C"

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"

	"flowline/internal/model"
	"flowline/migrations"
)

// Store owns a serialized SQLite connection. No caller receives a borrowed C buffer.
// Building requires CGO, a C compiler and the system SQLite development library.
type Store struct {
	mu sync.Mutex
	db *C.sqlite3
}

func OpenStore(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" || strings.ContainsRune(path, '\x00') {
		return nil, errors.New("database path is empty or invalid")
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, fmt.Errorf("create database: %w", err)
		}
		chmodErr := file.Chmod(0600)
		closeErr := file.Close()
		if err := errors.Join(chmodErr, closeErr); err != nil {
			return nil, fmt.Errorf("secure database permissions: %w", err)
		}
	}

	filename := C.CString(path)
	defer C.free(unsafe.Pointer(filename))

	store := &Store{}
	result := C.sqlite3_open_v2(filename, &store.db, C.SQLITE_OPEN_READWRITE|C.SQLITE_OPEN_CREATE|C.SQLITE_OPEN_FULLMUTEX, nil)
	if result != C.SQLITE_OK {
		err := store.sqliteError("open database", result)
		if store.db != nil {
			C.sqlite3_close(store.db)
		}

		return nil, err
	}
	if result := C.sqlite3_busy_timeout(store.db, 5000); result != C.SQLITE_OK {
		err := store.sqliteError("set busy timeout", result)
		store.Close()

		return nil, err
	}

	if err := store.execScript("PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA foreign_keys=ON;"); err != nil {
		store.Close()
		return nil, err
	}
	if err := store.migrate(); err != nil {
		store.Close()
		return nil, err
	}

	return store, nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db == nil {
		return nil
	}
	if result := C.sqlite3_close(s.db); result != C.SQLITE_OK {
		return s.sqliteError("close database", result)
	}
	s.db = nil

	return nil
}

func (s *Store) sqliteError(operation string, code C.int) error {
	if s.db == nil {
		return fmt.Errorf("%s: SQLite error %d", operation, int(code))
	}

	return fmt.Errorf("%s: %s (SQLite %d)", operation, C.GoString(C.sqlite3_errmsg(s.db)), int(code))
}

// The following SQL helpers require the mutex or exclusive startup access.
func (s *Store) execScript(query string) error {
	if s.db == nil {
		return errors.New("database is closed")
	}

	text := C.CString(query)
	defer C.free(unsafe.Pointer(text))

	if result := C.sqlite3_exec(s.db, text, nil, nil, nil); result != C.SQLITE_OK {
		return s.sqliteError("execute SQL", result)
	}

	return nil
}

func (s *Store) prepare(query string, args ...string) (*C.sqlite3_stmt, error) {
	if s.db == nil {
		return nil, errors.New("database is closed")
	}

	text := C.CString(query)
	defer C.free(unsafe.Pointer(text))

	var statement *C.sqlite3_stmt
	if result := C.sqlite3_prepare_v2(s.db, text, -1, &statement, nil); result != C.SQLITE_OK {
		return nil, s.sqliteError("prepare SQL", result)
	}
	if int(C.sqlite3_bind_parameter_count(statement)) != len(args) {
		C.sqlite3_finalize(statement)
		return nil, errors.New("SQL parameter count mismatch")
	}

	for i, arg := range args {
		value := C.CString(arg)
		result := C.flowline_bind_text(statement, C.int(i+1), value, C.int(len(arg)))
		C.free(unsafe.Pointer(value))
		if result != C.SQLITE_OK {
			err := s.sqliteError("bind SQL parameter", result)
			C.sqlite3_finalize(statement)

			return nil, err
		}
	}

	return statement, nil
}

func (s *Store) exec(query string, args ...string) error {
	statement, err := s.prepare(query, args...)
	if err != nil {
		return err
	}
	defer C.sqlite3_finalize(statement)

	if result := C.sqlite3_step(statement); result != C.SQLITE_DONE {
		return s.sqliteError("write database", result)
	}

	return nil
}

func (s *Store) query(query string, args ...string) ([][]string, error) {
	statement, err := s.prepare(query, args...)
	if err != nil {
		return nil, err
	}
	defer C.sqlite3_finalize(statement)

	rows := [][]string{}
	for {
		result := C.sqlite3_step(statement)
		if result == C.SQLITE_DONE {
			return rows, nil
		}
		if result != C.SQLITE_ROW {
			return nil, s.sqliteError("read database", result)
		}

		columns := int(C.sqlite3_column_count(statement))
		row := make([]string, columns)
		for i := range columns {
			value := C.sqlite3_column_text(statement, C.int(i))
			length := C.sqlite3_column_bytes(statement, C.int(i))
			row[i] = C.GoStringN((*C.char)(unsafe.Pointer(value)), length)
		}
		rows = append(rows, row)
	}
}

func (s *Store) migrate() (err error) {
	if err := s.execScript("BEGIN IMMEDIATE;"); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = s.execScript("ROLLBACK;")
		}
	}()

	if err = s.execScript("CREATE TABLE IF NOT EXISTS schema_migrations (name TEXT PRIMARY KEY, applied_at TEXT NOT NULL);"); err != nil {
		return err
	}
	entries, err := migrations.Files.ReadDir(".")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	known := map[string]bool{}
	for _, entry := range entries {
		known[entry.Name()] = true
	}
	applied, err := s.query("SELECT name FROM schema_migrations")
	if err != nil {
		return err
	}
	for _, row := range applied {
		if !known[row[0]] {
			return fmt.Errorf("database contains unsupported migration %q", row[0])
		}
	}

	for _, entry := range entries {
		rows, queryErr := s.query("SELECT name FROM schema_migrations WHERE name = ?", entry.Name())
		if queryErr != nil {
			return queryErr
		}
		if len(rows) != 0 {
			continue
		}
		contents, readErr := migrations.Files.ReadFile(entry.Name())
		if readErr != nil {
			return readErr
		}
		if err = s.execScript(string(contents)); err != nil {
			return fmt.Errorf("migration %s: %w", entry.Name(), err)
		}
		if err = s.exec("INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)", entry.Name(), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}

	return s.execScript("COMMIT;")
}

func (s *Store) SaveRepository(repository model.Repository) error {
	if !model.ValidID(repository.ID) || !filepath.IsAbs(repository.Path) || strings.TrimSpace(repository.Name) == "" {
		return errors.New("invalid repository ID, name or absolute path")
	}
	payload, err := json.Marshal(repository)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.exec(`INSERT INTO repositories(id, path, payload) VALUES (?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET path=excluded.path, payload=excluded.payload`, repository.ID, repository.Path, string(payload))
}

func (s *Store) Repositories() ([]model.Repository, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.query("SELECT payload FROM repositories ORDER BY id")
	if err != nil {
		return nil, err
	}

	return decodeRows[model.Repository](rows)
}

func (s *Store) Repository(id string) (model.Repository, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.query("SELECT payload FROM repositories WHERE id = ?", id)
	return decodeOne[model.Repository](rows, err)
}

func (s *Store) SavePipeline(p model.Pipeline) error {
	if err := model.ValidateDraft(p); err != nil {
		return err
	}
	payload, err := json.Marshal(p)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.exec(`INSERT INTO pipelines(id, name, updated_at, payload) VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, updated_at=excluded.updated_at, payload=excluded.payload`, p.ID, p.Name, p.UpdatedAt, string(payload))
}

func (s *Store) Pipelines() ([]model.Pipeline, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.query("SELECT payload FROM pipelines ORDER BY updated_at DESC, id")
	if err != nil {
		return nil, err
	}

	return decodeRows[model.Pipeline](rows)
}

func (s *Store) Pipeline(id string) (model.Pipeline, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.query("SELECT payload FROM pipelines WHERE id = ?", id)
	return decodeOne[model.Pipeline](rows, err)
}

func (s *Store) SaveRun(run model.Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.saveRun(run)
}

func (s *Store) saveRun(run model.Run) error {
	if !model.ValidID(run.ID) || !model.ValidID(run.PipelineID) || run.PipelineID != run.Snapshot.ID || run.CreatedAt == "" {
		return errors.New("invalid run identity")
	}
	if err := model.ValidateDraft(run.Snapshot); err != nil {
		return fmt.Errorf("invalid run snapshot: %w", err)
	}
	switch run.Status {
	case "waiting", "running", "success", "error", "cancelled", "interrupted":
	default:
		return errors.New("invalid run status")
	}

	snapshot, err := json.Marshal(run.Snapshot)
	if err != nil {
		return err
	}
	gitContext, err := json.Marshal(run.Context)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(run)
	if err != nil {
		return err
	}
	executorConfig, err := json.Marshal(run.ExecutorConfigs)
	if err != nil {
		return err
	}

	return s.exec(`INSERT INTO runs(id, pipeline_id, pipeline_name, created_at, status, snapshot, git_context, payload, executor_config)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET pipeline_id=excluded.pipeline_id, pipeline_name=excluded.pipeline_name,
		created_at=excluded.created_at, status=excluded.status, snapshot=excluded.snapshot,
		git_context=excluded.git_context, payload=excluded.payload, executor_config=excluded.executor_config`,
		run.ID, run.PipelineID, run.PipelineName, run.CreatedAt, run.Status, string(snapshot), string(gitContext), string(payload), string(executorConfig))
}

func (s *Store) Runs() ([]model.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.query("SELECT payload FROM runs ORDER BY created_at DESC, id DESC")
	if err != nil {
		return nil, err
	}

	return decodeRows[model.Run](rows)
}

func (s *Store) Run(id string) (model.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.query("SELECT payload FROM runs WHERE id = ?", id)
	return decodeOne[model.Run](rows, err)
}

func decodeRows[T any](rows [][]string) ([]T, error) {
	values := make([]T, 0, len(rows))
	for _, row := range rows {
		var value T
		if err := json.Unmarshal([]byte(row[0]), &value); err != nil {
			return nil, fmt.Errorf("decode stored record: %w", err)
		}
		values = append(values, value)
	}

	return values, nil
}

func decodeOne[T any](rows [][]string, err error) (T, error) {
	var value T
	if err != nil {
		return value, err
	}
	if len(rows) == 0 {
		return value, sql.ErrNoRows
	}
	if err := json.Unmarshal([]byte(rows[0][0]), &value); err != nil {
		return value, fmt.Errorf("decode stored record: %w", err)
	}

	return value, nil
}

// RecoverRuns is called before scheduling starts. It never resumes interrupted
// executors, and preserves all completed outputs and immutable configuration.
func (s *Store) RecoverRuns() (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err = s.execScript("BEGIN IMMEDIATE;"); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = s.execScript("ROLLBACK;")
		}
	}()

	rows, err := s.query("SELECT payload FROM runs WHERE status IN ('waiting', 'running')")
	if err != nil {
		return err
	}
	runs, err := decodeRows[model.Run](rows)
	if err != nil {
		return err
	}
	finished := time.Now().UTC().Format(time.RFC3339Nano)
	for _, run := range runs {
		run.Status, run.FinishedAt = "interrupted", finished
		run.Error = "Приложение завершилось до окончания запуска"
		for _, node := range run.Nodes {
			if node == nil {
				continue
			}
			switch node.Status {
			case "running":
				node.Status, node.FinishedAt = "interrupted", finished
				node.Error = "Исполнение прервано перезапуском приложения"
			case "waiting":
				node.Status, node.FinishedAt = "skipped", finished
				node.Error = "Предыдущий запуск был прерван"
			}
		}
		if err = s.saveRun(run); err != nil {
			return err
		}
	}

	return s.execScript("COMMIT;")
}

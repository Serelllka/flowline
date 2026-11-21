CREATE TABLE repositories (
    id TEXT PRIMARY KEY,
    path TEXT NOT NULL UNIQUE,
    payload TEXT NOT NULL
);
CREATE TABLE pipelines (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    payload TEXT NOT NULL
);
CREATE TABLE runs (
    id TEXT PRIMARY KEY,
    pipeline_id TEXT NOT NULL,
    pipeline_name TEXT NOT NULL,
    created_at TEXT NOT NULL,
    status TEXT NOT NULL,
    snapshot TEXT NOT NULL,
    git_context TEXT NOT NULL,
    payload TEXT NOT NULL
);
CREATE INDEX runs_created_at ON runs(created_at DESC);
CREATE INDEX runs_status ON runs(status);

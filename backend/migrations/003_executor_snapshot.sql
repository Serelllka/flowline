ALTER TABLE runs ADD COLUMN executor_config TEXT NOT NULL DEFAULT 'null';
CREATE TRIGGER runs_immutable_executor_config
BEFORE UPDATE ON runs
WHEN NEW.executor_config != OLD.executor_config
BEGIN
    SELECT RAISE(ABORT, 'run executor configuration is immutable');
END;

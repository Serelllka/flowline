CREATE TRIGGER runs_immutable_context
BEFORE UPDATE ON runs
WHEN NEW.snapshot != OLD.snapshot
  OR NEW.git_context != OLD.git_context
  OR NEW.pipeline_id != OLD.pipeline_id
  OR NEW.pipeline_name != OLD.pipeline_name
  OR NEW.created_at != OLD.created_at
BEGIN
    SELECT RAISE(ABORT, 'run snapshot and Git context are immutable');
END;

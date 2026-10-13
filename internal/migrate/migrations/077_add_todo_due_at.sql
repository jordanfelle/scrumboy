-- Optional deadline for a todo (Unix ms, UTC). NULL means no due date.
ALTER TABLE todos ADD COLUMN due_at INTEGER NULL;

-- Unix ms at which the "due soon" reminder was sent for the current due_at.
-- NULL means no reminder has been sent yet. The application clears it whenever
-- due_at changes so a rescheduled todo is reminded again.
ALTER TABLE todos ADD COLUMN due_reminded_at INTEGER NULL;

-- Serves the reminder scan: active, unreminded todos that have a due date,
-- ordered by deadline. Archived and already-reminded rows stay out of the index.
CREATE INDEX IF NOT EXISTS idx_todos_due_reminder_pending
  ON todos(due_at, id)
  WHERE due_at IS NOT NULL AND due_reminded_at IS NULL AND archived_at IS NULL;

-- Serves board "overdue"/"due within N days" reads and the due-date sort.
CREATE INDEX IF NOT EXISTS idx_todos_active_project_due_at
  ON todos(project_id, due_at, id)
  WHERE due_at IS NOT NULL AND archived_at IS NULL;

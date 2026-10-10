package eventbus

// Domain event types for todo lifecycle webhooks.
const (
	TodoCreatedEventType = "todo.created"
	TodoUpdatedEventType = "todo.updated"
	TodoMovedEventType   = "todo.moved"
	TodoDeletedEventType = "todo.deleted"
)

// TodoLifecyclePayload is the JSON shape for todo.created / todo.updated / todo.moved / todo.deleted.
// Keep in sync with httpapi.Server.PublishTodoLifecycle (marshal).
//
// Integer user ids are included for compatibility; integrations that store an external
// account link must key on the *StableUserID fields (users.public_id), because SQLite may
// reuse integer ids after a delete.
type TodoLifecyclePayload struct {
	ProjectID            int64    `json:"projectId"`
	ProjectSlug          string   `json:"projectSlug,omitempty"`
	TodoID               int64    `json:"todoId"`
	LocalID              int64    `json:"localId"`
	Title                string   `json:"title"`
	ColumnKey            string   `json:"columnKey"`
	AssigneeUserID       *int64   `json:"assigneeUserId,omitempty"`
	AssigneeStableUserID string   `json:"assigneeStableUserId,omitempty"`
	ActorUserID          *int64   `json:"actorUserId,omitempty"`
	ActorStableUserID    string   `json:"actorStableUserId,omitempty"`
	ChangedFields        []string `json:"changedFields,omitempty"` // todo.updated
	FromColumnKey        string   `json:"fromColumnKey,omitempty"` // todo.moved
	ToColumnKey          string   `json:"toColumnKey,omitempty"`
	FromColumnName       string   `json:"fromColumnName,omitempty"`
	ToColumnName         string   `json:"toColumnName,omitempty"`
}

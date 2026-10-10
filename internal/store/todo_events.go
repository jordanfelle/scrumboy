package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Todo lifecycle event types published after a successful commit.
const (
	TodoEventCreated = "todo.created"
	TodoEventUpdated = "todo.updated"
	TodoEventMoved   = "todo.moved"
	TodoEventDeleted = "todo.deleted"
)

// TodoLifecycleEvent describes one committed todo mutation. It is emitted from the store
// (not a transport) so REST and MCP mutations publish identically.
type TodoLifecycleEvent struct {
	Type        string
	ProjectSlug string
	// Todo is the post-commit snapshot (for deletes, the last state before removal).
	Todo        Todo
	ActorUserID *int64
	// Stable user ids are captured from users.public_id inside the mutation
	// transaction. They describe the committed historical event and must never
	// be re-resolved from reusable integer ids after commit.
	ActorStableUserID    string
	AssigneeStableUserID string
	// ChangedFields lists the changed attributes for todo.updated (title, body, sprint,
	// estimation, priority, tags). Assignee changes are delivered as todo.assigned only.
	ChangedFields []string
	// From/To* are set for todo.moved.
	FromColumnKey, ToColumnKey   string
	FromColumnName, ToColumnName string
}

// TodoLifecycleFunc is called after a successful commit. It must not block on slow work.
type TodoLifecycleFunc func(ctx context.Context, e TodoLifecycleEvent)

func (s *Store) SetTodoLifecyclePublisher(fn TodoLifecycleFunc) {
	s.todoLifecyclePublisher = fn
}

func (s *Store) publishTodoLifecycle(ctx context.Context, e TodoLifecycleEvent) {
	if s.todoLifecyclePublisher == nil {
		return
	}
	s.todoLifecyclePublisher(ctx, e)
}

type todoLifecycleIdentitySnapshot struct {
	actorUserID          *int64
	actorStableUserID    string
	assigneeStableUserID string
}

func (i todoLifecycleIdentitySnapshot) apply(e *TodoLifecycleEvent) {
	e.ActorUserID = cloneInt64Ptr(i.actorUserID)
	e.ActorStableUserID = i.actorStableUserID
	e.AssigneeStableUserID = i.assigneeStableUserID
}

// captureTodoLifecycleIdentityTx resolves every stable identity needed by a
// lifecycle event on the same database snapshot as the mutation. Lookup is
// best-effort: integer compatibility ids are retained, while a stable id that
// cannot be established is omitted. Actor and assignee share one query, even
// when they refer to different users.
func captureTodoLifecycleIdentityTx(ctx context.Context, tx *sql.Tx, assigneeUserID *int64) todoLifecycleIdentitySnapshot {
	var snapshot todoLifecycleIdentitySnapshot
	if actorID, ok := UserIDFromContext(ctx); ok {
		snapshot.actorUserID = &actorID
	}

	ids := make([]int64, 0, 2)
	if snapshot.actorUserID != nil {
		ids = append(ids, *snapshot.actorUserID)
	}
	if assigneeUserID != nil && (snapshot.actorUserID == nil || *assigneeUserID != *snapshot.actorUserID) {
		ids = append(ids, *assigneeUserID)
	}
	publicIDs, err := queryUserPublicIDs(ctx, tx, ids...)
	if err != nil {
		return snapshot
	}
	if snapshot.actorUserID != nil {
		snapshot.actorStableUserID = publicIDs[*snapshot.actorUserID]
	}
	if assigneeUserID != nil {
		snapshot.assigneeStableUserID = publicIDs[*assigneeUserID]
	}
	return snapshot
}

type userPublicIDQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func queryUserPublicIDs(ctx context.Context, q userPublicIDQuerier, ids ...int64) (map[int64]string, error) {
	out := make(map[int64]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := q.QueryContext(ctx,
		`SELECT id, COALESCE(public_id, '') FROM users WHERE id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("user public ids: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var pub string
		if err := rows.Scan(&id, &pub); err != nil {
			return nil, fmt.Errorf("scan user public id: %w", err)
		}
		if pub != "" {
			out[id] = pub
		}
	}
	return out, rows.Err()
}

// UserPublicIDs returns users.public_id (the stable external identifier) keyed by integer user id.
// Unknown ids are omitted.
func (s *Store) UserPublicIDs(ctx context.Context, ids ...int64) (map[int64]string, error) {
	return queryUserPublicIDs(ctx, s.db, ids...)
}

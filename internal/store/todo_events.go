package store

import (
	"context"
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
	if actorID, ok := UserIDFromContext(ctx); ok {
		e.ActorUserID = &actorID
	}
	s.todoLifecyclePublisher(ctx, e)
}

// UserPublicIDs returns users.public_id (the stable external identifier) keyed by integer user id.
// Unknown ids are omitted.
func (s *Store) UserPublicIDs(ctx context.Context, ids ...int64) (map[int64]string, error) {
	out := make(map[int64]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx,
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

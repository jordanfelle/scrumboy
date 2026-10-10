package store

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestUpdateTodoTagChangesPersistAndPublishAccurateDiffs(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	owner, err := st.BootstrapUser(context.Background(), "tag-events@example.com", "password123", "Tag Events")
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	ctx := WithUserID(context.Background(), owner.ID)
	project, err := st.CreateProject(ctx, "Tag lifecycle")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	todo, err := st.CreateTodo(ctx, project.ID, CreateTodoInput{
		Title: "initial", ColumnKey: DefaultColumnBacklog, Tags: []string{"alpha"},
	}, ModeFull)
	if err != nil {
		t.Fatalf("create todo: %v", err)
	}

	var events []TodoLifecycleEvent
	st.SetTodoLifecyclePublisher(func(_ context.Context, event TodoLifecycleEvent) {
		events = append(events, event)
	})

	assertUpdate := func(title string, tags, wantTags, wantFields, wantAdded, wantRemoved []string) {
		t.Helper()
		events = nil
		updated, err := st.UpdateTodo(ctx, todo.ID, UpdateTodoInput{Title: title, Tags: tags}, ModeFull)
		if err != nil {
			t.Fatalf("update todo: %v", err)
		}
		persisted, err := st.GetTodoByLocalID(ctx, project.ID, todo.LocalID, ModeFull)
		if err != nil {
			t.Fatalf("read persisted todo: %v", err)
		}
		for label, got := range map[string][]string{"returned": updated.Tags, "persisted": persisted.Tags} {
			if !sameStringSet(got, wantTags) || len(got) != len(wantTags) {
				t.Fatalf("%s tags=%v want set %v", label, got, wantTags)
			}
		}

		if wantFields == nil {
			if len(events) != 0 {
				t.Fatalf("no-op update published events: %+v", events)
			}
			return
		}
		if len(events) != 1 || events[0].Type != TodoEventUpdated {
			t.Fatalf("events=%+v, want one todo.updated", events)
		}
		if !reflect.DeepEqual(events[0].ChangedFields, wantFields) {
			t.Fatalf("changed fields=%v want %v", events[0].ChangedFields, wantFields)
		}

		var raw string
		if err := st.db.QueryRowContext(ctx, `
			SELECT metadata FROM audit_events
			WHERE action = 'todo_updated' AND target_id = ?
			ORDER BY id DESC LIMIT 1
		`, todo.ID).Scan(&raw); err != nil {
			t.Fatalf("read update audit: %v", err)
		}
		var metadata struct {
			ChangedFields []string `json:"changed_fields"`
			TagsAdded     []string `json:"tags_added"`
			TagsRemoved   []string `json:"tags_removed"`
		}
		if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
			t.Fatalf("decode update audit: %v", err)
		}
		if !reflect.DeepEqual(metadata.ChangedFields, wantFields) ||
			!sameStringSet(metadata.TagsAdded, wantAdded) || len(metadata.TagsAdded) != len(wantAdded) ||
			!sameStringSet(metadata.TagsRemoved, wantRemoved) || len(metadata.TagsRemoved) != len(wantRemoved) {
			t.Fatalf("audit metadata=%+v want fields=%v added=%v removed=%v", metadata, wantFields, wantAdded, wantRemoved)
		}
	}

	// Adding, removing, and replacing tags are all tag-only updates.
	assertUpdate("initial", []string{"alpha", "beta"}, []string{"alpha", "beta"}, []string{"tags"}, []string{"beta"}, nil)
	assertUpdate("initial", []string{"beta"}, []string{"beta"}, []string{"tags"}, nil, []string{"alpha"})
	assertUpdate("initial", []string{"gamma"}, []string{"gamma"}, []string{"tags"}, []string{"gamma"}, []string{"beta"})
	assertUpdate("renamed", []string{"gamma", "delta"}, []string{"gamma", "delta"}, []string{"title", "tags"}, []string{"delta"}, nil)

	var auditsBefore int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events WHERE action = 'todo_updated' AND target_id = ?`, todo.ID).Scan(&auditsBefore); err != nil {
		t.Fatalf("count update audits: %v", err)
	}
	assertUpdate("renamed", []string{"delta", "gamma"}, []string{"gamma", "delta"}, nil, nil, nil)
	assertUpdate("renamed", []string{"gamma", "delta"}, []string{"gamma", "delta"}, nil, nil, nil)
	var auditsAfter int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events WHERE action = 'todo_updated' AND target_id = ?`, todo.ID).Scan(&auditsAfter); err != nil {
		t.Fatalf("count update audits after no-ops: %v", err)
	}
	if auditsAfter != auditsBefore {
		t.Fatalf("no-op/reorder created audit events: before=%d after=%d", auditsBefore, auditsAfter)
	}
}

func TestTodoLifecycleNullableIdentitiesRemainOmitted(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	project, err := st.CreateProject(context.Background(), "Pre-bootstrap lifecycle")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	var events []TodoLifecycleEvent
	st.SetTodoLifecyclePublisher(func(_ context.Context, event TodoLifecycleEvent) {
		events = append(events, event)
	})
	if _, err := st.CreateTodo(context.Background(), project.ID, CreateTodoInput{
		Title: "anonymous actor", ColumnKey: DefaultColumnBacklog,
	}, ModeFull); err != nil {
		t.Fatalf("create todo: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events=%+v", events)
	}
	if events[0].ActorUserID != nil || events[0].ActorStableUserID != "" ||
		events[0].Todo.AssigneeUserID != nil || events[0].AssigneeStableUserID != "" {
		t.Fatalf("nullable identities were populated: %+v", events[0])
	}
}

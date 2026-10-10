package mcp_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"scrumboy/internal/db"
	"scrumboy/internal/httpapi"
	"scrumboy/internal/mcp"
	"scrumboy/internal/migrate"
	"scrumboy/internal/store"
)

type lifecycleRecorder struct {
	mu     sync.Mutex
	events []store.TodoLifecycleEvent
}

func (r *lifecycleRecorder) record(_ context.Context, e store.TodoLifecycleEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *lifecycleRecorder) drain() []store.TodoLifecycleEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.events
	r.events = nil
	return out
}

// MCP mutations must publish todo lifecycle events exactly like REST: the hook lives in the store.
func TestTodoLifecycleEventsFromMCP(t *testing.T) {
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "app.db"), db.Options{BusyTimeout: 5000, JournalMode: "WAL", Synchronous: "FULL"})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer sqlDB.Close()
	if err := migrate.Apply(context.Background(), sqlDB); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(sqlDB, nil)
	srv := httpapi.NewServer(st, httpapi.Options{
		MaxRequestBody: 1 << 20,
		ScrumboyMode:   "full",
		MCPHandler:     mcp.New(st, mcp.Options{Mode: "full"}),
	})
	rec := &lifecycleRecorder{}
	st.SetTodoLifecyclePublisher(rec.record)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	client := newCookieClient(t, ts)
	bootstrapUser(t, client, ts.URL)
	ownerID := firstUserID(t, sqlDB)
	project, _ := createTodoUpdateMCPProject(t, st, ownerID, "Lifecycle")
	assignee, err := st.CreateUser(context.Background(), "lifecycle-assignee@example.com", "password123", "Assignee")
	if err != nil {
		t.Fatalf("create assignee: %v", err)
	}
	if err := st.AddProjectMember(store.WithUserID(context.Background(), ownerID), ownerID, project.ID, assignee.ID, store.RoleContributor); err != nil {
		t.Fatalf("add member: %v", err)
	}
	stableIDs, err := st.UserPublicIDs(context.Background(), ownerID, assignee.ID)
	if err != nil {
		t.Fatalf("stable ids: %v", err)
	}
	rec.drain()

	call := func(tool string, args map[string]any) map[string]any {
		t.Helper()
		resp, out := callTodoUpdateMCP(t, client, ts.URL, "legacy", tool, args)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status=%d response=%+v", tool, resp.StatusCode, out)
		}
		return out["data"].(map[string]any)["todo"].(map[string]any)
	}
	types := func(evs []store.TodoLifecycleEvent) []string {
		var out []string
		for _, e := range evs {
			out = append(out, e.Type)
		}
		return out
	}

	created := call("todos_create", map[string]any{"projectSlug": project.Slug, "title": "first", "columnKey": "backlog"})
	localID := int64(created["localId"].(float64))
	evs := rec.drain()
	if got, want := types(evs), []string{"todo.created"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("create events=%v want=%v", got, want)
	}
	if evs[0].ActorUserID == nil || *evs[0].ActorUserID != ownerID || evs[0].ProjectSlug != project.Slug || evs[0].Todo.Title != "first" {
		t.Fatalf("create event=%+v", evs[0])
	}
	if evs[0].ActorStableUserID != stableIDs[ownerID] || evs[0].AssigneeStableUserID != "" {
		t.Fatalf("create stable identities=%+v", evs[0])
	}

	call("todos_update", map[string]any{"projectSlug": project.Slug, "localId": localID, "patch": map[string]any{"title": "renamed"}})
	evs = rec.drain()
	if got, want := types(evs), []string{"todo.updated"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("update events=%v want=%v", got, want)
	}
	if !reflect.DeepEqual(evs[0].ChangedFields, []string{"title"}) || evs[0].Todo.Title != "renamed" {
		t.Fatalf("update event=%+v", evs[0])
	}
	if evs[0].ActorStableUserID != stableIDs[ownerID] || evs[0].AssigneeStableUserID != "" {
		t.Fatalf("update stable identities=%+v", evs[0])
	}

	// No-op patch and assignee-only patch (delivered as todo.assigned, not todo.updated) emit nothing.
	call("todos_update", map[string]any{"projectSlug": project.Slug, "localId": localID, "patch": map[string]any{"title": "renamed"}})
	call("todos_update", map[string]any{"projectSlug": project.Slug, "localId": localID, "patch": map[string]any{"assigneeUserId": assignee.ID}})
	if evs := rec.drain(); len(evs) != 0 {
		t.Fatalf("expected no lifecycle events for no-op/assignee-only update, got %v", types(evs))
	}

	call("todos_move", map[string]any{"projectSlug": project.Slug, "localId": localID, "toColumnKey": "doing"})
	evs = rec.drain()
	if got, want := types(evs), []string{"todo.moved"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("move events=%v want=%v", got, want)
	}
	if evs[0].FromColumnKey != "backlog" || evs[0].ToColumnKey != "doing" || evs[0].Todo.ColumnKey != "doing" {
		t.Fatalf("move event=%+v", evs[0])
	}
	if evs[0].ActorStableUserID != stableIDs[ownerID] || evs[0].AssigneeStableUserID != stableIDs[assignee.ID] {
		t.Fatalf("move stable identities=%+v", evs[0])
	}

	resp, out := callTodoUpdateMCP(t, client, ts.URL, "legacy", "todos_delete", map[string]any{"projectSlug": project.Slug, "localId": localID})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete status=%d response=%+v", resp.StatusCode, out)
	}
	evs = rec.drain()
	if got, want := types(evs), []string{"todo.deleted"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("delete events=%v want=%v", got, want)
	}
	if evs[0].Todo.LocalID != localID || evs[0].Todo.Title != "renamed" {
		t.Fatalf("delete event=%+v", evs[0])
	}
	if evs[0].ActorStableUserID != stableIDs[ownerID] || evs[0].AssigneeStableUserID != stableIDs[assignee.ID] {
		t.Fatalf("delete stable identities=%+v", evs[0])
	}
}

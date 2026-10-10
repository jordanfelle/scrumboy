package httpapi

import (
	"context"
	"encoding/json"
	"testing"

	"scrumboy/internal/eventbus"
	"scrumboy/internal/store"
)

func lifecyclePayloads(t *testing.T, c *collectingConsumer) map[string]eventbus.TodoLifecyclePayload {
	t.Helper()
	out := map[string]eventbus.TodoLifecyclePayload{}
	for _, e := range c.events {
		switch e.Type {
		case "todo.created", "todo.updated", "todo.moved", "todo.deleted":
			var p eventbus.TodoLifecyclePayload
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				t.Fatalf("decode %s: %v", e.Type, err)
			}
			if e.ProjectID != p.ProjectID {
				t.Fatalf("%s envelope projectId=%d payload=%d", e.Type, e.ProjectID, p.ProjectID)
			}
			if _, dup := out[e.Type]; dup {
				t.Fatalf("duplicate %s event", e.Type)
			}
			out[e.Type] = p
		}
	}
	return out
}

func TestPublishTodoLifecycle_PayloadsCarryStableIDs(t *testing.T) {
	srv, st, collector := newTestServerWithCollector(t)
	st.SetTodoLifecyclePublisher(srv.PublishTodoLifecycle)
	ctx, owner, project := setupAuthenticatedProject(t, st)

	pub, err := st.UserPublicIDs(context.Background(), owner.ID, 987654)
	if err != nil {
		t.Fatalf("public ids: %v", err)
	}
	if pub[owner.ID] == "" {
		t.Fatalf("owner has no public_id")
	}
	if _, ok := pub[987654]; ok {
		t.Fatalf("unknown id must be omitted")
	}

	todo, err := st.CreateTodo(ctx, project.ID, store.CreateTodoInput{
		Title: "t", ColumnKey: store.DefaultColumnBacklog, AssigneeUserID: &owner.ID,
	}, store.ModeFull)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := st.UpdateTodo(ctx, todo.ID, store.UpdateTodoInput{Title: "t2", AssigneeUserID: &owner.ID}, store.ModeFull); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := st.MoveTodo(ctx, todo.ID, store.DefaultColumnDoing, nil, nil, store.ModeFull); err != nil {
		t.Fatalf("move: %v", err)
	}
	if err := st.DeleteTodo(ctx, todo.ID, store.ModeFull); err != nil {
		t.Fatalf("delete: %v", err)
	}

	got := lifecyclePayloads(t, collector)
	if len(got) != 4 {
		t.Fatalf("expected created/updated/moved/deleted, got %v", got)
	}
	for typ, p := range got {
		if p.ProjectID != project.ID || p.ProjectSlug != project.Slug || p.TodoID != todo.ID || p.LocalID != todo.LocalID {
			t.Fatalf("%s identity=%+v", typ, p)
		}
		if p.ActorUserID == nil || *p.ActorUserID != owner.ID || p.ActorStableUserID != pub[owner.ID] {
			t.Fatalf("%s actor=%v stable=%q want stable=%q", typ, p.ActorUserID, p.ActorStableUserID, pub[owner.ID])
		}
		if p.AssigneeUserID == nil || p.AssigneeStableUserID != pub[owner.ID] {
			t.Fatalf("%s assignee=%v stable=%q", typ, p.AssigneeUserID, p.AssigneeStableUserID)
		}
	}
	if p := got["todo.updated"]; len(p.ChangedFields) != 1 || p.ChangedFields[0] != "title" || p.Title != "t2" {
		t.Fatalf("updated=%+v", p)
	}
	if p := got["todo.moved"]; p.FromColumnKey != store.DefaultColumnBacklog || p.ToColumnKey != store.DefaultColumnDoing || p.ColumnKey != store.DefaultColumnDoing {
		t.Fatalf("moved=%+v", p)
	}
}

func TestPublishTodoLifecycle_NotDeliveredOnFailedMutation(t *testing.T) {
	srv, st, collector := newTestServerWithCollector(t)
	st.SetTodoLifecyclePublisher(srv.PublishTodoLifecycle)
	ctx, _, project := setupAuthenticatedProject(t, st)

	if _, err := st.CreateTodo(ctx, project.ID, store.CreateTodoInput{Title: "", ColumnKey: store.DefaultColumnBacklog}, store.ModeFull); err == nil {
		t.Fatalf("expected validation error")
	}
	if err := st.DeleteTodo(ctx, 424242, store.ModeFull); err == nil {
		t.Fatalf("expected not found")
	}
	if got := lifecyclePayloads(t, collector); len(got) != 0 {
		t.Fatalf("failed mutations must not publish: %v", got)
	}
}

func TestPublishTodoLifecycle_SameColumnReorderAndContributorBody(t *testing.T) {
	srv, st, collector := newTestServerWithCollector(t)
	st.SetTodoLifecyclePublisher(srv.PublishTodoLifecycle)
	ownerCtx, owner, project := setupAuthenticatedProject(t, st)
	contrib, err := st.CreateUser(context.Background(), "contrib@example.com", "pass1234A!", "Contrib")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := st.AddProjectMember(ownerCtx, owner.ID, project.ID, contrib.ID, store.RoleContributor); err != nil {
		t.Fatalf("add member: %v", err)
	}
	todo, err := st.CreateTodo(ownerCtx, project.ID, store.CreateTodoInput{
		Title: "t", ColumnKey: store.DefaultColumnBacklog, AssigneeUserID: &contrib.ID,
	}, store.ModeFull)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	collector.events = nil

	// Same-column reorder: no todo.moved.
	if _, err := st.MoveTodo(ownerCtx, todo.ID, store.DefaultColumnBacklog, nil, nil, store.ModeFull); err != nil {
		t.Fatalf("reorder: %v", err)
	}
	if got := lifecyclePayloads(t, collector); len(got) != 0 {
		t.Fatalf("same-column reorder must not publish: %v", got)
	}

	// Assigned contributor edits the body (body-only scope): todo.updated, actor != assignee-owner swap-proof.
	contribCtx := store.WithUserID(context.Background(), contrib.ID)
	if _, err := st.UpdateTodo(contribCtx, todo.ID, store.UpdateTodoInput{Title: "t", Body: "new body", AssigneeUserID: &contrib.ID}, store.ModeFull); err != nil {
		t.Fatalf("contributor update: %v", err)
	}
	got := lifecyclePayloads(t, collector)
	p, ok := got["todo.updated"]
	if !ok || len(p.ChangedFields) != 1 || p.ChangedFields[0] != "body" {
		t.Fatalf("contributor body edit: %+v", got)
	}
	if p.ActorUserID == nil || *p.ActorUserID != contrib.ID {
		t.Fatalf("actor=%v", p.ActorUserID)
	}
	// Re-saving the same body is a no-op.
	collector.events = nil
	if _, err := st.UpdateTodo(contribCtx, todo.ID, store.UpdateTodoInput{Title: "t", Body: "new body", AssigneeUserID: &contrib.ID}, store.ModeFull); err != nil {
		t.Fatalf("contributor no-op update: %v", err)
	}
	if got := lifecyclePayloads(t, collector); len(got) != 0 {
		t.Fatalf("no-op body edit must not publish: %v", got)
	}
}

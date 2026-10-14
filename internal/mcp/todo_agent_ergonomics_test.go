package mcp_test

import (
	"net/http"
	"testing"

	"scrumboy/internal/store"
)

// bodyAppend adds to the stored body without resending it, and brief drops the
// body from update/move/get results.
func TestTodoMCPBodyAppendAndBrief(t *testing.T) {
	ts, db, st, cleanup := newTodoUpdateMCPServer(t, "full")
	defer cleanup()
	client := newCookieClient(t, ts)
	bootstrapUser(t, client, ts.URL)
	project, ctx := createTodoUpdateMCPProject(t, st, firstUserID(t, db), "MCP agent ergonomics")
	todo, err := st.CreateTodo(ctx, project.ID, store.CreateTodoInput{Title: "t", Body: "original"}, store.ModeFull)
	if err != nil {
		t.Fatalf("create todo: %v", err)
	}

	call := func(tool string, args map[string]any) map[string]any {
		t.Helper()
		args["projectSlug"] = project.Slug
		args["localId"] = todo.LocalID
		resp, out := callTodoUpdateMCP(t, client, ts.URL, "legacy", tool, args)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status=%d response=%+v", tool, resp.StatusCode, out)
		}
		return out["data"].(map[string]any)["todo"].(map[string]any)
	}
	storedBody := func() string {
		t.Helper()
		got, err := st.GetTodoByLocalID(ctx, project.ID, todo.LocalID, store.ModeFull)
		if err != nil {
			t.Fatalf("reload: %v", err)
		}
		return got.Body
	}

	got := call("todos_update", map[string]any{"patch": map[string]any{"bodyAppend": "MR: !1"}})
	if want := "original\n\nMR: !1"; got["body"] != want || storedBody() != want {
		t.Fatalf("append: response=%v stored=%q want=%q", got["body"], storedBody(), want)
	}

	got = call("todos_update", map[string]any{"brief": true, "patch": map[string]any{"bodyAppend": "MR: !2"}})
	if _, has := got["body"]; has {
		t.Fatalf("brief update returned body: %+v", got)
	}
	if want := "original\n\nMR: !1\n\nMR: !2"; storedBody() != want {
		t.Fatalf("second append stored=%q want=%q", storedBody(), want)
	}

	got = call("todos_update", map[string]any{"patch": map[string]any{"body": "fresh", "bodyAppend": "tail"}})
	if want := "fresh\n\ntail"; got["body"] != want {
		t.Fatalf("body+bodyAppend=%v want=%q", got["body"], want)
	}

	got = call("todos_get", map[string]any{"brief": true})
	if _, has := got["body"]; has || got["title"] != "t" {
		t.Fatalf("brief get=%+v", got)
	}
	got = call("todos_get", map[string]any{})
	if got["body"] != "fresh\n\ntail" {
		t.Fatalf("full get body=%v", got["body"])
	}

	got = call("todos_move", map[string]any{"brief": true, "toColumnKey": "doing"})
	if _, has := got["body"]; has || got["columnKey"] != "doing" {
		t.Fatalf("brief move=%+v", got)
	}

	resp, out := callTodoUpdateMCP(t, client, ts.URL, "legacy", "todos_update", map[string]any{
		"projectSlug": project.Slug, "localId": todo.LocalID, "patch": map[string]any{"bodyAppend": nil},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("null bodyAppend status=%d response=%+v", resp.StatusCode, out)
	}
	resp, out = callTodoUpdateMCP(t, client, ts.URL, "legacy", "todos_update", map[string]any{
		"projectSlug": project.Slug, "localId": todo.LocalID, "patch": map[string]any{"bodyAppend": ""},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty bodyAppend status=%d response=%+v", resp.StatusCode, out)
	}
}

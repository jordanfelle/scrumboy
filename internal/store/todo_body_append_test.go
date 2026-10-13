package store

import (
	"context"
	"sync"
	"testing"
)

// BodyAppend is applied to the body read inside the update transaction, so an
// input built from a stale snapshot does not overwrite a concurrent write.
func TestUpdateTodoBodyAppendUsesFreshBody(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	project, err := st.CreateProject(ctx, "Body Append")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	todo, err := st.CreateTodo(ctx, project.ID, CreateTodoInput{Title: "t", Body: "X"}, ModeFull)
	if err != nil {
		t.Fatalf("CreateTodo: %v", err)
	}

	// Someone else changed the body after the caller took its snapshot ("X").
	if _, err := st.db.ExecContext(ctx, `UPDATE todos SET body = ? WHERE id = ?`, "X\n\nother", todo.ID); err != nil {
		t.Fatalf("concurrent write: %v", err)
	}
	got, err := st.UpdateTodo(ctx, todo.ID, UpdateTodoInput{Title: "t", Body: "X", BodyAppend: "mine"}, ModeFull)
	if err != nil {
		t.Fatalf("UpdateTodo: %v", err)
	}
	if want := "X\n\nother\n\nmine"; got.Body != want {
		t.Fatalf("body=%q want=%q", got.Body, want)
	}

	// With BodyReplaces the supplied body is the base.
	got, err = st.UpdateTodo(ctx, todo.ID, UpdateTodoInput{Title: "t", Body: "fresh", BodyAppend: "tail", BodyReplaces: true}, ModeFull)
	if err != nil {
		t.Fatalf("UpdateTodo replace+append: %v", err)
	}
	if want := "fresh\n\ntail"; got.Body != want {
		t.Fatalf("body=%q want=%q", got.Body, want)
	}
}

func TestUpdateTodoConcurrentBodyAppendsAreNotLost(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	project, err := st.CreateProject(ctx, "Body Append Race")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	todo, err := st.CreateTodo(ctx, project.ID, CreateTodoInput{Title: "t", Body: "X"}, ModeFull)
	if err != nil {
		t.Fatalf("CreateTodo: %v", err)
	}

	const n = 8
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := st.UpdateTodo(ctx, todo.ID, UpdateTodoInput{Title: "t", Body: "X", BodyAppend: string(rune('a' + i))}, ModeFull)
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("UpdateTodo: %v", err)
		}
	}
	got, err := st.GetTodoByLocalID(ctx, project.ID, todo.LocalID, ModeFull)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	for i := 0; i < n; i++ {
		if c := string(rune('a' + i)); !containsLine(got.Body, c) {
			t.Fatalf("append %q lost; body=%q", c, got.Body)
		}
	}
}

func containsLine(body, line string) bool {
	for _, l := range splitLines(body) {
		if l == line {
			return true
		}
	}
	return false
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

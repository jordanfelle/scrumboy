package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func dueAtTestTodo(t *testing.T, st *Store, projectID int64, title, column string, due *time.Time) Todo {
	t.Helper()
	return dueAtTestTodoCtx(t, context.Background(), st, projectID, title, column, due)
}

func dueAtTestTodoCtx(t *testing.T, ctx context.Context, st *Store, projectID int64, title, column string, due *time.Time) Todo {
	t.Helper()
	todo, err := st.CreateTodo(ctx, projectID, CreateTodoInput{Title: title, ColumnKey: column, DueAt: due}, ModeFull)
	if err != nil {
		t.Fatalf("CreateTodo(%q): %v", title, err)
	}
	return todo
}

func TestDueAt_CreateAndGetRoundTrip(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	p, err := st.CreateProject(ctx, "due")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	due := time.Date(2026, 11, 1, 17, 0, 0, 0, time.UTC)
	created := dueAtTestTodo(t, st, p.ID, "with due", DefaultColumnBacklog, &due)
	if created.DueAt == nil || !created.DueAt.Equal(due) {
		t.Fatalf("created DueAt = %v, want %v", created.DueAt, due)
	}
	got, err := st.GetTodoByLocalID(ctx, p.ID, created.LocalID, ModeFull)
	if err != nil {
		t.Fatalf("GetTodoByLocalID: %v", err)
	}
	if got.DueAt == nil || !got.DueAt.Equal(due) {
		t.Fatalf("stored DueAt = %v, want %v", got.DueAt, due)
	}

	none := dueAtTestTodo(t, st, p.ID, "no due", DefaultColumnBacklog, nil)
	if none.DueAt != nil {
		t.Fatalf("todo without a due date has DueAt %v", none.DueAt)
	}
}

func TestDueAt_CreateRejectsOutOfRange(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	p, err := st.CreateProject(ctx, "due")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	for name, v := range map[string]time.Time{
		"epoch":  time.Unix(0, 0).UTC(),
		"before": time.Unix(-5, 0).UTC(),
		"2100":   time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC),
	} {
		v := v
		_, err := st.CreateTodo(ctx, p.ID, CreateTodoInput{Title: name, ColumnKey: DefaultColumnBacklog, DueAt: &v}, ModeFull)
		if !errors.Is(err, ErrValidation) {
			t.Errorf("%s: err = %v, want ErrValidation", name, err)
		}
	}
}

func TestDueAt_UpdateSetClearPreserve(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	p, err := st.CreateProject(ctx, "due")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	todo := dueAtTestTodo(t, st, p.ID, "t", DefaultColumnBacklog, nil)
	base := UpdateTodoInput{Title: "t"}

	due := time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)
	in := base
	in.DueAt, in.DueAtPresent = &due, true
	updated, err := st.UpdateTodo(ctx, todo.ID, in, ModeFull)
	if err != nil {
		t.Fatalf("set due: %v", err)
	}
	if updated.DueAt == nil || !updated.DueAt.Equal(due) {
		t.Fatalf("after set DueAt = %v", updated.DueAt)
	}
	if !updated.MaterialChanged {
		t.Error("setting a due date should be a material change")
	}

	// Absent: preserved.
	updated, err = st.UpdateTodo(ctx, todo.ID, UpdateTodoInput{Title: "renamed"}, ModeFull)
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if updated.DueAt == nil || !updated.DueAt.Equal(due) {
		t.Fatalf("absent dueAt must preserve, got %v", updated.DueAt)
	}

	// Present nil: cleared.
	in = UpdateTodoInput{Title: "renamed", DueAtPresent: true}
	updated, err = st.UpdateTodo(ctx, todo.ID, in, ModeFull)
	if err != nil {
		t.Fatalf("clear due: %v", err)
	}
	if updated.DueAt != nil {
		t.Fatalf("after clear DueAt = %v", updated.DueAt)
	}
	got, _ := st.GetTodoByLocalID(ctx, p.ID, todo.LocalID, ModeFull)
	if got.DueAt != nil {
		t.Fatalf("stored DueAt after clear = %v", got.DueAt)
	}
}

func TestDueAt_ClaimDueReminders(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	p, err := st.CreateProject(ctx, "due")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	at := func(d time.Duration) *time.Time { v := now.Add(d); return &v }

	soon := dueAtTestTodo(t, st, p.ID, "due in 3h", DefaultColumnDoing, at(3*time.Hour))
	edge := dueAtTestTodo(t, st, p.ID, "due in 23h", DefaultColumnBacklog, at(23*time.Hour))
	far := dueAtTestTodo(t, st, p.ID, "due in 3d", DefaultColumnBacklog, at(72*time.Hour))
	overdue := dueAtTestTodo(t, st, p.ID, "due 2h ago", DefaultColumnBacklog, at(-2*time.Hour))
	done := dueAtTestTodo(t, st, p.ID, "done", DefaultColumnDone, at(2*time.Hour))
	nodue := dueAtTestTodo(t, st, p.ID, "no due", DefaultColumnBacklog, nil)
	_ = nodue

	got, err := st.ClaimDueReminders(ctx, now, 100)
	if err != nil {
		t.Fatalf("ClaimDueReminders: %v", err)
	}
	ids := map[int64]DueReminder{}
	for _, r := range got {
		ids[r.Todo.ID] = r
		if r.ProjectSlug != p.Slug {
			t.Errorf("slug = %q, want %q", r.ProjectSlug, p.Slug)
		}
		if r.LeadHours != DefaultDueReminderLeadHours {
			t.Errorf("lead = %d", r.LeadHours)
		}
	}
	if _, ok := ids[soon.ID]; !ok {
		t.Error("todo due in 3h should be reminded")
	}
	if _, ok := ids[edge.ID]; !ok {
		t.Error("todo due in 23h should be reminded with a 24h lead")
	}
	for name, id := range map[string]int64{"far": far.ID, "overdue": overdue.ID, "done": done.ID} {
		if _, ok := ids[id]; ok {
			t.Errorf("%s todo must not be reminded", name)
		}
	}

	// Claimed exactly once.
	again, err := st.ClaimDueReminders(ctx, now.Add(time.Minute), 100)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	for _, r := range again {
		if r.Todo.ID == soon.ID || r.Todo.ID == edge.ID {
			t.Errorf("todo %d claimed twice", r.Todo.ID)
		}
	}

	// The overdue todo was marked silently, so it never fires later either.
	var marked int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM todos WHERE id = ? AND due_reminded_at IS NOT NULL`, overdue.ID).Scan(&marked); err != nil || marked != 1 {
		t.Errorf("overdue todo marked = %d (err %v), want 1", marked, err)
	}

	// The far todo enters the window two days later.
	later, err := st.ClaimDueReminders(ctx, now.Add(49*time.Hour), 100)
	if err != nil {
		t.Fatalf("later claim: %v", err)
	}
	found := false
	for _, r := range later {
		if r.Todo.ID == far.ID {
			found = true
		}
	}
	if !found {
		t.Error("far todo should be reminded once inside the window")
	}
}

func TestDueAt_RescheduleResetsReminder(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	p, err := st.CreateProject(ctx, "due")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	due := now.Add(2 * time.Hour)
	todo := dueAtTestTodo(t, st, p.ID, "t", DefaultColumnBacklog, &due)

	if got, _ := st.ClaimDueReminders(ctx, now, 10); len(got) != 1 {
		t.Fatalf("first claim = %d reminders, want 1", len(got))
	}
	if got, _ := st.ClaimDueReminders(ctx, now, 10); len(got) != 0 {
		t.Fatalf("repeat claim = %d reminders, want 0", len(got))
	}

	// Same value: still reminded, no new cycle.
	in := UpdateTodoInput{Title: "t", DueAt: &due, DueAtPresent: true}
	if _, err := st.UpdateTodo(ctx, todo.ID, in, ModeFull); err != nil {
		t.Fatalf("same-value update: %v", err)
	}
	if got, _ := st.ClaimDueReminders(ctx, now, 10); len(got) != 0 {
		t.Fatalf("unchanged due date was reminded again (%d)", len(got))
	}

	// New value: new cycle.
	next := now.Add(5 * time.Hour)
	in.DueAt = &next
	if _, err := st.UpdateTodo(ctx, todo.ID, in, ModeFull); err != nil {
		t.Fatalf("reschedule: %v", err)
	}
	if got, _ := st.ClaimDueReminders(ctx, now, 10); len(got) != 1 {
		t.Fatalf("rescheduled todo claim = %d, want 1", len(got))
	}
}

func TestDueAt_BackupRoundTrip(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	p, err := st.CreateProject(ctx, "due")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	due := time.Date(2026, 11, 2, 8, 30, 0, 0, time.UTC)
	dueAtTestTodo(t, st, p.ID, "with due", DefaultColumnBacklog, &due)
	dueAtTestTodo(t, st, p.ID, "without", DefaultColumnBacklog, nil)

	data, err := st.ExportAllProjects(ctx, ModeFull)
	if err != nil {
		t.Fatalf("ExportAllProjects: %v", err)
	}
	var exportedDue, exportedNone int
	for _, pr := range data.Projects {
		for _, td := range pr.Todos {
			if td.DueAt != nil {
				exportedDue++
				if *td.DueAt != due.UnixMilli() {
					t.Errorf("exported dueAt = %d, want %d", *td.DueAt, due.UnixMilli())
				}
			} else {
				exportedNone++
			}
		}
	}
	if exportedDue != 1 || exportedNone != 1 {
		t.Fatalf("exported due=%d none=%d, want 1/1", exportedDue, exportedNone)
	}

	if _, err := st.ImportProjects(ctx, data, ModeFull, "copy"); err != nil {
		t.Fatalf("ImportProjects(copy): %v", err)
	}
	after, err := st.ExportAllProjects(ctx, ModeFull)
	if err != nil {
		t.Fatalf("re-export: %v", err)
	}
	withDue := 0
	for _, pr := range after.Projects {
		for _, td := range pr.Todos {
			if td.DueAt != nil {
				withDue++
			}
		}
	}
	if withDue != 2 {
		t.Fatalf("after copy import %d todos carry a due date, want 2 (original + copy)", withDue)
	}
}

func TestDueAt_BackupRejectsOutOfRange(t *testing.T) {
	bad := int64(0)
	data := &ExportData{Version: "1.2", Projects: []ProjectExport{{Todos: []TodoExport{{LocalID: 1, DueAt: &bad}}}}}
	if err := validateBackupVersionAndArchiveFields(data); !errors.Is(err, ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}
}

func TestDueAt_ReturnedValueMatchesStoredPrecision(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	p, err := st.CreateProject(ctx, "due")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	due := time.Date(2026, 11, 1, 17, 0, 0, 123456789, time.UTC) // sub-millisecond
	created := dueAtTestTodo(t, st, p.ID, "t", DefaultColumnBacklog, &due)
	got, err := st.GetTodoByLocalID(ctx, p.ID, created.LocalID, ModeFull)
	if err != nil {
		t.Fatalf("GetTodoByLocalID: %v", err)
	}
	if !created.DueAt.Equal(*got.DueAt) {
		t.Fatalf("create returned %v but the stored value reads back %v", created.DueAt, got.DueAt)
	}

	// Re-saving the same instant (even with extra precision) is not a change.
	in := UpdateTodoInput{Title: "t", DueAt: &due, DueAtPresent: true}
	updated, err := st.UpdateTodo(ctx, created.ID, in, ModeFull)
	if err != nil {
		t.Fatalf("UpdateTodo: %v", err)
	}
	if updated.MaterialChanged {
		t.Error("re-saving the same due date reported a material change")
	}
}

func TestDueAt_ClaimSkipsArchivedAndTemporaryBoards(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	p, err := st.CreateProject(ctx, "due")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	due := now.Add(2 * time.Hour)

	archived := dueAtTestTodo(t, st, p.ID, "archived", DefaultColumnBacklog, &due)
	live := dueAtTestTodo(t, st, p.ID, "live", DefaultColumnBacklog, &due)
	if _, err := st.ArchiveTodoByLocalID(ctx, p.ID, archived.LocalID, ModeFull); err != nil {
		t.Fatalf("ArchiveTodoByLocalID: %v", err)
	}

	temp, err := st.CreateAnonymousBoard(ctx)
	if err != nil {
		t.Fatalf("CreateAnonymousBoard: %v", err)
	}
	if _, err := st.CreateTodo(ctx, temp.ID, CreateTodoInput{Title: "temp", ColumnKey: DefaultColumnBacklog, DueAt: &due}, ModeAnonymous); err != nil {
		t.Fatalf("CreateTodo(temp): %v", err)
	}

	got, err := st.ClaimDueReminders(ctx, now, 100)
	if err != nil {
		t.Fatalf("ClaimDueReminders: %v", err)
	}
	if len(got) != 1 || got[0].Todo.ID != live.ID {
		t.Fatalf("claimed %d reminders, want exactly the live durable todo %d: %+v", len(got), live.ID, got)
	}
}

func dueAtPendingMarked(t *testing.T, st *Store, id int64) bool {
	t.Helper()
	var n int
	if err := st.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM todos WHERE id = ? AND due_reminded_at IS NOT NULL`, id).Scan(&n); err != nil {
		t.Fatalf("query marker: %v", err)
	}
	return n == 1
}

func TestDueAt_ReassignAfterUnassignedClaimRearms(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	owner, err := st.BootstrapUser(ctx, "owner@example.com", "pass1234A!", "Owner")
	if err != nil {
		t.Fatal(err)
	}
	ctx = WithUserID(ctx, owner.ID)
	p, err := st.CreateProject(ctx, "due")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	due := now.Add(20 * time.Hour)
	todo := dueAtTestTodoCtx(t, ctx, st, p.ID, "unassigned", DefaultColumnBacklog, &due)

	// The reminder fires while nobody is assigned: nobody can be emailed.
	if got, _ := st.ClaimDueReminders(ctx, now, 10); len(got) != 1 {
		t.Fatalf("first claim = %d, want 1", len(got))
	}
	// Assigning someone afterwards must not leave them without a reminder.
	if _, err := st.UpdateTodo(ctx, todo.ID, UpdateTodoInput{Title: "unassigned", AssigneeUserID: &owner.ID}, ModeFull); err != nil {
		t.Fatalf("assign: %v", err)
	}
	got, _ := st.ClaimDueReminders(ctx, now.Add(time.Minute), 10)
	if len(got) != 1 || got[0].Todo.AssigneeUserID == nil || *got[0].Todo.AssigneeUserID != owner.ID {
		t.Fatalf("claim after assignment = %+v, want one reminder for the new assignee", got)
	}
	// An update that does not change the assignee does not re-arm.
	if _, err := st.UpdateTodo(ctx, todo.ID, UpdateTodoInput{Title: "renamed", AssigneeUserID: &owner.ID}, ModeFull); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if got, _ := st.ClaimDueReminders(ctx, now.Add(2*time.Minute), 10); len(got) != 0 {
		t.Fatalf("rename re-armed the reminder (%d)", len(got))
	}
}

func TestDueAt_ClaimNotStarvedByOverdueBacklog(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	p, err := st.CreateProject(ctx, "due")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	var overdue []Todo
	for i := 0; i < 6; i++ {
		d := now.Add(-time.Duration(i+1) * time.Hour)
		overdue = append(overdue, dueAtTestTodo(t, st, p.ID, "overdue", DefaultColumnBacklog, &d))
	}
	doneDue := now.Add(-time.Hour)
	doneTodo := dueAtTestTodo(t, st, p.ID, "done long ago", DefaultColumnDone, &doneDue)
	soon := now.Add(15 * time.Minute)
	imminent := dueAtTestTodo(t, st, p.ID, "imminent", DefaultColumnBacklog, &soon)

	// limit (2) is far smaller than the overdue backlog (7): the imminent card must
	// still be reminded on the very first call.
	got, err := st.ClaimDueReminders(ctx, now, 2)
	if err != nil {
		t.Fatalf("ClaimDueReminders: %v", err)
	}
	if len(got) != 1 || got[0].Todo.ID != imminent.ID {
		t.Fatalf("claimed %+v, want only the imminent todo %d", got, imminent.ID)
	}
	for _, o := range append(overdue, doneTodo) {
		if !dueAtPendingMarked(t, st, o.ID) {
			t.Errorf("overdue todo %d (%s) was not marked, so it stays in the pending set", o.ID, o.Title)
		}
	}
}

func TestDueAt_MergeImportKeepsReminderMarkerWhenUnchanged(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	owner, err := st.BootstrapUser(ctx, "owner@example.com", "pass1234A!", "Owner")
	if err != nil {
		t.Fatal(err)
	}
	ctx = WithUserID(ctx, owner.ID)
	p, err := st.CreateProject(ctx, "due")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	due := now.Add(10 * time.Hour)
	todo := dueAtTestTodoCtx(t, ctx, st, p.ID, "t", DefaultColumnBacklog, &due)
	if got, _ := st.ClaimDueReminders(ctx, now, 10); len(got) != 1 {
		t.Fatalf("first claim = %d, want 1", len(got))
	}

	data, err := st.ExportAllProjects(ctx, ModeFull)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if _, err := st.ImportProjects(ctx, data, ModeFull, "merge"); err != nil {
		t.Fatalf("merge import: %v", err)
	}
	if !dueAtPendingMarked(t, st, todo.ID) {
		t.Fatal("merge-importing an unchanged dueAt cleared the reminder marker")
	}
	if got, _ := st.ClaimDueReminders(ctx, now.Add(time.Minute), 10); len(got) != 0 {
		t.Fatalf("merge import caused %d duplicate reminders", len(got))
	}

	// A changed dueAt through merge import is a new cycle.
	for i := range data.Projects {
		for j := range data.Projects[i].Todos {
			v := due.Add(3 * time.Hour).UnixMilli()
			data.Projects[i].Todos[j].DueAt = &v
		}
	}
	if _, err := st.ImportProjects(ctx, data, ModeFull, "merge"); err != nil {
		t.Fatalf("merge import (changed): %v", err)
	}
	if got, _ := st.ClaimDueReminders(ctx, now.Add(2*time.Minute), 10); len(got) != 1 {
		t.Fatalf("rescheduled merge import claim = %d, want 1", len(got))
	}
}

func TestDueAt_ExportJSONRoundTripsStrictly(t *testing.T) {
	due := time.Date(2026, 11, 2, 8, 30, 0, 0, time.UTC).UnixMilli()
	in := ExportData{Version: "1.2", Projects: []ProjectExport{{Slug: "p", Name: "P", Todos: []TodoExport{
		{LocalID: 1, Title: "with", DueAt: &due},
		{LocalID: 2, Title: "without"},
	}}}}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(raw), `"dueAt"`) != 1 {
		t.Fatalf("dueAt must appear only for the todo that has one: %s", raw)
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	var out ExportData
	if err := dec.Decode(&out); err != nil {
		t.Fatalf("strict decode: %v", err)
	}
	todos := out.Projects[0].Todos
	if todos[0].DueAt == nil || *todos[0].DueAt != due || todos[1].DueAt != nil {
		t.Fatalf("round trip lost dueAt: %+v", todos)
	}
}

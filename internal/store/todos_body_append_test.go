package store

import (
	"strings"
	"testing"
)

// A body append built from a stale snapshot must not revert fields the caller
// omitted that another writer changed after the snapshot was read.
func TestUpdateTodo_OmittedFieldsKeepLatestValuesInsideTx(t *testing.T) {
	st, cleanup, ctx, project, maintainer, _, _ := setupAssigneeTestProject(t)
	defer cleanup()
	ctxM := WithUserID(ctx, maintainer.ID)

	todo, err := st.CreateTodo(ctxM, project.ID, CreateTodoInput{
		Title: "Original", Body: "base", Tags: []string{"old"}, ColumnKey: DefaultColumnBacklog,
	}, ModeFull)
	if err != nil {
		t.Fatalf("CreateTodo: %v", err)
	}
	stale := todo // snapshot the appender read before the concurrent edit

	if _, err := st.UpdateTodo(ctxM, todo.ID, UpdateTodoInput{
		Title:            "Renamed",
		Body:             "base",
		Tags:             []string{"fresh"},
		EstimationPoints: ptrInt64(5),
		AssigneeUserID:   ptrInt64(maintainer.ID),
	}, ModeFull); err != nil {
		t.Fatalf("concurrent edit: %v", err)
	}

	got, err := st.UpdateTodo(ctxM, todo.ID, UpdateTodoInput{
		Title:                stale.Title,
		Body:                 stale.Body,
		Tags:                 stale.Tags,
		EstimationPoints:     stale.EstimationPoints,
		AssigneeUserID:       stale.AssigneeUserID,
		BodyAppend:           "MR: !1",
		KeepTitle:            true,
		KeepBody:             true,
		KeepTags:             true,
		KeepEstimationPoints: true,
		KeepAssigneeUserID:   true,
	}, ModeFull)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if got.Title != "Renamed" {
		t.Fatalf("title reverted: %q", got.Title)
	}
	if len(got.Tags) != 1 || got.Tags[0] != "fresh" {
		t.Fatalf("tags reverted: %v", got.Tags)
	}
	if got.EstimationPoints == nil || *got.EstimationPoints != 5 {
		t.Fatalf("estimation reverted: %v", got.EstimationPoints)
	}
	if got.AssigneeUserID == nil || *got.AssigneeUserID != maintainer.ID {
		t.Fatalf("assignee reverted: %v", got.AssigneeUserID)
	}
	if !strings.HasSuffix(got.Body, "MR: !1") || !strings.HasPrefix(got.Body, "base") {
		t.Fatalf("body = %q", got.Body)
	}
}


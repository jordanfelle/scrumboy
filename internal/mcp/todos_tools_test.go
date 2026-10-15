package mcp

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	todoapp "scrumboy/internal/application/todo"
)

func TestBuildUpdatePatch_patchSprintId_omitted(t *testing.T) {
	t.Parallel()

	patch, aerr := buildUpdatePatch(json.RawMessage(`{"title":"after"}`))
	if aerr != nil {
		t.Fatalf("buildUpdatePatch: %v", aerr)
	}
	if !patch.Title.Present || patch.Title.Value != "after" {
		t.Fatalf("title field = %+v", patch.Title)
	}
	if patch.SprintID.Present {
		t.Fatalf("omitted sprintId field = %+v, want absent", patch.SprintID)
	}
}

func TestTodoResultBriefOmitsOnlyBodyAndRetainsDueAt(t *testing.T) {
	due := time.Date(2026, 11, 1, 17, 0, 0, 0, time.UTC)
	item := todoItem{ProjectSlug: "roadmap", LocalID: 4, Title: "Ship it", Body: "sensitive body", DueAt: &due}

	decode := func(value any) map[string]any {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		return got
	}

	full := decode(todoResult(false, item))
	if full["body"] != item.Body || full["dueAt"] != due.Format(time.RFC3339) {
		t.Fatalf("full projection = %+v", full)
	}
	brief := decode(todoResult(true, item))
	if _, ok := brief["body"]; ok {
		t.Fatalf("brief projection returned body: %+v", brief)
	}
	if brief["dueAt"] != due.Format(time.RFC3339) {
		t.Fatalf("brief dueAt = %v, want %s", brief["dueAt"], due.Format(time.RFC3339))
	}

	item.DueAt = nil
	brief = decode(todoResult(true, item))
	if dueAt, ok := brief["dueAt"]; !ok || dueAt != nil {
		t.Fatalf("unset brief dueAt = %v present=%v, want explicit null", dueAt, ok)
	}
}

func TestBuildUpdatePatch_patchSprintId_assigns(t *testing.T) {
	t.Parallel()
	patch := json.RawMessage(`{"sprintId":42}`)
	got, aerr := buildUpdatePatch(patch)
	if aerr != nil {
		t.Fatalf("buildUpdatePatch: %v", aerr)
	}
	if !got.SprintID.Present || got.SprintID.Value == nil || *got.SprintID.Value != 42 {
		t.Fatalf("sprint field = %+v, want present 42", got.SprintID)
	}
}

func TestBuildUpdatePatch_patchSprintId_null_clears(t *testing.T) {
	t.Parallel()
	patch := json.RawMessage(`{"sprintId":null}`)
	got, aerr := buildUpdatePatch(patch)
	if aerr != nil {
		t.Fatalf("buildUpdatePatch: %v", aerr)
	}
	if !got.SprintID.Present || got.SprintID.Value != nil {
		t.Fatalf("sprint field = %+v, want present nil", got.SprintID)
	}
}

func TestBuildUpdatePatch_patchSprintId_invalidJSON(t *testing.T) {
	t.Parallel()
	patch := json.RawMessage(`{"sprintId":"not-a-number"}`)
	_, aerr := buildUpdatePatch(patch)
	if aerr == nil {
		t.Fatal("expected adapter error for invalid sprintId type")
	}
	if aerr.Code != CodeValidationError {
		t.Fatalf("expected validation error code, got %q", aerr.Code)
	}
}

func TestBuildUpdatePatch_mapsAllSupportedFields(t *testing.T) {
	t.Parallel()

	patch, aerr := buildUpdatePatch(json.RawMessage(`{
		"title":"title",
		"body":"body",
		"tags":["one","two"],
		"estimationPoints":8,
		"assigneeUserId":21,
		"sprintId":13
	}`))
	if aerr != nil {
		t.Fatalf("buildUpdatePatch: %v", aerr)
	}
	if !patch.Title.Present || patch.Title.Value != "title" || !patch.Body.Present || patch.Body.Value != "body" {
		t.Fatalf("string fields = title %+v body %+v", patch.Title, patch.Body)
	}
	if !patch.Tags.Present || !reflect.DeepEqual(patch.Tags.Value, []string{"one", "two"}) {
		t.Fatalf("tags field = %+v", patch.Tags)
	}
	assertMCPUpdatePatchPointer(t, "estimationPoints", patch.EstimationPoints, 8)
	assertMCPUpdatePatchPointer(t, "assigneeUserId", patch.AssigneeUserID, 21)
	assertMCPUpdatePatchPointer(t, "sprintId", patch.SprintID, 13)
}

func TestBuildUpdatePatch_mapsNullableClearsAndEmptyPatch(t *testing.T) {
	t.Parallel()

	patch, aerr := buildUpdatePatch(json.RawMessage(`{
		"estimationPoints":null,
		"assigneeUserId":null,
		"sprintId":null
	}`))
	if aerr != nil {
		t.Fatalf("buildUpdatePatch clears: %v", aerr)
	}
	for name, field := range map[string]todoapp.Field[*int64]{
		"estimationPoints": patch.EstimationPoints,
		"assigneeUserId":   patch.AssigneeUserID,
		"sprintId":         patch.SprintID,
	} {
		if !field.Present || field.Value != nil {
			t.Fatalf("%s field = %+v, want present nil", name, field)
		}
	}

	empty, aerr := buildUpdatePatch(json.RawMessage(`{}`))
	if aerr != nil {
		t.Fatalf("buildUpdatePatch empty: %v", aerr)
	}
	if empty.HasFields() {
		t.Fatalf("empty patch = %+v, want no present fields", empty)
	}
}

func TestBuildUpdatePatch_rejectsNullReplacementAndUnsupportedFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		patch     string
		message   string
		wantField string
	}{
		{name: "title null", patch: `{"title":null}`, message: "title cannot be null", wantField: "title"},
		{name: "body null", patch: `{"body":null}`, message: "body cannot be null", wantField: "body"},
		{name: "tags null", patch: `{"tags":null}`, message: "tags cannot be null", wantField: "tags"},
		{name: "unsupported", patch: `{"columnKey":"done"}`, message: "unsupported patch field", wantField: "columnKey"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, aerr := buildUpdatePatch(json.RawMessage(tt.patch))
			if aerr == nil || aerr.Code != CodeValidationError || aerr.Message != tt.message {
				t.Fatalf("error = %+v, want validation message %q field %q", aerr, tt.message, tt.wantField)
			}
			details, ok := aerr.Details.(map[string]any)
			if !ok || details["field"] != tt.wantField {
				t.Fatalf("error details = %#v, want field %q", aerr.Details, tt.wantField)
			}
		})
	}
}

func assertMCPUpdatePatchPointer(t *testing.T, name string, field todoapp.Field[*int64], want int64) {
	t.Helper()
	if !field.Present || field.Value == nil || *field.Value != want {
		t.Fatalf("%s field = %+v, want present %d", name, field, want)
	}
}

func TestBuildUpdatePatch_dueAt(t *testing.T) {
	t.Parallel()

	got, aerr := buildUpdatePatch(json.RawMessage(`{"title":"t"}`))
	if aerr != nil {
		t.Fatalf("buildUpdatePatch: %v", aerr)
	}
	if got.DueAt.Present {
		t.Fatalf("omitted dueAt = %+v, want absent", got.DueAt)
	}

	got, aerr = buildUpdatePatch(json.RawMessage(`{"dueAt":"2026-11-01T17:00:00Z"}`))
	if aerr != nil {
		t.Fatalf("buildUpdatePatch(set): %v", aerr)
	}
	if !got.DueAt.Present || got.DueAt.Value == nil || got.DueAt.Value.UTC().Format("2006-01-02T15:04:05Z") != "2026-11-01T17:00:00Z" {
		t.Fatalf("set dueAt = %+v", got.DueAt)
	}
	if !got.DueAt.Present || !(todoapp.UpdatePatch{DueAt: got.DueAt}).HasFields() {
		t.Fatal("a dueAt-only patch must count as a present field")
	}

	got, aerr = buildUpdatePatch(json.RawMessage(`{"dueAt":null}`))
	if aerr != nil {
		t.Fatalf("buildUpdatePatch(null): %v", aerr)
	}
	if !got.DueAt.Present || got.DueAt.Value != nil {
		t.Fatalf("null dueAt = %+v, want present nil", got.DueAt)
	}

	for _, bad := range []string{`{"dueAt":"tomorrow"}`, `{"dueAt":12345}`, `{"dueAt":true}`} {
		if _, aerr := buildUpdatePatch(json.RawMessage(bad)); aerr == nil || aerr.Code != CodeValidationError {
			t.Errorf("%s: err = %v, want validation error", bad, aerr)
		}
	}
}

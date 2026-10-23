package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"
)

func wallPlacementsByTitle(t *testing.T, sqlDB *sql.DB, projectID int64) []string {
	t.Helper()
	rows, err := sqlDB.Query(`
SELECT t.title, p.x, p.y FROM wall_story_placements p JOIN todos t ON t.id = p.todo_id
WHERE p.project_id = ?`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var title string
		var x, y float64
		if err := rows.Scan(&title, &x, &y); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%s@%g,%g", title, x, y))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func wallEdgesByTitle(t *testing.T, st *Store, sqlDB *sql.DB, projectID int64) []string {
	t.Helper()
	wall, err := st.GetWall(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	name := func(raw string) string {
		ep, err := ParseWallEdgeEndpoint(raw)
		if err != nil {
			t.Fatalf("stored edge endpoint %q: %v", raw, err)
		}
		if ep.Kind() != WallEdgeEndpointStory {
			return "note"
		}
		var title string
		if err := sqlDB.QueryRow(`SELECT title FROM todos WHERE project_id = ? AND local_id = ?`, projectID, ep.TodoLocalID()).Scan(&title); err != nil {
			return fmt.Sprintf("missing#%d", ep.TodoLocalID())
		}
		return title
	}
	var out []string
	for _, e := range wall.Edges {
		a, b := name(e.From), name(e.To)
		if a > b {
			a, b = b, a
		}
		out = append(out, a+" ~ "+b)
	}
	sort.Strings(out)
	return out
}

func seedIdentityWall(t *testing.T, st *Store, ctx context.Context, projectID int64, todos []Todo) WallNote {
	t.Helper()
	for i, todo := range todos {
		if _, _, err := st.PinWallStory(ctx, projectID, todo.LocalID, float64(100*(i+1)), float64(10*(i+1))); err != nil {
			t.Fatalf("pin %s: %v", todo.Title, err)
		}
	}
	note, _, err := st.CreateNote(ctx, projectID, CreateNoteInput{X: 5, Y: 6, Width: 200, Height: 120, Color: "#FFEE88", Text: "independent"})
	if err != nil {
		t.Fatal(err)
	}
	return note
}

func addIdentityEdge(t *testing.T, st *Store, ctx context.Context, projectID int64, from, to string) {
	t.Helper()
	if _, _, err := st.CreateEdge(ctx, projectID, from, to); err != nil {
		t.Fatalf("edge %s -> %s: %v", from, to, err)
	}
}

func TestImportMergeRemapsWallPlacementsAndStoryEdges(t *testing.T) {
	st, sqlDB, ctx, p := setupImportIdentityProject(t)
	todos := createIdentityTodos(t, st, ctx, p.ID, ModeFull, "one", "two", "three", "four")
	note := seedIdentityWall(t, st, ctx, p.ID, todos)
	addIdentityEdge(t, st, ctx, p.ID, FormatWallStoryEndpoint(todos[1].LocalID), FormatWallStoryEndpoint(todos[2].LocalID))
	addIdentityEdge(t, st, ctx, p.ID, note.ID, FormatWallStoryEndpoint(todos[3].LocalID))
	addIdentityEdge(t, st, ctx, p.ID, note.ID, FormatWallStoryEndpoint(todos[0].LocalID))
	data := exportIdentityProject(t, st, ctx, p.Slug)
	for _, todo := range []Todo{todos[1], todos[3]} {
		if err := st.DeleteTodoByLocalID(ctx, p.ID, todo.LocalID, ModeFull); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := st.ImportProjects(ctx, data, ModeFull, "merge"); err != nil {
		t.Fatalf("merge import: %v", err)
	}
	if localIDByTitle(t, sqlDB, p.ID, "two") == todos[1].LocalID || localIDByTitle(t, sqlDB, p.ID, "four") == todos[3].LocalID {
		t.Fatal("expected regenerated local ids for two and four")
	}
	assertStrings(t, "placements", wallPlacementsByTitle(t, sqlDB, p.ID), []string{
		"four@400,40",
		"one@100,10",
		"three@300,30",
		"two@200,20",
	})
	assertStrings(t, "edges", wallEdgesByTitle(t, st, sqlDB, p.ID), []string{
		"four ~ note",
		"note ~ one",
		"three ~ two",
	})
	wall, err := st.GetWall(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(wall.Notes) != 1 || wall.Notes[0].ID != note.ID || wall.Notes[0].X != 5 || wall.Notes[0].Text != "independent" {
		t.Fatalf("wall notes = %+v", wall.Notes)
	}
}

func TestImportMergeLegacyWallKeepsPlacementsAndMapsEdges(t *testing.T) {
	st, sqlDB, ctx, p := setupImportIdentityProject(t)
	todos := createIdentityTodos(t, st, ctx, p.ID, ModeFull, "one", "two", "three")
	note := seedIdentityWall(t, st, ctx, p.ID, []Todo{todos[0], todos[2]})
	addIdentityEdge(t, st, ctx, p.ID, FormatWallStoryEndpoint(todos[0].LocalID), FormatWallStoryEndpoint(todos[2].LocalID))
	addIdentityEdge(t, st, ctx, p.ID, note.ID, FormatWallStoryEndpoint(todos[2].LocalID))
	data := exportIdentityProject(t, st, ctx, p.Slug)
	data.Projects[0].Wall.Stories = nil
	data.Projects[0].Wall.StoriesPresent = false
	data.Projects[0].Wall.Edges = append(data.Projects[0].Wall.Edges, WallEdge{ID: "legacy-edge", From: note.ID, To: FormatWallStoryEndpoint(todos[1].LocalID)})
	if err := st.DeleteTodoByLocalID(ctx, p.ID, todos[1].LocalID, ModeFull); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PatchWallStory(ctx, p.ID, todos[0].LocalID, 0, 111, 222); err != nil {
		t.Fatal(err)
	}

	if _, err := st.ImportProjects(ctx, data, ModeFull, "merge"); err != nil {
		t.Fatalf("merge import: %v", err)
	}
	assertStrings(t, "placements", wallPlacementsByTitle(t, sqlDB, p.ID), []string{"one@111,222", "three@200,20"})
	assertStrings(t, "edges", wallEdgesByTitle(t, st, sqlDB, p.ID), []string{"note ~ three", "one ~ three"})
}

func TestImportCopyAndReplaceKeepWallAcrossOverlappingProjects(t *testing.T) {
	st, sqlDB, ctx, p := setupImportIdentityProject(t)
	todos := createIdentityTodos(t, st, ctx, p.ID, ModeFull, "one", "two")
	note := seedIdentityWall(t, st, ctx, p.ID, todos)
	addIdentityEdge(t, st, ctx, p.ID, FormatWallStoryEndpoint(todos[0].LocalID), FormatWallStoryEndpoint(todos[1].LocalID))
	addIdentityEdge(t, st, ctx, p.ID, note.ID, FormatWallStoryEndpoint(todos[1].LocalID))
	data := exportIdentityProject(t, st, ctx, p.Slug)
	second := data.Projects[0]
	second.Slug = "identity-wall-second"
	second.Name = "Second"
	second.Todos = append([]TodoExport(nil), second.Todos...)
	for i := range second.Todos {
		second.Todos[i].Title = "second " + second.Todos[i].Title
	}
	data.Projects = append(data.Projects, second)

	for _, mode := range []string{"copy", "replace"} {
		t.Run(mode, func(t *testing.T) {
			if _, err := st.ImportProjects(ctx, data, ModeFull, mode); err != nil {
				t.Fatalf("%s import: %v", mode, err)
			}
			for _, prefix := range []string{"", "second "} {
				var projectID int64
				if err := sqlDB.QueryRow(`SELECT MAX(project_id) FROM todos WHERE title = ?`, prefix+"one").Scan(&projectID); err != nil {
					t.Fatal(err)
				}
				assertStrings(t, prefix+"placements", wallPlacementsByTitle(t, sqlDB, projectID), []string{prefix + "one@100,10", prefix + "two@200,20"})
				assertStrings(t, prefix+"edges", wallEdgesByTitle(t, st, sqlDB, projectID), []string{"note ~ " + prefix + "two", prefix + "one ~ " + prefix + "two"})
			}
		})
	}
}

func TestImportRejectsDuplicateTodoLocalIDs(t *testing.T) {
	st, sqlDB, ctx, p := setupImportIdentityProject(t)
	todos := createIdentityTodos(t, st, ctx, p.ID, ModeFull, "one", "two")
	seedIdentityWall(t, st, ctx, p.ID, todos)
	data := exportIdentityProject(t, st, ctx, p.Slug)
	data.Projects[0].Todos[1].LocalID = data.Projects[0].Todos[0].LocalID

	board, err := st.CreateAnonymousBoard(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	before := importIdentitySnapshot(t, sqlDB)
	for _, mode := range []string{"copy", "merge", "replace", "board"} {
		t.Run(mode, func(t *testing.T) {
			var err error
			if mode == "board" {
				boardData := *data
				boardData.Mode, boardData.Scope = "anonymous", "board"
				_, err = st.ImportProjectsWithTarget(context.Background(), &boardData, ModeAnonymous, "copy", board.Slug)
			} else {
				_, err = st.ImportProjects(ctx, data, ModeFull, mode)
			}
			if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "duplicate") {
				t.Fatalf("import err = %v, want duplicate localId validation error", err)
			}
			if after := importIdentitySnapshot(t, sqlDB); after != before {
				t.Fatalf("rejected import changed data:\nbefore:\n%s\nafter:\n%s", before, after)
			}
		})
	}
	if _, err := st.PreviewImport(ctx, data, ModeFull, "merge"); !errors.Is(err, ErrValidation) {
		t.Fatalf("preview err = %v, want validation error", err)
	}
}

func TestImportMergeFailureRollsBackRemappedWall(t *testing.T) {
	st, sqlDB, ctx, p := setupImportIdentityProject(t)
	todos := createIdentityTodos(t, st, ctx, p.ID, ModeFull, "one", "two", "three")
	note := seedIdentityWall(t, st, ctx, p.ID, todos)
	addIdentityEdge(t, st, ctx, p.ID, note.ID, FormatWallStoryEndpoint(todos[1].LocalID))
	data := exportIdentityProject(t, st, ctx, p.Slug)
	data.Projects[0].Assets = nil
	if err := st.DeleteTodoByLocalID(ctx, p.ID, todos[1].LocalID, ModeFull); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	data.Projects = append(data.Projects, ProjectExport{
		Slug: "identity-broken", Name: "Broken", CreatedAt: now, UpdatedAt: now,
		CustomFields: []CustomFieldExport{{Key: "points", Name: "Points", Type: "number"}},
		Todos: []TodoExport{{LocalID: 1, Title: "broken", Status: "BACKLOG", Rank: 1000, Tags: []string{}, CreatedAt: now, UpdatedAt: now,
			CustomFields: map[string]any{"points": "not a number"}}},
	})

	before := importIdentitySnapshot(t, sqlDB)
	_, err := st.ImportProjects(ctx, data, ModeFull, "merge")
	if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "import custom fields") {
		t.Fatalf("merge import err = %v, want custom field validation error", err)
	}
	if after := importIdentitySnapshot(t, sqlDB); after != before {
		t.Fatalf("failed import changed data:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

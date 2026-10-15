package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"scrumboy/internal/eventbus"
	"scrumboy/internal/store"
)

type dueEventCollector struct {
	mu     sync.Mutex
	events []eventbus.Event
}

func (c *dueEventCollector) publish(_ context.Context, e eventbus.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
}

func (c *dueEventCollector) snapshot() []eventbus.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]eventbus.Event(nil), c.events...)
}

func dueSoonEvent(t *testing.T, assignee *int64, dueAt time.Time) eventbus.Event {
	t.Helper()
	payload, err := json.Marshal(eventbus.TodoDueSoonPayload{
		ProjectID: 7, ProjectSlug: "roadmap", TodoID: 11, LocalID: 4, Title: "Ship it",
		AssigneeUserID: assignee, AssigneeStableUserID: "assignee-public",
		DueAt: dueAt.UTC().Format(time.RFC3339), LeadHours: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	return eventbus.Event{Type: eventbus.TodoDueSoonEventType, ProjectID: 7, Payload: payload}
}

func configureDueReminderFake(st *emailNotifyFakeStore, dueAt time.Time) {
	due := dueAt.UTC()
	st.dueTodo.DueAt = &due
}

func TestEmailNotifier_DueSoon_EmailsAssignee(t *testing.T) {
	st := newEmailNotifyFake()
	q := newMailQueue(discardLogger())
	n := newEmailNotifier(st, q, "https://example.test", true, discardLogger())
	assignee := int64(2)
	due := time.Date(2026, 11, 1, 17, 0, 0, 0, time.UTC)
	configureDueReminderFake(st, due)

	n.OnEvent(context.Background(), dueSoonEvent(t, &assignee, due))
	items := drainAfterAsync(q)
	if len(items) != 1 {
		t.Fatalf("queued = %d, want 1", len(items))
	}
	mail := items[0]
	if mail.To != "assignee@example.com" {
		t.Errorf("to = %q", mail.To)
	}
	if mail.Subject != "Due soon: Ship it" {
		t.Errorf("subject = %q", mail.Subject)
	}
	for _, want := range []string{"Sun, 01 Nov 2026 17:00 UTC", "#4", "Roadmap", "https://example.test/roadmap/t/4"} {
		if !strings.Contains(mail.Body, want) {
			t.Errorf("body missing %q:\n%s", want, mail.Body)
		}
	}
}

func TestEmailNotifier_DueSoon_RespectsPreferencesAndRecipient(t *testing.T) {
	due := time.Now().Add(time.Hour)
	assignee := int64(2)

	t.Run("unassigned has no recipient", func(t *testing.T) {
		st := newEmailNotifyFake()
		configureDueReminderFake(st, due)
		q := newMailQueue(discardLogger())
		newEmailNotifier(st, q, "https://example.test", true, discardLogger()).OnEvent(context.Background(), dueSoonEvent(t, nil, due))
		if got := drainAfterAsync(q); len(got) != 0 {
			t.Fatalf("queued = %d, want 0", len(got))
		}
	})
	t.Run("master opt-in off", func(t *testing.T) {
		st := newEmailNotifyFake()
		configureDueReminderFake(st, due)
		pref := st.prefs[2]
		pref.Enabled = false
		st.prefs[2] = pref
		q := newMailQueue(discardLogger())
		newEmailNotifier(st, q, "https://example.test", true, discardLogger()).OnEvent(context.Background(), dueSoonEvent(t, &assignee, due))
		if got := drainAfterAsync(q); len(got) != 0 {
			t.Fatalf("queued = %d, want 0", len(got))
		}
	})
	t.Run("assigned category off", func(t *testing.T) {
		st := newEmailNotifyFake()
		configureDueReminderFake(st, due)
		pref := st.prefs[2]
		pref.Assigned = false
		st.prefs[2] = pref
		q := newMailQueue(discardLogger())
		newEmailNotifier(st, q, "https://example.test", true, discardLogger()).OnEvent(context.Background(), dueSoonEvent(t, &assignee, due))
		if got := drainAfterAsync(q); len(got) != 0 {
			t.Fatalf("queued = %d, want 0", len(got))
		}
	})
	t.Run("smtp not configured", func(t *testing.T) {
		st := newEmailNotifyFake()
		configureDueReminderFake(st, due)
		q := newMailQueue(discardLogger())
		newEmailNotifier(st, q, "https://example.test", false, discardLogger()).OnEvent(context.Background(), dueSoonEvent(t, &assignee, due))
		if got := drainAfterAsync(q); len(got) != 0 {
			t.Fatalf("queued = %d, want 0", len(got))
		}
	})
}

func TestEmailNotifier_DueSoon_RevalidatesBeforeEnqueueAndDelivery(t *testing.T) {
	due := time.Now().UTC().Add(6 * time.Hour).Truncate(time.Second)
	assignee := int64(2)
	event := dueSoonEvent(t, &assignee, due)

	mutations := map[string]func(*emailNotifyFakeStore){
		"membership removed": func(st *emailNotifyFakeStore) { st.roles[assignee] = "" },
		"reassigned": func(st *emailNotifyFakeStore) {
			other := int64(3)
			st.dueTodo.AssigneeUserID = &other
		},
		"due date changed": func(st *emailNotifyFakeStore) {
			changed := due.Add(time.Hour)
			st.dueTodo.DueAt = &changed
		},
		"recipient identity replaced": func(st *emailNotifyFakeStore) {
			user := st.users[assignee]
			user.PublicID = "replacement-public"
			st.users[assignee] = user
		},
		"preference changed": func(st *emailNotifyFakeStore) {
			pref := st.prefs[assignee]
			pref.Assigned = false
			st.prefs[assignee] = pref
		},
		"todo archived": func(st *emailNotifyFakeStore) {
			now := time.Now().UTC()
			st.dueTodo.ArchivedAt = &now
		},
		"todo completed": func(st *emailNotifyFakeStore) { st.dueDone = true },
	}

	for name, mutate := range mutations {
		t.Run(name+" before enqueue", func(t *testing.T) {
			st := newEmailNotifyFake()
			configureDueReminderFake(st, due)
			mutate(st)
			q := newMailQueue(discardLogger())
			newEmailNotifier(st, q, "https://example.test", true, discardLogger()).handleDueSoon(context.Background(), event)
			if got := q.Drain(); len(got) != 0 {
				t.Fatalf("queued after authorization was revoked: %+v", got)
			}
		})

		t.Run(name+" after enqueue", func(t *testing.T) {
			st := newEmailNotifyFake()
			configureDueReminderFake(st, due)
			q := newMailQueue(discardLogger())
			newEmailNotifier(st, q, "https://example.test", true, discardLogger()).handleDueSoon(context.Background(), event)
			queued := q.Drain()
			if len(queued) != 1 || queued[0].Prepare == nil {
				t.Fatalf("initial queue = %+v, want one deferred delivery", queued)
			}
			mutate(st)
			if prepared, ok, err := queued[0].Prepare(context.Background()); err != nil || ok {
				t.Fatalf("Prepare after authorization was revoked = (%+v, %v, %v), want dropped", prepared, ok, err)
			}
		})
	}
}

func TestEmailNotifier_DueSoon_AuthorizedDeliveryUsesCurrentData(t *testing.T) {
	due := time.Now().UTC().Add(6 * time.Hour).Truncate(time.Second)
	assignee := int64(2)
	st := newEmailNotifyFake()
	configureDueReminderFake(st, due)
	st.dueTodo.Title = "Current title"
	q := newMailQueue(discardLogger())
	newEmailNotifier(st, q, "https://example.test", true, discardLogger()).handleDueSoon(context.Background(), dueSoonEvent(t, &assignee, due))
	queued := q.Drain()
	if len(queued) != 1 || queued[0].Prepare == nil {
		t.Fatalf("initial queue = %+v, want one deferred delivery", queued)
	}
	st.dueTodo.Title = "Newest authorized title"
	prepared, ok, err := queued[0].Prepare(context.Background())
	if err != nil || !ok {
		t.Fatalf("Prepare = (%+v, %v, %v), want authorized", prepared, ok, err)
	}
	if prepared.To != "assignee@example.com" || prepared.Subject != "Due soon: Newest authorized title" ||
		!strings.Contains(prepared.Body, "#4 Newest authorized title") {
		t.Fatalf("prepared delivery did not use current authorized data: %+v", prepared)
	}
}

func TestDueReminderScheduler_PublishesOncePerDueDate(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	owner, err := st.BootstrapUser(ctx, "owner@example.com", "pass1234A!", "Owner")
	if err != nil {
		t.Fatal(err)
	}
	ownerCtx := store.WithUserID(ctx, owner.ID)
	project, err := st.CreateProject(ownerCtx, "Reminders")
	if err != nil {
		t.Fatal(err)
	}
	due := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second).Add(123 * time.Millisecond)
	todo, err := st.CreateTodo(ownerCtx, project.ID, store.CreateTodoInput{
		Title: "Ship it", ColumnKey: store.DefaultColumnDoing, DueAt: &due, AssigneeUserID: &owner.ID,
	}, store.ModeFull)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateTodo(ownerCtx, project.ID, store.CreateTodoInput{Title: "No due", ColumnKey: store.DefaultColumnDoing}, store.ModeFull); err != nil {
		t.Fatal(err)
	}

	collector := &dueEventCollector{}
	scheduler := newDueReminderScheduler(st, collector.publish, 10*time.Millisecond, discardLogger())
	runCtx, cancel := context.WithCancel(ctx)
	go scheduler.Run(runCtx)
	time.Sleep(300 * time.Millisecond) // many ticks
	cancel()
	select {
	case <-scheduler.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler did not stop after cancel")
	}

	events := collector.snapshot()
	if len(events) != 1 {
		t.Fatalf("published %d events, want exactly 1", len(events))
	}
	if events[0].Type != eventbus.TodoDueSoonEventType || events[0].ProjectID != project.ID {
		t.Fatalf("event = %+v", events[0])
	}
	var payload eventbus.TodoDueSoonPayload
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.TodoID != todo.ID || payload.LocalID != todo.LocalID || payload.Title != "Ship it" ||
		payload.ProjectSlug != project.Slug || payload.LeadHours != store.DefaultDueReminderLeadHours ||
		payload.DueAt != due.Format(time.RFC3339Nano) {
		t.Fatalf("payload = %+v", payload)
	}
	if payload.AssigneeUserID == nil || *payload.AssigneeUserID != owner.ID {
		t.Fatalf("assignee = %v, want %d", payload.AssigneeUserID, owner.ID)
	}
	if payload.AssigneeStableUserID == "" {
		t.Error("stable assignee id missing")
	}
}

func TestAdminDueReminder_SettingAndAuthorization(t *testing.T) {
	ts, _, cleanup := newTestHTTPServer(t, "full")
	defer cleanup()
	owner := newCookieClient(t)
	bootstrapUserClient(t, owner, ts.URL, "Owner", "owner@example.com", "password123")
	url := ts.URL + "/api/admin/settings/due-reminder"

	var out map[string]any
	if resp, body := doJSON(t, owner, http.MethodGet, url, nil, &out); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", resp.StatusCode, body)
	}
	if out["leadHours"] != float64(24) || out["customized"] != false {
		t.Fatalf("default = %v", out)
	}

	if resp, _ := doJSON(t, owner, http.MethodPut, url, map[string]any{"leadHours": 48}, &out); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status=%d", resp.StatusCode)
	}
	_, _ = doJSON(t, owner, http.MethodGet, url, nil, &out)
	if out["leadHours"] != float64(48) || out["customized"] != true {
		t.Fatalf("after PUT = %v", out)
	}

	// 0 disables; out-of-range and missing are rejected.
	if resp, _ := doJSON(t, owner, http.MethodPut, url, map[string]any{"leadHours": 0}, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT 0 status=%d", resp.StatusCode)
	}
	for name, body := range map[string]map[string]any{
		"negative": {"leadHours": -1},
		"too big":  {"leadHours": store.MaxDueReminderLeadHours + 1},
		"missing":  {},
	} {
		if resp, _ := doJSON(t, owner, http.MethodPut, url, body, nil); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status=%d, want 400", name, resp.StatusCode)
		}
	}

	if resp, _ := doJSON(t, owner, http.MethodDelete, url, nil, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE status=%d", resp.StatusCode)
	}
	_, _ = doJSON(t, owner, http.MethodGet, url, nil, &out)
	if out["leadHours"] != float64(24) || out["customized"] != false {
		t.Fatalf("after DELETE = %v", out)
	}

	// A plain user cannot change it.
	if resp, _ := doJSON(t, owner, http.MethodPost, ts.URL+"/api/admin/users", map[string]any{
		"name": "Plain", "email": "plain@example.com", "password": "password123",
	}, nil); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create plain user status=%d", resp.StatusCode)
	}
	plain := newCookieClient(t)
	loginUserClient(t, plain, ts.URL, "plain@example.com", "password123")
	if resp, _ := doJSON(t, plain, http.MethodPut, url, map[string]any{"leadHours": 1}, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("plain PUT status=%d, want 403", resp.StatusCode)
	}
}

func TestTodoDueAt_RESTCreateUpdateClear(t *testing.T) {
	ts, _, cleanup := newTestHTTPServer(t, "full")
	defer cleanup()
	client := newCookieClient(t)
	bootstrapUserClient(t, client, ts.URL, "Owner", "owner@example.com", "password123")

	var project struct {
		Slug string `json:"slug"`
	}
	if resp, body := doJSON(t, client, http.MethodPost, ts.URL+"/api/projects", map[string]any{"name": "Due dates"}, &project); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create project status=%d body=%s", resp.StatusCode, body)
	}

	payload := todoCreateRESTPayload("with due date")
	payload["dueAt"] = "2026-11-01T17:00:00Z"
	created := createTodoThroughREST(t, client, ts.URL, project.Slug, payload)
	if created.DueAt == nil || created.DueAt.UTC().Format(time.RFC3339) != "2026-11-01T17:00:00Z" {
		t.Fatalf("created dueAt = %v", created.DueAt)
	}

	patchURL := ts.URL + "/api/board/" + project.Slug + "/todos/" + itoa(created.LocalID)
	patch := func(body map[string]any) todoJSON {
		t.Helper()
		body["assigneeUserId"] = nil
		var out todoJSON
		resp, raw := doJSON(t, client, http.MethodPatch, patchURL, body, &out)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("PATCH status=%d body=%s", resp.StatusCode, raw)
		}
		return out
	}

	// Omitted dueAt preserves it.
	if got := patch(map[string]any{"title": "renamed", "body": "b", "tags": []string{}}); got.DueAt == nil {
		t.Fatal("omitted dueAt cleared the due date")
	}
	// Explicit value replaces it.
	got := patch(map[string]any{"title": "renamed", "body": "b", "tags": []string{}, "dueAt": "2026-12-24T08:00:00Z"})
	if got.DueAt == nil || got.DueAt.UTC().Format(time.RFC3339) != "2026-12-24T08:00:00Z" {
		t.Fatalf("after set dueAt = %v", got.DueAt)
	}
	// Explicit null clears it.
	if got := patch(map[string]any{"title": "renamed", "body": "b", "tags": []string{}, "dueAt": nil}); got.DueAt != nil {
		t.Fatalf("after clear dueAt = %v", got.DueAt)
	}

	// Malformed and out-of-range values are rejected.
	for name, v := range map[string]any{"not a date": "tomorrow", "year 2100": "2100-01-01T00:00:00Z", "epoch": "1970-01-01T00:00:00Z"} {
		body := map[string]any{"title": "t", "body": "b", "tags": []string{}, "assigneeUserId": nil, "dueAt": v}
		if resp, _ := doJSON(t, client, http.MethodPatch, patchURL, body, nil); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: PATCH status=%d, want 400", name, resp.StatusCode)
		}
	}
}

func TestServerClose_StopsReminderSchedulerBeforeConsumers(t *testing.T) {
	st := newTestStore(t)
	srv := NewServer(st, Options{MaxRequestBody: 1 << 20, ScrumboyMode: "full", DueReminderInterval: 5 * time.Millisecond})
	if srv.dueReminderDone == nil || srv.dueReminderCancel == nil {
		t.Fatal("scheduler was not started in full mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Close(ctx)

	select {
	case <-srv.dueReminderDone:
	default:
		t.Fatal("Close returned before the reminder scheduler finished")
	}
}

func TestServer_NoReminderSchedulerInAnonymousMode(t *testing.T) {
	st := newTestStore(t)
	srv := NewServer(st, Options{MaxRequestBody: 1 << 20, ScrumboyMode: "anonymous"})
	if srv.dueReminderDone != nil || srv.dueReminderCancel != nil {
		t.Fatal("scheduler must not run in anonymous mode")
	}
	srv.Close(context.Background())
}

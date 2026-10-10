package httpapi

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"scrumboy/internal/eventbus"
	"scrumboy/internal/store"
)

type webhookDeliveryAuthorizerFunc func(context.Context, int64) (store.WebhookDeliveryTarget, bool, error)

func (f webhookDeliveryAuthorizerFunc) GetWebhookDeliveryTarget(ctx context.Context, webhookID int64) (store.WebhookDeliveryTarget, bool, error) {
	return f(ctx, webhookID)
}

func TestWebhookDeliveryAuthorizationRevalidatesQueuedSubscriptions(t *testing.T) {
	_, st, _ := newTestServerWithCollector(t)
	ownerCtx, owner, project := setupAuthenticatedProject(t, st)

	var hitsMu sync.Mutex
	hits := map[string]int{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitsMu.Lock()
		hits[r.URL.Path]++
		hitsMu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()
	client, publicURL := webhookClientToTestServer(t, ts, "authorized-hooks.example")

	type subscription struct {
		user store.User
		hook store.Webhook
	}
	create := func(name, path string, events []string) subscription {
		t.Helper()
		user, err := st.CreateUser(context.Background(), name+"@example.com", "pass1234A!", name)
		if err != nil {
			t.Fatalf("create %s user: %v", name, err)
		}
		if err := st.AddProjectMember(ownerCtx, owner.ID, project.ID, user.ID, store.RoleMaintainer); err != nil {
			t.Fatalf("add %s maintainer: %v", name, err)
		}
		hook, err := st.CreateWebhook(store.WithUserID(context.Background(), user.ID), user.ID, store.CreateWebhookInput{
			ProjectID: project.ID,
			URL:       publicURL + path,
			Events:    events,
		})
		if err != nil {
			t.Fatalf("create %s webhook: %v", name, err)
		}
		return subscription{user: user, hook: hook}
	}

	create("current", "/current", []string{"todo.updated"})
	removed := create("removed", "/removed", []string{"todo.updated"})
	demoted := create("demoted", "/demoted", []string{"todo.updated"})
	deletedSubscription := create("deleted-subscription", "/deleted-subscription", []string{"todo.updated"})
	deletedAccount := create("deleted-account", "/deleted-account", []string{"todo.updated"})
	create("other", "/other", []string{"todo.updated"})
	create("wildcard", "/wildcard", []string{"*"})

	queue := newWebhookQueue(log.New(io.Discard, "", 0))
	dispatcher := newWebhookDispatcher(st, queue, log.New(io.Discard, "", 0))
	worker := newWebhookWorkerWithClient(st, queue, log.New(io.Discard, "", 0), client)
	event := eventbus.Event{
		ID: "queued-before-revocation", Type: "todo.updated", ProjectID: project.ID,
		Time: time.Now().UTC(), Payload: []byte(`{"todoId":1}`),
	}

	// Queue while every subscription owner is still an authorized Maintainer.
	dispatcher.enqueueWork(event)

	if err := st.RemoveProjectMember(ownerCtx, owner.ID, project.ID, removed.user.ID); err != nil {
		t.Fatalf("remove maintainer: %v", err)
	}
	if err := st.UpdateProjectMemberRole(ownerCtx, owner.ID, project.ID, demoted.user.ID, store.RoleContributor); err != nil {
		t.Fatalf("demote maintainer: %v", err)
	}
	if err := st.DeleteWebhook(context.Background(), deletedSubscription.user.ID, deletedSubscription.hook.ID); err != nil {
		t.Fatalf("delete subscription: %v", err)
	}
	if err := st.DeleteUser(context.Background(), owner.ID, deletedAccount.user.ID); err != nil {
		t.Fatalf("delete account: %v", err)
	}

	worker.flush()
	assertHits := func(path string, want int) {
		t.Helper()
		hitsMu.Lock()
		defer hitsMu.Unlock()
		if got := hits[path]; got != want {
			t.Fatalf("hits[%s]=%d want %d; all hits=%v", path, got, want, hits)
		}
	}
	assertHits("/current", 1)
	assertHits("/other", 1)
	assertHits("/wildcard", 1)
	assertHits("/removed", 0)
	assertHits("/demoted", 0)
	assertHits("/deleted-subscription", 0)
	assertHits("/deleted-account", 0)

	// Future dispatch also filters ineligible owners, while unrelated authorized
	// subscriptions and wildcard matching continue normally.
	event.ID = "after-revocation"
	dispatcher.enqueueWork(event)
	worker.flush()
	assertHits("/current", 2)
	assertHits("/other", 2)
	assertHits("/wildcard", 2)
	assertHits("/removed", 0)
	assertHits("/demoted", 0)
}

func TestWebhookDeliveryAuthorizationLookupFailureFailsClosed(t *testing.T) {
	var httpHits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httpHits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()
	client, publicURL := webhookClientToTestServer(t, ts, "lookup-failure.example")

	var lookups atomic.Int32
	authorizer := webhookDeliveryAuthorizerFunc(func(context.Context, int64) (store.WebhookDeliveryTarget, bool, error) {
		lookups.Add(1)
		return store.WebhookDeliveryTarget{}, false, errors.New("temporary database failure")
	})
	queue := newWebhookQueue(log.New(io.Discard, "", 0))
	worker := newWebhookWorkerWithClient(authorizer, queue, log.New(io.Discard, "", 0), client)
	queue.Enqueue(webhookDelivery{
		WebhookID: 44, URL: publicURL, EventID: "auth-failure", EventType: "todo.updated", Body: []byte(`{}`),
	})
	worker.flush()

	if got := httpHits.Load(); got != 0 {
		t.Fatalf("authorization failure leaked %d HTTP deliveries", got)
	}
	if got := lookups.Load(); got != int32(len(deliveryBackoff)) {
		t.Fatalf("authorization lookups=%d want retry count %d", got, len(deliveryBackoff))
	}
}

func TestWebhookWorkerReauthorizesEachOrdinaryRetry(t *testing.T) {
	var httpHits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if httpHits.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()
	client, publicURL := webhookClientToTestServer(t, ts, "retry-hooks.example")

	var lookups atomic.Int32
	authorizer := webhookDeliveryAuthorizerFunc(func(context.Context, int64) (store.WebhookDeliveryTarget, bool, error) {
		lookups.Add(1)
		return store.WebhookDeliveryTarget{URL: publicURL}, true, nil
	})
	queue := newWebhookQueue(log.New(io.Discard, "", 0))
	worker := newWebhookWorkerWithClient(authorizer, queue, log.New(io.Discard, "", 0), client)
	queue.Enqueue(webhookDelivery{
		WebhookID: 55, URL: publicURL, EventID: "ordinary-retry", EventType: "todo.updated", Body: []byte(`{}`),
	})
	worker.flush()

	if got := httpHits.Load(); got != 3 {
		t.Fatalf("HTTP attempts=%d want 3", got)
	}
	if got := lookups.Load(); got != 3 {
		t.Fatalf("authorization lookups=%d want one per attempt", got)
	}
}

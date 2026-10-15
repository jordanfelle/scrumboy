package httpapi

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"scrumboy/internal/eventbus"
	"scrumboy/internal/store"
)

const (
	defaultDueReminderInterval = time.Minute
	// dueReminderBatch bounds one claim. A full batch triggers an immediate
	// follow-up claim, so a large backlog drains without waiting a tick.
	dueReminderBatch = 200
)

type dueReminderStore interface {
	ClaimDueReminders(ctx context.Context, now time.Time, limit int) ([]store.DueReminder, error)
}

// dueReminderScheduler periodically claims todos that entered their reminder
// window and publishes one todo.due_soon event for each. Delivery channels
// (email, webhooks) subscribe to the event; the scheduler knows nothing about
// them.
type dueReminderScheduler struct {
	store    dueReminderStore
	publish  func(context.Context, eventbus.Event)
	interval time.Duration
	now      func() time.Time
	logger   *log.Logger
	done     chan struct{}
}

func newDueReminderScheduler(st dueReminderStore, publish func(context.Context, eventbus.Event), interval time.Duration, logger *log.Logger) *dueReminderScheduler {
	if interval <= 0 {
		interval = defaultDueReminderInterval
	}
	return &dueReminderScheduler{
		store:    st,
		publish:  publish,
		interval: interval,
		now:      time.Now,
		logger:   logger,
		done:     make(chan struct{}),
	}
}

// Done is closed after Run returns.
func (d *dueReminderScheduler) Done() <-chan struct{} { return d.done }

// Run ticks until ctx is cancelled. The first tick fires after one interval,
// not at startup, so a restart loop cannot hammer the database.
func (d *dueReminderScheduler) Run(ctx context.Context) {
	defer close(d.done)
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.tick(ctx)
		}
	}
}

func (d *dueReminderScheduler) tick(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		claimed, err := d.store.ClaimDueReminders(ctx, d.now(), dueReminderBatch)
		if err != nil {
			if ctx.Err() == nil {
				d.logger.Printf("due reminders: claim: %v", err)
			}
			return
		}
		for _, reminder := range claimed {
			d.publishReminder(ctx, reminder)
		}
		// A full page means more may be waiting; drain it now. Overdue rows are
		// marked separately and never count toward the page.
		if len(claimed) < dueReminderBatch {
			return
		}
	}
}

func (d *dueReminderScheduler) publishReminder(ctx context.Context, r store.DueReminder) {
	if r.Todo.DueAt == nil {
		return
	}
	payload, err := json.Marshal(eventbus.TodoDueSoonPayload{
		ProjectID:            r.Todo.ProjectID,
		ProjectSlug:          r.ProjectSlug,
		TodoID:               r.Todo.ID,
		LocalID:              r.Todo.LocalID,
		Title:                r.Todo.Title,
		ColumnKey:            r.Todo.ColumnKey,
		AssigneeUserID:       r.Todo.AssigneeUserID,
		AssigneeStableUserID: r.AssigneeStableUserID,
		DueAt:                r.Todo.DueAt.UTC().Format(time.RFC3339Nano),
		LeadHours:            r.LeadHours,
	})
	if err != nil {
		return
	}
	d.publish(ctx, eventbus.Event{
		Type:      eventbus.TodoDueSoonEventType,
		ProjectID: r.Todo.ProjectID,
		Payload:   payload,
	})
}

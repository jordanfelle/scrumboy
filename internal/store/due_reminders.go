package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// orgSettingDueReminderLeadHours is the org_settings key holding how many hours
// before a todo's due date its reminder fires.
const orgSettingDueReminderLeadHours = "dueReminderLeadHours"

const (
	// DefaultDueReminderLeadHours applies until an admin configures a value.
	DefaultDueReminderLeadHours = 24
	// MaxDueReminderLeadHours caps the lead time at 90 days.
	MaxDueReminderLeadHours = 24 * 90
)

// GetDueReminderLeadHours returns the configured reminder lead time in hours.
// 0 means reminders are disabled. customized reports whether an admin has set a
// value rather than the caller seeing DefaultDueReminderLeadHours.
func (s *Store) GetDueReminderLeadHours(ctx context.Context) (hours int, customized bool, err error) {
	raw, err := s.GetOrgSetting(ctx, orgSettingDueReminderLeadHours)
	if err != nil {
		return 0, false, err
	}
	if strings.TrimSpace(raw) == "" {
		return DefaultDueReminderLeadHours, false, nil
	}
	hours, err = parseDueReminderLeadHours(raw)
	if err != nil {
		return 0, false, err
	}
	return hours, true, nil
}

func parseDueReminderLeadHours(raw string) (int, error) {
	hours, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || hours < 0 || hours > MaxDueReminderLeadHours {
		return 0, fmt.Errorf("%w: due reminder lead time must be a whole number of hours between 0 and %d", ErrValidation, MaxDueReminderLeadHours)
	}
	return hours, nil
}

// SetDueReminderLeadHours sets the org-wide reminder lead time. 0 disables
// reminders. Requires admin or owner role.
func (s *Store) SetDueReminderLeadHours(ctx context.Context, requesterID int64, hours int) error {
	if err := s.requireAdmin(ctx, requesterID); err != nil {
		return err
	}
	if _, err := parseDueReminderLeadHours(strconv.Itoa(hours)); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin set due reminder lead time tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := setOrgSettingTx(ctx, tx, orgSettingDueReminderLeadHours, strconv.Itoa(hours)); err != nil {
		return err
	}
	return tx.Commit()
}

// ClearDueReminderLeadHours removes the admin override, returning to
// DefaultDueReminderLeadHours. Requires admin or owner role.
func (s *Store) ClearDueReminderLeadHours(ctx context.Context, requesterID int64) error {
	if err := s.requireAdmin(ctx, requesterID); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM org_settings WHERE key = ?`, orgSettingDueReminderLeadHours); err != nil {
		return fmt.Errorf("clear due reminder lead time: %w", err)
	}
	return nil
}

// DueReminder is one todo whose reminder was just claimed.
type DueReminder struct {
	Todo        Todo
	ProjectSlug string
	// AssigneeStableUserID is users.public_id of the assignee, empty when unassigned.
	AssigneeStableUserID string
	LeadHours            int
}

// ClaimDueReminders marks every pending todo that has entered its reminder
// window as reminded, and returns the ones that are still upcoming. The mark
// and the read happen in one transaction, so a todo is claimed at most once per
// due date even if the caller crashes before delivering (delivery is
// at-most-once by design: a stale reminder is worse than a missed one).
//
// A todo is pending when it has a due date, has not been reminded for that
// date and is not archived. Pending todos whose due date has already passed at
// claim time are first marked without being returned, however many there are:
// that covers due dates set in the past, dates imported from a backup, and
// cards left in done columns, which would otherwise produce a burst of
// "overdue" notices nobody asked for and pile up in the pending index. The
// remaining claim returns only upcoming todos outside a done column in a
// durable project, at most limit per call.
func (s *Store) ClaimDueReminders(ctx context.Context, now time.Time, limit int) ([]DueReminder, error) {
	leadHours, _, err := s.GetDueReminderLeadHours(ctx)
	if err != nil {
		return nil, err
	}
	if leadHours == 0 || limit <= 0 {
		return nil, nil
	}
	nowMs := now.UTC().UnixMilli()
	windowEndMs := now.UTC().Add(time.Duration(leadHours) * time.Hour).UnixMilli()

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin claim due reminders: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Step 1: silently mark everything whose due date has already passed (including
	// cards left in done columns), with no limit. Doing this first keeps stale rows
	// from filling the capped claim below, and keeps them out of the pending index.
	if _, err := tx.ExecContext(ctx, `
UPDATE todos SET due_reminded_at = ?
WHERE due_at IS NOT NULL
  AND due_reminded_at IS NULL
  AND archived_at IS NULL
  AND due_at <= ?`, nowMs, nowMs); err != nil {
		return nil, fmt.Errorf("mark overdue due reminders: %w", err)
	}

	// Step 2: claim upcoming todos inside the window, capped at limit.
	rows, err := tx.QueryContext(ctx, `
UPDATE todos SET due_reminded_at = ?
WHERE id IN (
  SELECT t.id
  FROM todos t
  JOIN projects p ON p.id = t.project_id
  JOIN project_workflow_columns c ON c.project_id = t.project_id AND c.key = t.column_key
  WHERE t.due_at IS NOT NULL
    AND t.due_reminded_at IS NULL
    AND t.archived_at IS NULL
    AND t.due_at > ?
    AND t.due_at <= ?
    AND c.is_done = 0
    AND p.expires_at IS NULL
  ORDER BY t.due_at, t.id
  LIMIT ?
)
RETURNING id, due_at`, nowMs, nowMs, windowEndMs, limit)
	if err != nil {
		return nil, fmt.Errorf("claim due reminders: %w", err)
	}
	var upcoming []int64
	for rows.Next() {
		var id, dueAtMs int64
		if err := rows.Scan(&id, &dueAtMs); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan claimed due reminder: %w", err)
		}
		upcoming = append(upcoming, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("claimed due reminders rows: %w", err)
	}
	_ = rows.Close()

	out := make([]DueReminder, 0, len(upcoming))
	for _, id := range upcoming {
		todo, err := getTodoTx(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		var slug string
		if err := tx.QueryRowContext(ctx, `SELECT slug FROM projects WHERE id = ?`, todo.ProjectID).Scan(&slug); err != nil {
			return nil, fmt.Errorf("due reminder project slug: %w", err)
		}
		reminder := DueReminder{Todo: todo, ProjectSlug: slug, LeadHours: leadHours}
		if todo.AssigneeUserID != nil {
			ids, err := queryUserPublicIDs(ctx, tx, *todo.AssigneeUserID)
			if err != nil {
				return nil, err
			}
			reminder.AssigneeStableUserID = ids[*todo.AssigneeUserID]
		}
		out = append(out, reminder)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit claim due reminders: %w", err)
	}
	return out, nil
}

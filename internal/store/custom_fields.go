package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// CustomFieldType is the value type of a project custom field.
type CustomFieldType string

const (
	CustomFieldText   CustomFieldType = "text"
	CustomFieldURL    CustomFieldType = "url"
	CustomFieldNumber CustomFieldType = "number"
)

const (
	maxCustomFieldsPerProject = 25
	maxCustomFieldNameLen     = 100
	maxCustomFieldValueLen    = 2000
	// maxCustomFieldNumber is the largest magnitude a float64 holds exactly.
	maxCustomFieldNumber = 1 << 53
)

// ValidCustomFieldType reports whether t is a supported field type.
func ValidCustomFieldType(t CustomFieldType) bool {
	return t == CustomFieldText || t == CustomFieldURL || t == CustomFieldNumber
}

// CustomField is a project-scoped field definition.
type CustomField struct {
	ID        int64
	ProjectID int64
	Key       string
	Name      string
	Type      CustomFieldType
	Position  int
}

// CustomFieldValues maps a field key to its value: a string for text/url
// fields, a float64 for number fields. Fields without a value are absent.
type CustomFieldValues map[string]any

func cloneCustomFieldValues(v CustomFieldValues) CustomFieldValues {
	if v == nil {
		return nil
	}
	out := make(CustomFieldValues, len(v))
	for k, val := range v {
		out[k] = val
	}
	return out
}

func listCustomFieldsQ(ctx context.Context, q sqlRowsQueryer, projectID int64) ([]CustomField, error) {
	rows, err := q.QueryContext(ctx, `
SELECT id, project_id, key, name, type, position
FROM project_custom_fields
WHERE project_id = ?
ORDER BY position, id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list custom fields: %w", err)
	}
	defer rows.Close()
	out := []CustomField{}
	for rows.Next() {
		var f CustomField
		var typ string
		if err := rows.Scan(&f.ID, &f.ProjectID, &f.Key, &f.Name, &typ, &f.Position); err != nil {
			return nil, fmt.Errorf("scan custom field: %w", err)
		}
		f.Type = CustomFieldType(typ)
		out = append(out, f)
	}
	return out, rows.Err()
}

// ListProjectCustomFields returns a project's field definitions in position order.
func (s *Store) ListProjectCustomFields(ctx context.Context, projectID int64) ([]CustomField, error) {
	return listCustomFieldsQ(ctx, s.db, projectID)
}

// CreateCustomField adds a field definition. key may be empty, in which case
// it is derived from the name. Callers enforce the maintainer role.
func (s *Store) CreateCustomField(ctx context.Context, projectID int64, key, name string, typ CustomFieldType) (CustomField, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > maxCustomFieldNameLen {
		return CustomField{}, fmt.Errorf("%w: invalid custom field name", ErrValidation)
	}
	if !ValidCustomFieldType(typ) {
		return CustomField{}, fmt.Errorf("%w: custom field type must be text, url or number", ErrValidation)
	}
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" {
		key = workflowKeyFromName(name)
	}
	if !isValidColumnKey(key) {
		return CustomField{}, fmt.Errorf("%w: invalid custom field key", ErrValidation)
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return CustomField{}, fmt.Errorf("begin create custom field: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := serializeProjectWriteTx(ctx, tx, projectID); err != nil {
		return CustomField{}, err
	}
	existing, err := listCustomFieldsQ(ctx, tx, projectID)
	if err != nil {
		return CustomField{}, err
	}
	if len(existing) >= maxCustomFieldsPerProject {
		return CustomField{}, fmt.Errorf("%w: project may have at most %d custom fields", ErrValidation, maxCustomFieldsPerProject)
	}
	position := 0
	for _, f := range existing {
		if f.Key == key {
			return CustomField{}, fmt.Errorf("%w: custom field key already exists", ErrConflict)
		}
		if f.Position >= position {
			position = f.Position + 1
		}
	}
	res, err := tx.ExecContext(ctx, `
INSERT INTO project_custom_fields(project_id, key, name, type, position)
VALUES (?, ?, ?, ?, ?)`, projectID, key, name, string(typ), position)
	if err != nil {
		return CustomField{}, fmt.Errorf("insert custom field: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return CustomField{}, fmt.Errorf("custom field id: %w", err)
	}
	if err := touchProject(ctx, tx, projectID, time.Now().UTC().UnixMilli()); err != nil {
		return CustomField{}, err
	}
	if err := insertAuditEventTx(ctx, tx, projectID, auditActorFromContext(ctx), "custom_field_created", "custom_field", &id, map[string]any{"key": key, "type": string(typ)}); err != nil {
		return CustomField{}, fmt.Errorf("audit custom_field_created: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return CustomField{}, fmt.Errorf("commit create custom field: %w", err)
	}
	return CustomField{ID: id, ProjectID: projectID, Key: key, Name: name, Type: typ, Position: position}, nil
}

// DeleteCustomField removes a definition and every todo value stored under it.
func (s *Store) DeleteCustomField(ctx context.Context, projectID int64, key string) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin delete custom field: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := serializeProjectWriteTx(ctx, tx, projectID); err != nil {
		return err
	}
	key = strings.ToLower(strings.TrimSpace(key))
	var fieldID int64
	var valueCount int64
	if err := tx.QueryRowContext(ctx, `
SELECT f.id, (SELECT COUNT(*) FROM todo_custom_field_values v WHERE v.field_id = f.id)
FROM project_custom_fields f WHERE f.project_id = ? AND f.key = ?`, projectID, key).Scan(&fieldID, &valueCount); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("find custom field: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM project_custom_fields WHERE id = ?`, fieldID); err != nil {
		return fmt.Errorf("delete custom field: %w", err)
	}
	if err := insertAuditEventTx(ctx, tx, projectID, auditActorFromContext(ctx), "custom_field_deleted", "custom_field", &fieldID, map[string]any{"key": key, "values_removed": valueCount}); err != nil {
		return fmt.Errorf("audit custom_field_deleted: %w", err)
	}
	if err := touchProject(ctx, tx, projectID, time.Now().UTC().UnixMilli()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit delete custom field: %w", err)
	}
	return nil
}

// normalizeCustomFieldValue validates v against the field type and returns the
// canonical stored value (string or float64).
func normalizeCustomFieldValue(f CustomField, v any) (any, error) {
	bad := func(msg string) error {
		return fmt.Errorf("%w: custom field %q: %s", ErrValidation, f.Key, msg)
	}
	switch f.Type {
	case CustomFieldText:
		str, ok := v.(string)
		if !ok {
			return nil, bad("expected a string")
		}
		str = strings.TrimSpace(str)
		if str == "" {
			return nil, bad("value cannot be empty (use null to clear)")
		}
		if utf8.RuneCountInString(str) > maxCustomFieldValueLen {
			return nil, bad("value too long")
		}
		return str, nil
	case CustomFieldURL:
		str, ok := v.(string)
		if !ok {
			return nil, bad("expected a string")
		}
		str = strings.TrimSpace(str)
		if utf8.RuneCountInString(str) > maxCustomFieldValueLen {
			return nil, bad("value too long")
		}
		u, err := url.Parse(str)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, bad("expected an http(s) URL")
		}
		return str, nil
	case CustomFieldNumber:
		var n float64
		switch x := v.(type) {
		case float64:
			n = x
		case int:
			n = float64(x)
		case int64:
			n = float64(x)
		default:
			return nil, bad("expected a number")
		}
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return nil, bad("expected a finite number")
		}
		if math.Abs(n) >= maxCustomFieldNumber {
			return nil, bad("number out of range (must be below 2^53)")
		}
		return n, nil
	}
	return nil, bad("unknown field type")
}

type resolvedCustomField struct {
	fieldID int64
	value   any // nil clears
}

// resolveCustomFieldPatchTx validates a sparse key->value patch (nil clears)
// against the project's definitions.
func resolveCustomFieldPatchTx(ctx context.Context, tx *sql.Tx, projectID int64, patch CustomFieldValues) (map[string]resolvedCustomField, error) {
	defs, err := listCustomFieldsQ(ctx, tx, projectID)
	if err != nil {
		return nil, err
	}
	byKey := make(map[string]CustomField, len(defs))
	for _, d := range defs {
		byKey[d.Key] = d
	}
	out := make(map[string]resolvedCustomField, len(patch))
	for key, raw := range patch {
		def, ok := byKey[key]
		if !ok {
			return nil, fmt.Errorf("%w: unknown custom field %q", ErrValidation, key)
		}
		if raw == nil {
			out[key] = resolvedCustomField{fieldID: def.ID}
			continue
		}
		val, err := normalizeCustomFieldValue(def, raw)
		if err != nil {
			return nil, err
		}
		out[key] = resolvedCustomField{fieldID: def.ID, value: val}
	}
	return out, nil
}

// applyCustomFieldPatchTx writes a resolved patch. It returns the sorted keys
// whose value actually changed and the todo's resulting values.
func applyCustomFieldPatchTx(ctx context.Context, tx *sql.Tx, todoID int64, current CustomFieldValues, patch map[string]resolvedCustomField) ([]string, CustomFieldValues, error) {
	next := cloneCustomFieldValues(current)
	var changed []string
	for key, r := range patch {
		old, had := current[key]
		if r.value == nil {
			if !had {
				continue
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM todo_custom_field_values WHERE todo_id = ? AND field_id = ?`, todoID, r.fieldID); err != nil {
				return nil, nil, fmt.Errorf("clear custom field value: %w", err)
			}
			delete(next, key)
			changed = append(changed, key)
			continue
		}
		if had && old == r.value {
			continue
		}
		var text, num any
		switch x := r.value.(type) {
		case string:
			text = x
		case float64:
			num = x
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO todo_custom_field_values(todo_id, field_id, value_text, value_num)
VALUES (?, ?, ?, ?)
ON CONFLICT(todo_id, field_id) DO UPDATE SET value_text = excluded.value_text, value_num = excluded.value_num`,
			todoID, r.fieldID, text, num); err != nil {
			return nil, nil, fmt.Errorf("set custom field value: %w", err)
		}
		if next == nil {
			next = CustomFieldValues{}
		}
		next[key] = r.value
		changed = append(changed, key)
	}
	sort.Strings(changed)
	return changed, next, nil
}

// loadTodoCustomFieldsQ returns the todo's values keyed by field key; nil when none.
func loadTodoCustomFieldsQ(ctx context.Context, q sqlRowsQueryer, todoID int64) (CustomFieldValues, error) {
	rows, err := q.QueryContext(ctx, `
SELECT f.key, f.type, v.value_text, v.value_num
FROM todo_custom_field_values v
JOIN project_custom_fields f ON f.id = v.field_id
WHERE v.todo_id = ?`, todoID)
	if err != nil {
		return nil, fmt.Errorf("load custom field values: %w", err)
	}
	defer rows.Close()
	var out CustomFieldValues
	for rows.Next() {
		var key, typ string
		var text sql.NullString
		var num sql.NullFloat64
		if err := rows.Scan(&key, &typ, &text, &num); err != nil {
			return nil, fmt.Errorf("scan custom field value: %w", err)
		}
		if out == nil {
			out = CustomFieldValues{}
		}
		switch {
		case CustomFieldType(typ) == CustomFieldNumber && num.Valid:
			out[key] = num.Float64
		case text.Valid:
			out[key] = text.String
		}
	}
	return out, rows.Err()
}

// listCustomFieldsForTodosQ loads values for many todos at once (board and
// archive reads). Todos without values are absent from the result.
func listCustomFieldsForTodosQ(ctx context.Context, q sqlRowsQueryer, todoIDs []int64) (map[int64]CustomFieldValues, error) {
	out := map[int64]CustomFieldValues{}
	const batchSize = 500
	for i := 0; i < len(todoIDs); i += batchSize {
		end := i + batchSize
		if end > len(todoIDs) {
			end = len(todoIDs)
		}
		batch := todoIDs[i:end]
		ph := make([]string, len(batch))
		args := make([]any, len(batch))
		for j, id := range batch {
			ph[j] = "?"
			args[j] = id
		}
		rows, err := q.QueryContext(ctx, `
SELECT v.todo_id, f.key, f.type, v.value_text, v.value_num
FROM todo_custom_field_values v
JOIN project_custom_fields f ON f.id = v.field_id
WHERE v.todo_id IN (`+strings.Join(ph, ",")+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("list custom fields for todos: %w", err)
		}
		for rows.Next() {
			var todoID int64
			var key, typ string
			var text sql.NullString
			var num sql.NullFloat64
			if err := rows.Scan(&todoID, &key, &typ, &text, &num); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan custom field value: %w", err)
			}
			if out[todoID] == nil {
				out[todoID] = CustomFieldValues{}
			}
			switch {
			case CustomFieldType(typ) == CustomFieldNumber && num.Valid:
				out[todoID][key] = num.Float64
			case text.Valid:
				out[todoID][key] = text.String
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("rows custom field values: %w", err)
		}
	}
	return out, nil
}

// importCustomFieldsTx restores a backup's custom field definitions and todo
// values into a project after its todos exist. Definitions are matched by key
// (existing ones are kept; a type mismatch is a validation error), values are
// validated against their definition, and a todo without values is untouched.
// Values are written to the destination todos recorded in imported, never to a
// todo looked up by its backup local ID.
func importCustomFieldsTx(ctx context.Context, tx *sql.Tx, projectID int64, p ProjectExport, imported *importedTodos) error {
	hasValues := false
	for _, t := range p.Todos {
		if len(t.CustomFields) > 0 {
			hasValues = true
			break
		}
	}
	if len(p.CustomFields) == 0 && !hasValues {
		return nil
	}
	existing, err := listCustomFieldsQ(ctx, tx, projectID)
	if err != nil {
		return err
	}
	byKey := make(map[string]CustomField, len(existing))
	position := 0
	for _, f := range existing {
		byKey[f.Key] = f
		if f.Position >= position {
			position = f.Position + 1
		}
	}
	for _, def := range p.CustomFields {
		key := strings.ToLower(strings.TrimSpace(def.Key))
		typ := CustomFieldType(def.Type)
		name := strings.TrimSpace(def.Name)
		if !isValidColumnKey(key) || !ValidCustomFieldType(typ) || name == "" || utf8.RuneCountInString(name) > maxCustomFieldNameLen {
			return fmt.Errorf("%w: project %q has an invalid custom field definition %q", ErrValidation, p.Name, def.Key)
		}
		if cur, ok := byKey[key]; ok {
			if cur.Type != typ {
				return fmt.Errorf("%w: custom field %q already exists with type %s, backup has %s", ErrValidation, key, cur.Type, typ)
			}
			continue
		}
		if len(byKey) >= maxCustomFieldsPerProject {
			return fmt.Errorf("%w: project %q may have at most %d custom fields", ErrValidation, p.Name, maxCustomFieldsPerProject)
		}
		res, err := tx.ExecContext(ctx, `
INSERT INTO project_custom_fields(project_id, key, name, type, position)
VALUES (?, ?, ?, ?, ?)`, projectID, key, name, string(typ), position)
		if err != nil {
			return fmt.Errorf("insert custom field: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("custom field id: %w", err)
		}
		byKey[key] = CustomField{ID: id, ProjectID: projectID, Key: key, Name: name, Type: typ, Position: position}
		position++
	}
	for i, t := range p.Todos {
		if len(t.CustomFields) == 0 {
			continue
		}
		todoID := imported.todoIDs[i]
		if todoID == 0 {
			return fmt.Errorf("custom fields for todo %d: todo was not imported", t.LocalID)
		}
		current, err := loadTodoCustomFieldsQ(ctx, tx, todoID)
		if err != nil {
			return err
		}
		patch := make(map[string]resolvedCustomField, len(t.CustomFields))
		for key, raw := range t.CustomFields {
			def, ok := byKey[strings.ToLower(strings.TrimSpace(key))]
			if !ok {
				return fmt.Errorf("%w: todo %d has a value for undefined custom field %q", ErrValidation, t.LocalID, key)
			}
			if raw == nil {
				continue
			}
			val, err := normalizeCustomFieldValue(def, raw)
			if err != nil {
				return err
			}
			patch[def.Key] = resolvedCustomField{fieldID: def.ID, value: val}
		}
		if _, _, err := applyCustomFieldPatchTx(ctx, tx, todoID, current, patch); err != nil {
			return err
		}
	}
	return nil
}

func auditActorFromContext(ctx context.Context) *int64 {
	if id, ok := UserIDFromContext(ctx); ok {
		return &id
	}
	return nil
}

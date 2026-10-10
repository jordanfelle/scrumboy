package migrate

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"
)

const usersPublicIDMigration = "076_add_users_public_id.sql"

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestMigration076BackfillsExistingUsersWithDistinctUUIDs(t *testing.T) {
	ctx := context.Background()
	sqlDB := openRawTestDB(t)
	applyMigrationsBefore(t, sqlDB, usersPublicIDMigration)

	now := time.Now().UTC().UnixMilli()
	for i, email := range []string{"a@example.com", "b@example.com", "c@example.com"} {
		if _, err := sqlDB.ExecContext(ctx, `INSERT INTO users(id, email, created_at, name, system_role) VALUES (?, ?, ?, ?, 'user')`, i+1, email, now, email); err != nil {
			t.Fatalf("insert pre-076 user: %v", err)
		}
	}
	if columnExists(t, sqlDB, "users", "public_id") {
		t.Fatal("public_id exists before migration 076")
	}

	if err := applyOne(ctx, sqlDB, usersPublicIDMigration); err != nil {
		t.Fatalf("apply 076: %v", err)
	}

	seen := map[string]bool{}
	rows, err := sqlDB.QueryContext(ctx, `SELECT public_id FROM users ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		if !uuidV4.MatchString(id) {
			t.Fatalf("backfilled public_id %q is not a UUIDv4", id)
		}
		if seen[id] {
			t.Fatalf("duplicate public_id %q", id)
		}
		seen[id] = true
		n++
	}
	if n != 3 {
		t.Fatalf("backfilled %d users, want 3", n)
	}
	if !indexExists(t, sqlDB, "idx_users_public_id") {
		t.Fatal("unique index idx_users_public_id missing")
	}
}

func TestMigration076NewUsersGetAnIDAndItIsImmutable(t *testing.T) {
	ctx := context.Background()
	sqlDB := openMigratedDB(t)
	now := time.Now().UTC().UnixMilli()

	insert := func(id int64, email string) string {
		t.Helper()
		// Deliberately the same column list the real insert sites use: no public_id.
		if _, err := sqlDB.ExecContext(ctx, `INSERT INTO users(id, email, created_at, name, system_role) VALUES (?, ?, ?, ?, 'user')`, id, email, now, email); err != nil {
			t.Fatalf("insert user: %v", err)
		}
		var pid string
		if err := sqlDB.QueryRowContext(ctx, `SELECT public_id FROM users WHERE id = ?`, id).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		if !uuidV4.MatchString(pid) {
			t.Fatalf("new user's public_id %q is not a UUIDv4", pid)
		}
		return pid
	}
	first := insert(1, "a@example.com")
	second := insert(2, "b@example.com")
	if first == second {
		t.Fatal("two users share a public_id")
	}

	// The scenario the column exists for: delete a user and let SQLite reuse the integer id.
	if _, err := sqlDB.ExecContext(ctx, `DELETE FROM users WHERE id = 2`); err != nil {
		t.Fatal(err)
	}
	reused := insert(2, "c@example.com")
	if reused == second {
		t.Fatal("a new user that reused users.id 2 inherited the deleted user's public_id")
	}

	// Immutable: any change to a set public_id aborts; a no-op write is allowed.
	if _, err := sqlDB.ExecContext(ctx, `UPDATE users SET public_id = 'x' WHERE id = 1`); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("changing public_id: err = %v, want an immutability error", err)
	}
	if _, err := sqlDB.ExecContext(ctx, `UPDATE users SET public_id = ? WHERE id = 1`, "0f8e2c4a-1b3d-4e5f-8a9b-0c1d2e3f4a5b"); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("changing public_id to another valid UUIDv4: err = %v, want an immutability error", err)
	}
	if _, err := sqlDB.ExecContext(ctx, `UPDATE users SET public_id = NULL WHERE id = 1`); err == nil {
		t.Fatal("clearing public_id must be rejected")
	}
	if _, err := sqlDB.ExecContext(ctx, `UPDATE users SET public_id = public_id, name = 'renamed' WHERE id = 1`); err != nil {
		t.Fatalf("a no-op public_id write must be allowed: %v", err)
	}
	// Two users can never share one.
	if _, err := sqlDB.ExecContext(ctx, `INSERT INTO users(id, email, created_at, name, system_role, public_id) VALUES (9, 'd@example.com', ?, 'd', 'user', ?)`, now, first); err == nil {
		t.Fatal("duplicate public_id insert must be rejected by the unique index")
	}
}

func TestMigration076PublicIDCheckAcceptsOnlyCanonicalUUIDv4(t *testing.T) {
	ctx := context.Background()
	sqlDB := openMigratedDB(t)
	now := time.Now().UTC().UnixMilli()

	nextID := int64(0)
	insertWith := func(publicID any) (int64, error) {
		nextID++
		_, err := sqlDB.ExecContext(ctx, `INSERT INTO users(id, email, created_at, name, system_role, public_id) VALUES (?, ?, ?, 'u', 'user', ?)`,
			nextID, fmt.Sprintf("u%d@example.com", nextID), now, publicID)
		return nextID, err
	}
	publicIDOf := func(id int64) string {
		t.Helper()
		var pid string
		if err := sqlDB.QueryRowContext(ctx, `SELECT public_id FROM users WHERE id = ?`, id).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		return pid
	}

	// An explicit, valid, canonical UUIDv4 is stored as given; every variant nibble is accepted.
	for _, valid := range []string{
		"0f8e2c4a-1b3d-4e5f-8a9b-0c1d2e3f4a5b",
		"0f8e2c4a-1b3d-4e5f-9a9b-0c1d2e3f4a5b",
		"0f8e2c4a-1b3d-4e5f-aa9b-0c1d2e3f4a5b",
		"0f8e2c4a-1b3d-4e5f-ba9b-0c1d2e3f4a5b",
	} {
		id, err := insertWith(valid)
		if err != nil {
			t.Fatalf("valid UUIDv4 %q rejected: %v", valid, err)
		}
		if got := publicIDOf(id); got != valid {
			t.Fatalf("explicit public_id stored as %q, want %q", got, valid)
		}
	}

	// NULL is allowed and replaced by a generated UUIDv4, which itself passes the CHECK.
	id, err := insertWith(nil)
	if err != nil {
		t.Fatalf("NULL public_id rejected: %v", err)
	}
	if got := publicIDOf(id); !uuidV4.MatchString(got) {
		t.Fatalf("NULL public_id was replaced with %q, want a generated UUIDv4", got)
	}
	// Omitting the column takes the same path; run it enough times that every random variant
	// nibble the generator can produce has gone through the CHECK.
	for i := 0; i < 200; i++ {
		nextID++
		if _, err := sqlDB.ExecContext(ctx, `INSERT INTO users(id, email, created_at, name, system_role) VALUES (?, ?, ?, 'u', 'user')`,
			nextID, fmt.Sprintf("gen%d@example.com", i), now); err != nil {
			t.Fatalf("insert without public_id (#%d): %v", i, err)
		}
		if got := publicIDOf(nextID); !uuidV4.MatchString(got) {
			t.Fatalf("generated public_id %q is not a UUIDv4", got)
		}
	}

	invalid := map[string]any{
		"empty":                "",
		"whitespace":           "                                    ",
		"version 1":            "0f8e2c4a-1b3d-1e5f-8a9b-0c1d2e3f4a5b",
		"version 7":            "0f8e2c4a-1b3d-7e5f-8a9b-0c1d2e3f4a5b",
		"variant 0":            "0f8e2c4a-1b3d-4e5f-0a9b-0c1d2e3f4a5b",
		"variant c":            "0f8e2c4a-1b3d-4e5f-ca9b-0c1d2e3f4a5b",
		"variant uppercase B":  "0f8e2c4a-1b3d-4e5f-Ba9b-0c1d2e3f4a5b",
		"uppercase hex":        "0F8E2C4A-1B3D-4E5F-8A9B-0C1D2E3F4A5B",
		"mixed case":           "0f8e2c4a-1b3d-4e5f-8a9b-0c1d2e3f4A5b",
		"too short":            "0f8e2c4a-1b3d-4e5f-8a9b-0c1d2e3f4a5",
		"too long":             "0f8e2c4a-1b3d-4e5f-8a9b-0c1d2e3f4a5b0",
		"no dashes":            "0f8e2c4a1b3d4e5f8a9b0c1d2e3f4a5b",
		"braced":               "{0f8e2c4a-1b3d-4e5f-8a9b-0c1d2e3f4a5}",
		"non-hex letter":       "0f8e2c4g-1b3d-4e5f-8a9b-0c1d2e3f4a5b",
		"dash in hex position": "0f8e2c4a-1b3d-4e5f-8a9b-0c1d2e3f4a-b",
		"misplaced dash":       "0f8e2c4a1-b3d-4e5f-8a9b-0c1d2e3f4a5b",
		"trailing space":       "0f8e2c4a-1b3d-4e5f-8a9b-0c1d2e3f4a5 ",
		"non-ascii":            "0f8e2c4a-1b3d-4e5f-8a9b-0c1d2e3f4a5é",
		"blob of a valid UUID": []byte("0f8e2c4a-1b3d-4e5f-8a9b-0c1d2e3f4a5c"),
		"integer":              int64(42),
	}
	for name, v := range invalid {
		if _, err := insertWith(v); err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
			t.Errorf("%s: insert public_id %v: err = %v, want a CHECK constraint failure", name, v, err)
		}
	}
}

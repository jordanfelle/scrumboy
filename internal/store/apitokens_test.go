package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"scrumboy/internal/db"
	"scrumboy/internal/migrate"
)

func TestCreateUserAPITokenAndGetUserByAPIToken(t *testing.T) {
	dir := t.TempDir()
	sqlDB, err := db.Open(filepath.Join(dir, "app.db"), db.Options{
		BusyTimeout: 5000,
		JournalMode: "WAL",
		Synchronous: "FULL",
	})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = sqlDB.Close() }()
	if err := migrate.Apply(context.Background(), sqlDB); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := New(sqlDB, nil)
	ctx := context.Background()

	u, err := st.BootstrapUser(ctx, "tok@example.com", "password123", "T")
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	name := "ci"
	_, plain, _, err := st.CreateUserAPIToken(ctx, u.ID, &name, false)
	if err != nil {
		t.Fatalf("create api token: %v", err)
	}
	if plain == "" || len(plain) < len(APITokenPrefix)+8 {
		t.Fatalf("unexpected plaintext token: %q", plain)
	}
	if plain[:len(APITokenPrefix)] != APITokenPrefix {
		t.Fatalf("expected %q prefix, got %q", APITokenPrefix, plain[:len(APITokenPrefix)])
	}

	got, err := st.GetUserByAPIToken(ctx, plain)
	if err != nil {
		t.Fatalf("get by api token: %v", err)
	}
	if got.ID != u.ID || got.Email != u.Email {
		t.Fatalf("user mismatch: got %+v want id=%d", got, u.ID)
	}

	if _, err := st.GetUserByAPIToken(ctx, plain+"x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong suffix: got %v want ErrNotFound", err)
	}
	if _, err := st.GetUserByAPIToken(ctx, "not-prefixed"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no prefix: got %v want ErrNotFound", err)
	}
}

func TestDeleteUserReassignsServiceTokensButDropsPersonalOnes(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()

	owner, err := st.BootstrapUser(ctx, "owner@example.com", "password123", "Owner")
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	departing, err := st.CreateUser(ctx, "departing@example.com", "password123", "Departing")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	svcName := "ci-automation"
	_, svcPlain, _, err := st.CreateUserAPIToken(ctx, departing.ID, &svcName, true)
	if err != nil {
		t.Fatalf("create service token: %v", err)
	}
	personalName := "laptop"
	_, personalPlain, _, err := st.CreateUserAPIToken(ctx, departing.ID, &personalName, false)
	if err != nil {
		t.Fatalf("create personal token: %v", err)
	}

	if err := st.DeleteUser(ctx, owner.ID, departing.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}

	// The old secret must never authenticate again — reassignment must not let the departed user
	// keep using their original plaintext as a way to impersonate whoever it was reassigned to.
	if _, err := st.GetUserByAPIToken(ctx, svcPlain); !errors.Is(err, ErrNotFound) {
		t.Fatalf("reassigned service token secret should be dead: got %v want ErrNotFound", err)
	}

	// But its audit record survives under the new owner instead of disappearing with the account.
	tokens, err := st.ListUserAPITokens(ctx, owner.ID)
	if err != nil {
		t.Fatalf("list api tokens: %v", err)
	}
	var found *APITokenMeta
	for i := range tokens {
		if tokens[i].Name != nil && *tokens[i].Name == svcName {
			found = &tokens[i]
		}
	}
	if found == nil {
		t.Fatalf("expected reassigned service token record under new owner, got %+v", tokens)
	}
	if !found.IsService {
		t.Fatalf("reassigned token should still be flagged is_service")
	}
	if found.RevokedAt == nil {
		t.Fatalf("reassigned token should be revoked, not left live under the new owner")
	}

	// Personal token is gone entirely with its former owner.
	if _, err := st.GetUserByAPIToken(ctx, personalPlain); !errors.Is(err, ErrNotFound) {
		t.Fatalf("personal token should not survive deletion: got %v want ErrNotFound", err)
	}
}

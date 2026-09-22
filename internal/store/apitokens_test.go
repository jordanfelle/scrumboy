package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestDeleteUserRetainsRevokedServiceTokensAcrossRepeatedOwnerDeletion(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()

	finalOwner, err := st.BootstrapUser(ctx, "final-owner@example.com", "password123", "Final Owner")
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	intermediateOwner, err := st.CreateUser(ctx, "intermediate-owner@example.com", "password123", "Intermediate Owner")
	if err != nil {
		t.Fatalf("create intermediate owner: %v", err)
	}
	if err := st.UpdateUserRole(ctx, finalOwner.ID, intermediateOwner.ID, SystemRoleOwner); err != nil {
		t.Fatalf("promote intermediate owner: %v", err)
	}
	departing, err := st.CreateUser(ctx, "original-owner@example.com", "password123", "Original Owner")
	if err != nil {
		t.Fatalf("create original owner: %v", err)
	}

	activeID, activeSecret, _, err := st.CreateUserAPIToken(ctx, departing.ID, stringPtr("active-service"), true)
	if err != nil {
		t.Fatalf("create active service token: %v", err)
	}
	revokedID, revokedSecret, _, err := st.CreateUserAPIToken(ctx, departing.ID, stringPtr("revoked-service"), true)
	if err != nil {
		t.Fatalf("create revoked service token: %v", err)
	}
	if err := st.RevokeUserAPIToken(ctx, departing.ID, revokedID); err != nil {
		t.Fatalf("revoke service token: %v", err)
	}
	var originalRevokedAt int64
	if err := st.db.QueryRowContext(ctx, `SELECT revoked_at FROM api_tokens WHERE id = ?`, revokedID).Scan(&originalRevokedAt); err != nil {
		t.Fatalf("read original revoked_at: %v", err)
	}

	if err := st.DeleteUser(ctx, intermediateOwner.ID, departing.ID); err != nil {
		t.Fatalf("delete original owner: %v", err)
	}
	assertAPITokenRow(t, st.db, activeID, intermediateOwner.ID, true, true)
	assertAPITokenRow(t, st.db, revokedID, intermediateOwner.ID, true, true)
	if _, err := st.GetUserByAPIToken(ctx, activeSecret); !errors.Is(err, ErrNotFound) {
		t.Fatalf("transferred active secret authenticated: %v", err)
	}
	if _, err := st.GetUserByAPIToken(ctx, revokedSecret); !errors.Is(err, ErrNotFound) {
		t.Fatalf("transferred revoked secret authenticated: %v", err)
	}

	if err := st.DeleteUser(ctx, finalOwner.ID, intermediateOwner.ID); err != nil {
		t.Fatalf("delete intermediate owner: %v", err)
	}
	assertAPITokenRow(t, st.db, activeID, finalOwner.ID, true, true)
	assertAPITokenRow(t, st.db, revokedID, finalOwner.ID, true, true)
	var retainedRevokedAt int64
	if err := st.db.QueryRowContext(ctx, `SELECT revoked_at FROM api_tokens WHERE id = ?`, revokedID).Scan(&retainedRevokedAt); err != nil {
		t.Fatalf("read retained revoked_at: %v", err)
	}
	if retainedRevokedAt != originalRevokedAt {
		t.Fatalf("revoked_at changed across transfers: got %d want %d", retainedRevokedAt, originalRevokedAt)
	}
}

func TestDeleteUserServiceTokenTransferRollsBackWhenUserDeletionFails(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()

	owner, err := st.BootstrapUser(ctx, "rollback-owner@example.com", "password123", "Owner")
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	departing, err := st.CreateUser(ctx, "rollback-target@example.com", "password123", "Target")
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	tokenID, secret, _, err := st.CreateUserAPIToken(ctx, departing.ID, stringPtr("rollback-service"), true)
	if err != nil {
		t.Fatalf("create service token: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `
CREATE TRIGGER fail_service_owner_delete
BEFORE DELETE ON users
BEGIN
  SELECT RAISE(FAIL, 'forced user deletion failure');
END`); err != nil {
		t.Fatalf("create deletion failure trigger: %v", err)
	}

	err = st.DeleteUser(ctx, owner.ID, departing.ID)
	if err == nil || !strings.Contains(err.Error(), "forced user deletion failure") {
		t.Fatalf("DeleteUser error=%v want forced failure", err)
	}
	assertAPITokenRow(t, st.db, tokenID, departing.ID, true, false)
	got, err := st.GetUserByAPIToken(ctx, secret)
	if err != nil {
		t.Fatalf("service secret should remain active after rollback: %v", err)
	}
	if got.ID != departing.ID {
		t.Fatalf("service secret resolved user=%d want %d", got.ID, departing.ID)
	}
}

func TestDeleteUserDoesNotProceedAfterRequesterDemotedBeforeMutation(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "authorization-race.db")
	primaryDB, err := db.Open(databasePath, db.Options{
		BusyTimeout: 5000,
		JournalMode: "WAL",
		Synchronous: "FULL",
	})
	if err != nil {
		t.Fatalf("open primary db: %v", err)
	}
	defer func() { _ = primaryDB.Close() }()
	if err := migrate.Apply(context.Background(), primaryDB); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := New(primaryDB, nil)
	ctx := context.Background()
	owner, err := st.BootstrapUser(ctx, "race-owner@example.com", "password123", "Owner")
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	target, err := st.CreateUser(ctx, "race-target@example.com", "password123", "Target")
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	if _, _, _, err := st.CreateUserAPIToken(ctx, target.ID, stringPtr("race-service"), true); err != nil {
		t.Fatalf("create service token: %v", err)
	}

	concurrentDB, err := db.Open(databasePath, db.Options{
		BusyTimeout: 5000,
		JournalMode: "WAL",
		Synchronous: "FULL",
	})
	if err != nil {
		t.Fatalf("open concurrent db: %v", err)
	}
	defer func() { _ = concurrentDB.Close() }()
	demotionTx, err := concurrentDB.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		t.Fatalf("begin demotion: %v", err)
	}
	if _, err := demotionTx.ExecContext(ctx, `UPDATE users SET system_role = 'user' WHERE id = ?`, owner.ID); err != nil {
		_ = demotionTx.Rollback()
		t.Fatalf("stage demotion: %v", err)
	}

	deleteResult := make(chan error, 1)
	go func() {
		deleteResult <- st.DeleteUser(ctx, owner.ID, target.ID)
	}()
	var (
		deleteErr      error
		deleteReturned bool
	)
	select {
	case deleteErr = <-deleteResult:
		deleteReturned = true
	case <-time.After(200 * time.Millisecond):
	}
	if err := demotionTx.Commit(); err != nil {
		t.Fatalf("commit demotion: %v", err)
	}
	if !deleteReturned {
		deleteErr = <-deleteResult
	}
	if deleteErr == nil {
		t.Fatal("DeleteUser succeeded using owner authorization read before a concurrent demotion")
	}
	if _, err := st.GetUser(ctx, target.ID); err != nil {
		t.Fatalf("target was deleted after requester lost owner role: %v", err)
	}
}

func stringPtr(value string) *string {
	return &value
}

func assertAPITokenRow(t *testing.T, db *sql.DB, tokenID, wantUserID int64, wantService, wantRevoked bool) {
	t.Helper()
	var (
		userID    int64
		isService bool
		revokedAt sql.NullInt64
	)
	if err := db.QueryRow(`SELECT user_id, is_service, revoked_at FROM api_tokens WHERE id = ?`, tokenID).Scan(&userID, &isService, &revokedAt); err != nil {
		t.Fatalf("read api token %d: %v", tokenID, err)
	}
	if userID != wantUserID || isService != wantService || revokedAt.Valid != wantRevoked {
		t.Fatalf("api token %d state user=%d service=%v revoked=%v; want user=%d service=%v revoked=%v", tokenID, userID, isService, revokedAt.Valid, wantUserID, wantService, wantRevoked)
	}
}

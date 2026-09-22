package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"scrumboy/internal/db"
	"scrumboy/internal/migrate"

	"modernc.org/sqlite"
)

const roleUpdateBarrierFunction = "scrumboy_test_block_role_update"

type roleUpdateBarrier struct {
	reached     chan struct{}
	release     chan struct{}
	reachedOnce sync.Once
	releaseOnce sync.Once
}

var roleUpdateBarrierRegistry = struct {
	sync.Mutex
	next     int64
	barriers map[int64]*roleUpdateBarrier
}{barriers: make(map[int64]*roleUpdateBarrier)}

func init() {
	sqlite.MustRegisterScalarFunction(roleUpdateBarrierFunction, 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		id, ok := args[0].(int64)
		if !ok {
			return nil, fmt.Errorf("role-update barrier id has type %T", args[0])
		}

		roleUpdateBarrierRegistry.Lock()
		barrier := roleUpdateBarrierRegistry.barriers[id]
		roleUpdateBarrierRegistry.Unlock()
		if barrier == nil {
			return nil, fmt.Errorf("role-update barrier %d is not registered", id)
		}

		barrier.reachedOnce.Do(func() {
			close(barrier.reached)
			<-barrier.release
		})
		return int64(0), nil
	})
}

func installOwnerDemotionBarrier(t *testing.T, sqlDB *sql.DB) *roleUpdateBarrier {
	t.Helper()

	barrier := &roleUpdateBarrier{
		reached: make(chan struct{}),
		release: make(chan struct{}),
	}
	roleUpdateBarrierRegistry.Lock()
	roleUpdateBarrierRegistry.next++
	id := roleUpdateBarrierRegistry.next
	roleUpdateBarrierRegistry.barriers[id] = barrier
	roleUpdateBarrierRegistry.Unlock()

	t.Cleanup(func() {
		barrier.releaseOnce.Do(func() { close(barrier.release) })
		roleUpdateBarrierRegistry.Lock()
		delete(roleUpdateBarrierRegistry.barriers, id)
		roleUpdateBarrierRegistry.Unlock()
	})

	_, err := sqlDB.Exec(fmt.Sprintf(`
CREATE TRIGGER test_block_owner_demotion
BEFORE UPDATE OF system_role ON users
WHEN OLD.system_role = 'owner' AND NEW.system_role <> 'owner'
BEGIN
    SELECT %s(%d);
END`, roleUpdateBarrierFunction, id))
	if err != nil {
		t.Fatalf("create owner-demotion barrier trigger: %v", err)
	}
	return barrier
}

func (b *roleUpdateBarrier) waitUntilReached(t *testing.T) {
	t.Helper()
	select {
	case <-b.reached:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for owner demotion to reach the write")
	}
}

func (b *roleUpdateBarrier) unblock() {
	b.releaseOnce.Do(func() { close(b.release) })
}

func newConcurrentRoleStores(t *testing.T) (*Store, *Store, *sql.DB, *sql.DB) {
	t.Helper()

	databasePath := filepath.Join(t.TempDir(), "app.db")
	options := db.Options{BusyTimeout: 5000, JournalMode: "WAL", Synchronous: "FULL"}
	firstDB, err := db.Open(databasePath, options)
	if err != nil {
		t.Fatalf("open first db: %v", err)
	}
	t.Cleanup(func() { _ = firstDB.Close() })
	if err := migrate.Apply(context.Background(), firstDB); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	secondDB, err := db.Open(databasePath, options)
	if err != nil {
		t.Fatalf("open second db: %v", err)
	}
	t.Cleanup(func() { _ = secondDB.Close() })

	return New(firstDB, nil), New(secondDB, nil), firstDB, secondDB
}

func waitForBlockedRoleUpdate(t *testing.T, sqlDB *sql.DB, result <-chan error) (error, bool) {
	t.Helper()

	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()

	var inUseSince time.Time
	for {
		select {
		case err := <-result:
			return err, true
		case <-ticker.C:
			if sqlDB.Stats().InUse == 0 {
				inUseSince = time.Time{}
				continue
			}
			if inUseSince.IsZero() {
				inUseSince = time.Now()
				continue
			}
			if time.Since(inUseSince) >= 100*time.Millisecond {
				return nil, false
			}
		case <-deadline.C:
			t.Fatal("timed out waiting for concurrent role update to reach its mutation")
		}
	}
}

func receiveRoleUpdateResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for role update")
		return nil
	}
}

func TestUpdateUserRole_ConcurrentOwnerDemotionsCannotRemoveAllOwners(t *testing.T) {
	firstStore, secondStore, firstDB, secondDB := newConcurrentRoleStores(t)
	ctx := context.Background()

	ownerA, err := firstStore.BootstrapUser(ctx, "owner-a@test.com", "password123", "Owner A")
	if err != nil {
		t.Fatalf("BootstrapUser: %v", err)
	}
	ownerB, err := firstStore.CreateUser(ctx, "owner-b@test.com", "password123", "Owner B")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := firstStore.UpdateUserRole(ctx, ownerA.ID, ownerB.ID, SystemRoleOwner); err != nil {
		t.Fatalf("promote second owner: %v", err)
	}

	barrier := installOwnerDemotionBarrier(t, firstDB)
	firstResult := make(chan error, 1)
	go func() {
		firstResult <- firstStore.UpdateUserRole(ctx, ownerA.ID, ownerB.ID, SystemRoleUser)
	}()
	barrier.waitUntilReached(t)

	secondResult := make(chan error, 1)
	go func() {
		secondResult <- secondStore.UpdateUserRole(ctx, ownerB.ID, ownerA.ID, SystemRoleUser)
	}()
	secondEarlyResult, secondCompleted := waitForBlockedRoleUpdate(t, secondDB, secondResult)

	barrier.unblock()
	if err := receiveRoleUpdateResult(t, firstResult); err != nil {
		t.Fatalf("first owner demotion failed: %v", err)
	}
	if !secondCompleted {
		secondEarlyResult = receiveRoleUpdateResult(t, secondResult)
	}
	if secondEarlyResult == nil {
		t.Fatal("both concurrent owner demotions succeeded")
	}

	owners, err := firstStore.countOwners(ctx)
	if err != nil {
		t.Fatalf("count owners: %v", err)
	}
	if owners != 1 {
		t.Fatalf("owner count = %d, want 1", owners)
	}
}

func TestUpdateUserRole_RequesterDemotedBeforeMutationFailsClosed(t *testing.T) {
	firstStore, secondStore, firstDB, secondDB := newConcurrentRoleStores(t)
	ctx := context.Background()

	ownerA, err := firstStore.BootstrapUser(ctx, "owner-a@test.com", "password123", "Owner A")
	if err != nil {
		t.Fatalf("BootstrapUser: %v", err)
	}
	ownerB, err := firstStore.CreateUser(ctx, "owner-b@test.com", "password123", "Owner B")
	if err != nil {
		t.Fatalf("CreateUser owner B: %v", err)
	}
	if err := firstStore.UpdateUserRole(ctx, ownerA.ID, ownerB.ID, SystemRoleOwner); err != nil {
		t.Fatalf("promote second owner: %v", err)
	}
	user, err := firstStore.CreateUser(ctx, "user@test.com", "password123", "User")
	if err != nil {
		t.Fatalf("CreateUser target: %v", err)
	}

	barrier := installOwnerDemotionBarrier(t, firstDB)
	demotionResult := make(chan error, 1)
	go func() {
		demotionResult <- firstStore.UpdateUserRole(ctx, ownerB.ID, ownerA.ID, SystemRoleUser)
	}()
	barrier.waitUntilReached(t)

	staleRequesterResult := make(chan error, 1)
	go func() {
		staleRequesterResult <- secondStore.UpdateUserRole(ctx, ownerA.ID, user.ID, SystemRoleAdmin)
	}()
	staleEarlyResult, staleCompleted := waitForBlockedRoleUpdate(t, secondDB, staleRequesterResult)

	barrier.unblock()
	if err := receiveRoleUpdateResult(t, demotionResult); err != nil {
		t.Fatalf("requester demotion failed: %v", err)
	}
	if !staleCompleted {
		staleEarlyResult = receiveRoleUpdateResult(t, staleRequesterResult)
	}
	if staleEarlyResult == nil {
		t.Fatal("role update succeeded after the requester's owner role was concurrently invalidated")
	}

	updatedUser, err := firstStore.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUser target: %v", err)
	}
	if updatedUser.SystemRole != SystemRoleUser {
		t.Fatalf("target role = %s, want %s", updatedUser.SystemRole, SystemRoleUser)
	}
}

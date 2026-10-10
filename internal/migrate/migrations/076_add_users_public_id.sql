-- Migration 076: permanent, never-reused public id for users
--
-- users.id is a plain INTEGER PRIMARY KEY (no AUTOINCREMENT), so SQLite may hand a deleted
-- user's id to a later user. That is fine for internal joins, but an external integration that
-- stores a Scrumboy user id to link an account (for example a chat bot) could end up pointing
-- at a different person after a delete. public_id is a random UUID, assigned once and never
-- changed, that such integrations can key on instead; users.id stays the internal key.
--
-- Existing users are backfilled here. New users get one from the insert trigger, so no insert
-- path (password, bootstrap, OIDC, or any future one) can leave it NULL. A second trigger makes
-- it immutable once set.
--
-- The CHECK accepts only NULL (filled in by the insert trigger) or a canonical lowercase UUIDv4,
-- so an explicit INSERT cannot store an empty or malformed value. It is a column constraint rather
-- than a trigger so it applies to every write, including the backfill and the insert trigger's own
-- UPDATE, with no dependence on trigger order. GLOB is case-sensitive: the template pins the
-- dashes, the version nibble and the variant nibble, and the character-class test rejects
-- anything that is not lowercase hex or a dash.

ALTER TABLE users ADD COLUMN public_id TEXT CHECK (
  public_id IS NULL OR (
    typeof(public_id) = 'text'
    AND length(public_id) = 36
    AND public_id GLOB '????????-????-4???-[89ab]???-????????????'
    AND public_id NOT GLOB '*[^0-9a-f-]*'
    AND length(replace(public_id, '-', '')) = 32
  )
);

UPDATE users
SET public_id = lower(
  hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' ||
  substr('89ab', 1 + (abs(random()) % 4), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6))
)
WHERE public_id IS NULL;

CREATE UNIQUE INDEX idx_users_public_id ON users(public_id);

CREATE TRIGGER IF NOT EXISTS trg_users_public_id_default
AFTER INSERT ON users
WHEN NEW.public_id IS NULL
BEGIN
  UPDATE users
  SET public_id = lower(
    hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' ||
    substr('89ab', 1 + (abs(random()) % 4), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6))
  )
  WHERE id = NEW.id;
END;

CREATE TRIGGER IF NOT EXISTS trg_users_public_id_immutable
BEFORE UPDATE OF public_id ON users
WHEN OLD.public_id IS NOT NULL AND NEW.public_id IS NOT OLD.public_id
BEGIN
  SELECT RAISE(ABORT, 'users.public_id is immutable');
END;

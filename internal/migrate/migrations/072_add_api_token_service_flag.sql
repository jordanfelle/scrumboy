-- Migration 072: mark an API token as a "service" token (bot/automation identity)
--
-- A service token is created under a user account like any other token, but is
-- flagged so operational tooling can distinguish it from a personal credential:
-- when its owning user is deleted, DeleteUser reassigns (rather than cascade-deletes)
-- any of that user's active service tokens to the requesting owner, revoking them in
-- the same step. This keeps the token's audit record (name, is_service, timestamps)
-- alive under the new owner instead of vanishing with the account, without leaving
-- the departed user's original secret valid to authenticate as the new owner.

ALTER TABLE api_tokens ADD COLUMN is_service INTEGER NOT NULL DEFAULT 0;

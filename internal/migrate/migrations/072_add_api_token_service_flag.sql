-- Migration 072: mark a normal user-owned API token for service/automation use
--
-- A service token is created under a user account like any other token, but is
-- flagged so operational tooling can distinguish it from a personal credential:
-- when its owning user is deleted, DeleteUser reassigns (rather than cascade-deletes)
-- all of that user's service-token records to the requesting owner. Active tokens are
-- revoked in the same step and already-revoked tokens retain their revocation time.
-- This keeps the token's audit record (name, is_service, timestamps) alive under the
-- new owner instead of vanishing with the account, without leaving the departed user's
-- original secret valid to authenticate as the new owner.

ALTER TABLE api_tokens ADD COLUMN is_service INTEGER NOT NULL DEFAULT 0;

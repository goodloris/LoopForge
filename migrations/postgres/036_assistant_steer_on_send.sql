-- Idempotent PostgreSQL counterpart of migrations/036_assistant_steer_on_send.sql
-- (upstream). Optional: startup AutoMigrate adds the same column. Existing
-- users keep queueing messages sent during a running reply.
ALTER TABLE assistant_preferences ADD COLUMN IF NOT EXISTS steer_on_send boolean NOT NULL DEFAULT false;

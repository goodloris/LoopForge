-- Idempotent PostgreSQL counterpart of migrations/039_platform_git_ssh_hosts.sql
-- (upstream): trusted Git SSH server public keys managed by platform
-- administrators. Optional: startup AutoMigrate creates the same table.
CREATE TABLE IF NOT EXISTS platform_git_ssh_hosts (
  host varchar(260) NOT NULL PRIMARY KEY,
  public_keys text NOT NULL,
  revision varchar(64) NOT NULL
);

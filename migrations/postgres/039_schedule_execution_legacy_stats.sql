-- PostgreSQL counterpart of migrations/039_schedule_execution_legacy_stats.sql
-- (upstream). Execute manually with the platform stopped and a database
-- backup. Requires the current schedule_job_execution_stats columns (startup
-- AutoMigrate adds them).
--
-- Backfills only missing hourly buckets from retained, finished executions of
-- the last 30 days. Existing buckets (including records removed by retention)
-- are never overwritten. A partial existing bucket, or a missing bucket whose
-- executions reported usage, needs execution-level reconciliation: the script
-- then raises an error and changes nothing. Unreported legacy token usage
-- stays unknown; no token usage is reconstructed. Idempotent: a second run
-- finds every bucket present and inserts nothing.
--   docker compose exec -T postgres psql -X -v ON_ERROR_STOP=1 -U loopforge -d loopforge < migrations/postgres/039_schedule_execution_legacy_stats.sql
BEGIN;

CREATE TEMPORARY TABLE legacy_schedule_stats ON COMMIT DROP AS
SELECT
    job_id,
    MAX(project_id) AS project_id,
    date_trunc('hour', started_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' AS bucket_hour,
    COUNT(*) AS finished,
    COUNT(*) FILTER (WHERE status = 'success') AS success,
    COUNT(*) FILTER (WHERE status = 'failed') AS failed,
    COUNT(*) FILTER (WHERE duration_ms > 0) AS timed,
    COALESCE(SUM(GREATEST(duration_ms, 0)), 0) AS duration_ms,
    COUNT(*) FILTER (WHERE jsonb_typeof(doc -> 'usage') = 'object') AS with_usage
FROM (
    SELECT job_id, project_id, started_at, status, payload::jsonb AS doc,
           CASE WHEN jsonb_typeof(payload::jsonb -> 'durationMs') = 'number'
                THEN trunc((payload::jsonb ->> 'durationMs')::numeric)::bigint ELSE 0 END AS duration_ms
    FROM schedule_job_executions
    WHERE started_at >= now() - interval '30 days'
      AND COALESCE(status, '') NOT IN ('', 'running')
) e
GROUP BY job_id, 3;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM legacy_schedule_stats r
        LEFT JOIN schedule_job_execution_stats s
          ON s.job_id = r.job_id AND s.bucket_hour = r.bucket_hour
        WHERE (s.job_id IS NOT NULL AND s.finished < r.finished)
           OR (s.job_id IS NULL AND r.with_usage > 0)
    ) THEN
        RAISE EXCEPTION 'schedule_job_execution_stats has partial buckets or buckets with usage to reconcile; nothing was changed';
    END IF;
END
$$;

INSERT INTO schedule_job_execution_stats (
    job_id, bucket_hour, project_id, finished, success, failed, timed, duration_ms,
    metered, tokens_total, tokens_input, tokens_output, tokens_cache_read,
    tokens_cache_write, priced, cost_usd
)
SELECT r.job_id, r.bucket_hour, r.project_id, r.finished, r.success, r.failed,
       r.timed, r.duration_ms, 0, 0, 0, 0, 0, 0, 0, 0
FROM legacy_schedule_stats r
WHERE NOT EXISTS (
    SELECT 1 FROM schedule_job_execution_stats s
    WHERE s.job_id = r.job_id AND s.bucket_hour = r.bucket_hour
);

COMMIT;

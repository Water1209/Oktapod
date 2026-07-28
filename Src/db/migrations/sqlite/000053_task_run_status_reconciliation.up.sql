-- A Run is the durable execution state machine. Older builds could commit a
-- terminal Run while leaving the denormalized Task status as "running".
-- Reconcile those rows once so UI routing and conversation queue decisions do
-- not treat an already blocked Run as active work.
UPDATE tasks
SET status = (
        SELECT r.status
        FROM runs r
        WHERE r.task_id = tasks.id
        ORDER BY r.created_at DESC, r.id DESC
        LIMIT 1
    ),
    error_code = CASE
        WHEN (
            SELECT r.status
            FROM runs r
            WHERE r.task_id = tasks.id
            ORDER BY r.created_at DESC, r.id DESC
            LIMIT 1
        ) IN ('blocked', 'failed')
        THEN COALESCE((
            SELECT NULLIF(s.error_code, '')
            FROM steps s
            WHERE s.run_id = (
                SELECT r.id
                FROM runs r
                WHERE r.task_id = tasks.id
                ORDER BY r.created_at DESC, r.id DESC
                LIMIT 1
            )
              AND s.status IN ('blocked', 'failed')
            ORDER BY s.position DESC, s.id DESC
            LIMIT 1
        ), error_code)
        ELSE error_code
    END,
    updated_at = (
        SELECT r.updated_at
        FROM runs r
        WHERE r.task_id = tasks.id
        ORDER BY r.created_at DESC, r.id DESC
        LIMIT 1
    )
WHERE EXISTS (
    SELECT 1
    FROM runs r
    WHERE r.id = (
        SELECT latest.id
        FROM runs latest
        WHERE latest.task_id = tasks.id
        ORDER BY latest.created_at DESC, latest.id DESC
        LIMIT 1
    )
      AND r.status <> tasks.status
);

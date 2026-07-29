-- name: AppendAgentTaskEvent :one
SELECT *
FROM append_agent_task_event(
    @task_id,
    @event_type,
    @source,
    @source_event_id,
    sqlc.narg('occurred_at'),
    @schema_version,
    @data
);

-- name: ListAgentTaskEvents :many
SELECT *
FROM agent_task_event
WHERE task_id = $1
ORDER BY sequence ASC;

-- name: ListAgentTaskEventsSince :many
SELECT *
FROM agent_task_event
WHERE task_id = $1
  AND sequence > $2
ORDER BY sequence ASC;

-- name: DeleteAgentTaskEventsByWorkspace :exec
DELETE FROM agent_task_event
WHERE workspace_id = $1;

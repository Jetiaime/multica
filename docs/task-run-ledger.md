# Task run ledger

Multica keeps the mutable `agent_task_queue` row as the authority for current
queue state and adds an append-only `agent_task_event` ledger for lifecycle
evidence. This is deliberately not full event sourcing: existing task commands
continue to read and mutate the queue row, while the ledger supports
reconciliation, incident review, and a read-only status projection.

## Authority model

| Fact | Authoritative source | Notes |
| --- | --- | --- |
| Queue transition | `server.task_queue` | Captured in the same database transaction as the task row change. |
| Provider start/exit | `daemon://<runtime-id>/provider` | Start is emitted only after a provider session exists; exit only after the provider result is observed. |
| Local slot acquire/release | `daemon://<runtime-id>/slot` | A terminal task row does not prove local cleanup or slot release. |
| Wrapper exit | `daemon://<runtime-id>/wrapper` | Low-frequency lifecycle evidence, not a heartbeat. |
| Journal delivery acknowledgement | `task-token://<task-id>/journal` | Forensic delivery evidence; it does not prove provider or runtime liveness. |

The task token is available to the spawned agent. Its observations are
therefore retained for diagnostics but are not authoritative for provider or
slot conditions.

## Event contract

Each event has a server UUID, task-local monotonic `sequence`, `type`,
`source`, optional caller-stable `source_event_id`, occurrence `time`,
server `observed_at`, `schema_version`, and non-secret `data`.

The envelope borrows the useful identity and time distinction from
[CloudEvents](https://github.com/cloudevents/spec/blob/main/cloudevents/spec.md)
without claiming CloudEvents wire compatibility. A producer retries one
occurrence with the same source event id. The server returns the original event
for a semantically identical retry and rejects the same id with a different
task, type, schema, occurrence time, or payload. The idempotency record and
event append happen atomically, following the caller-provided token and
semantic-equivalence guidance in
[Making retries safe with idempotent APIs](https://aws.amazon.com/builders-library/making-retries-safe-with-idempotent-APIs/).

Queue transitions use the task-row lock for sequence allocation and are
appended by an `AFTER INSERT OR UPDATE OF status` trigger. Consequently, a
rolled-back task mutation cannot leave a committed transition event.

## Status projection

`GET /api/tasks/{task-id}/status` and
`multica task status <task-id> --output json` return:

- `RunActive`
- `RuntimeAlive`
- `ProviderAlive`
- `SlotHeld`
- `Stalled`

Each condition contains `status` (`True`, `False`, or `Unknown`), `reason`,
`message`, and `last_transition_time`. This follows the
[Kubernetes Conditions](https://github.com/kubernetes/community/blob/main/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties)
model: conditions summarize observed facts, and missing or stale evidence is
represented as `Unknown` instead of being guessed as `False`.

Conditions are independent. In particular:

- `task_status=completed` does not prove `SlotHeld=False`;
- a terminal task row does not prove provider exit;
- a stale runtime heartbeat does not prove provider exit;
- a task-token provider event does not prove process liveness;
- `Stalled=True` is evidence for investigation, not authorization to rerun.

No condition in this phase performs admission control, dispatches work, or
automatically retries a task.

`history_complete` is true only when sequence 1 is the initial
`server.task_queue` event. Existing tasks created before the migration, or a
task whose initial server event is absent, remain queryable with
`history_complete=false`; consumers must not manufacture the missing prefix.

## Operations and retention

```bash
multica task events <task-id> --output json
multica task events <task-id> --since <sequence> --output json
multica task status <task-id> --output json
```

Task-scoped processes can append the allow-listed provider, wrapper, or journal
observations with `multica task event add`. The daemon uses its authenticated
endpoint for authoritative provider and slot events. Event payloads must be
low-frequency, non-secret facts; heartbeat and progress streams do not belong
in this ledger.

Events follow workspace retention. Workspace deletion explicitly removes its
task events because the database schema intentionally has no foreign-key
cascade. Ledger indexes are created concurrently so deployment does not block
normal table writes; see PostgreSQL's
[`CREATE INDEX CONCURRENTLY`](https://www.postgresql.org/docs/current/sql-createindex.html#SQL-CREATEINDEX-CONCURRENTLY)
trade-offs.

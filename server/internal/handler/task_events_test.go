package handler

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestTaskConditionsRequireAuthoritativeDaemonEvidence(t *testing.T) {
	now := time.Date(2026, 7, 29, 2, 0, 0, 0, time.UTC)
	task := db.AgentTaskQueue{
		Status:    "running",
		CreatedAt: pgtype.Timestamptz{Time: now.Add(-time.Minute), Valid: true},
		StartedAt: pgtype.Timestamptz{Time: now.Add(-30 * time.Second), Valid: true},
	}
	runtimeAlive := taskCondition("RuntimeAlive", "True", "HeartbeatFresh", "", now)

	untrusted := []db.AgentTaskEvent{{
		EventType:  "provider.started",
		Source:     "task-token://task/provider",
		OccurredAt: pgtype.Timestamptz{Time: now.Add(-20 * time.Second), Valid: true},
	}}
	if got := conditionForProvider(task, runtimeAlive, untrusted); got.Status != "Unknown" || got.Reason != "ProviderUnobserved" {
		t.Fatalf("task-token provider evidence must not prove liveness: %#v", got)
	}

	authoritative := []db.AgentTaskEvent{{
		EventType:  "provider.started",
		Source:     "daemon://runtime/provider",
		OccurredAt: pgtype.Timestamptz{Time: now.Add(-20 * time.Second), Valid: true},
	}}
	if got := conditionForProvider(task, runtimeAlive, authoritative); got.Status != "True" || got.Reason != "ProviderStarted" {
		t.Fatalf("daemon provider evidence should prove liveness: %#v", got)
	}
}

func TestSlotConditionDoesNotInferReleaseFromTerminalTask(t *testing.T) {
	now := time.Date(2026, 7, 29, 2, 0, 0, 0, time.UTC)
	task := db.AgentTaskQueue{
		Status:      "completed",
		CreatedAt:   pgtype.Timestamptz{Time: now.Add(-time.Minute), Valid: true},
		CompletedAt: pgtype.Timestamptz{Time: now, Valid: true},
	}
	unknown := taskCondition("RuntimeAlive", "Unknown", "RuntimeMissing", "", now)

	if got := conditionForSlot(task, unknown, unknown, nil, now); got.Status != "Unknown" || got.Reason != "ReleaseUnobserved" {
		t.Fatalf("terminal state must not imply local slot release: %#v", got)
	}

	events := []db.AgentTaskEvent{
		{
			EventType:  "slot.acquired",
			Source:     "daemon://runtime/slot",
			OccurredAt: pgtype.Timestamptz{Time: now.Add(-10 * time.Second), Valid: true},
		},
		{
			EventType:  "slot.released",
			Source:     "daemon://runtime/slot",
			OccurredAt: pgtype.Timestamptz{Time: now.Add(time.Second), Valid: true},
		},
	}
	if got := conditionForSlot(task, unknown, unknown, events, now.Add(2*time.Second)); got.Status != "False" || got.Reason != "ReleaseObserved" {
		t.Fatalf("explicit daemon release should clear the slot: %#v", got)
	}
}

func TestTaskConditionsDoNotInferTaskProcessFactsFromQueueOrRuntime(t *testing.T) {
	now := time.Date(2026, 7, 29, 2, 0, 0, 0, time.UTC)
	runtimeAlive := taskCondition("RuntimeAlive", "True", "HeartbeatFresh", "", now)
	providerUnknown := taskCondition("ProviderAlive", "Unknown", "ProviderUnobserved", "", now)
	running := db.AgentTaskQueue{
		Status:    "running",
		CreatedAt: pgtype.Timestamptz{Time: now.Add(-time.Minute), Valid: true},
		StartedAt: pgtype.Timestamptz{Time: now.Add(-30 * time.Second), Valid: true},
	}
	if got := conditionForSlot(running, runtimeAlive, providerUnknown, nil, now); got.Status != "Unknown" {
		t.Fatalf("runtime liveness must not prove a task-specific slot holder: %#v", got)
	}

	completed := running
	completed.Status = "completed"
	completed.CompletedAt = pgtype.Timestamptz{Time: now, Valid: true}
	events := []db.AgentTaskEvent{{
		EventType:  "provider.started",
		Source:     "daemon://runtime/provider",
		OccurredAt: pgtype.Timestamptz{Time: now.Add(-20 * time.Second), Valid: true},
	}}
	if got := conditionForProvider(completed, runtimeAlive, events); got.Status != "Unknown" || got.Reason != "ExitUnobserved" {
		t.Fatalf("terminal queue state must not prove provider exit: %#v", got)
	}
}

func TestAgentTaskEventLedgerIsTransactionalAndIdempotent(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "task-event-ledger-"+t.Name(), []byte(`{}`))
	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority)
		VALUES ($1, $2, 'queued', 0)
		RETURNING id
	`, agentID, handlerTestRuntimeID(t)).Scan(&taskID); err != nil {
		t.Fatalf("insert task: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_event WHERE task_id = $1`, taskID)
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
	})

	if _, err := testPool.Exec(ctx, `
		UPDATE agent_task_queue
		SET status = 'dispatched', dispatched_at = now()
		WHERE id = $1
	`, taskID); err != nil {
		t.Fatalf("dispatch task: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		UPDATE agent_task_queue
		SET status = 'running', started_at = now()
		WHERE id = $1
	`, taskID); err != nil {
		t.Fatalf("start task: %v", err)
	}

	taskUUID := parseUUID(taskID)
	events, err := testHandler.Queries.ListAgentTaskEvents(ctx, taskUUID)
	if err != nil {
		t.Fatalf("list transition events: %v", err)
	}
	wantTypes := []string{"task.queued", "task.dispatched", "task.running"}
	if len(events) != len(wantTypes) {
		t.Fatalf("transition event count = %d, want %d: %#v", len(events), len(wantTypes), events)
	}
	for i, want := range wantTypes {
		if events[i].Sequence != int64(i+1) || events[i].EventType != want || events[i].Source != "server.task_queue" {
			t.Fatalf("event %d = %#v, want sequence/type/source %d/%s/server.task_queue", i, events[i], i+1, want)
		}
	}

	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin rollback probe: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE agent_task_queue
		SET status = 'completed', completed_at = now()
		WHERE id = $1
	`, taskID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("update terminal status in transaction: %v", err)
	}
	var inTxCompleted int
	if err := tx.QueryRow(ctx, `
		SELECT count(*)
		FROM agent_task_event
		WHERE task_id = $1 AND event_type = 'task.completed'
	`, taskID).Scan(&inTxCompleted); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("read in-transaction event: %v", err)
	}
	if inTxCompleted != 1 {
		_ = tx.Rollback(ctx)
		t.Fatalf("in-transaction completed events = %d, want 1", inTxCompleted)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback probe: %v", err)
	}

	var status string
	var rolledBackCompleted int
	if err := testPool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id = $1`, taskID).Scan(&status); err != nil {
		t.Fatalf("read task after rollback: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		SELECT count(*)
		FROM agent_task_event
		WHERE task_id = $1 AND event_type = 'task.completed'
	`, taskID).Scan(&rolledBackCompleted); err != nil {
		t.Fatalf("read events after rollback: %v", err)
	}
	if status != "running" || rolledBackCompleted != 0 {
		t.Fatalf("rollback split task/event state: status=%s completed_events=%d", status, rolledBackCompleted)
	}

	params := db.AppendAgentTaskEventParams{
		TaskID:        taskUUID,
		EventType:     "provider.started",
		Source:        "daemon://runtime/provider",
		SourceEventID: "stable-provider-start",
		OccurredAt:    pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
		SchemaVersion: 1,
		Data:          []byte(`{"attempt":1}`),
	}
	first, err := testHandler.Queries.AppendAgentTaskEvent(ctx, params)
	if err != nil {
		t.Fatalf("append observation: %v", err)
	}
	second, err := testHandler.Queries.AppendAgentTaskEvent(ctx, params)
	if err != nil {
		t.Fatalf("replay observation: %v", err)
	}
	if first.ID != second.ID || first.Sequence != second.Sequence {
		t.Fatalf("idempotent replay created a new event: first=%#v second=%#v", first, second)
	}

	params.Data = []byte(`{"attempt":2}`)
	_, err = testHandler.Queries.AppendAgentTaskEvent(ctx, params)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("same id with changed data error = %v, want SQLSTATE 23505", err)
	}

	params.Data = []byte(`{"attempt":1}`)
	params.EventType = "provider.exited"
	_, err = testHandler.Queries.AppendAgentTaskEvent(ctx, params)
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("conflicting idempotency key error = %v, want SQLSTATE 23505", err)
	}

	const concurrentEvents = 12
	var (
		wg      sync.WaitGroup
		errs    = make(chan error, concurrentEvents)
		started = make(chan struct{})
	)
	for i := 0; i < concurrentEvents; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-started
			_, appendErr := testHandler.Queries.AppendAgentTaskEvent(context.Background(), db.AppendAgentTaskEventParams{
				TaskID:        taskUUID,
				EventType:     "journal.delivery_acked",
				Source:        "task-token://" + taskID + "/journal",
				SourceEventID: fmt.Sprintf("concurrent-%02d", i),
				OccurredAt:    pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
				SchemaVersion: 1,
				Data:          []byte(`{}`),
			})
			errs <- appendErr
		}(i)
	}
	close(started)
	wg.Wait()
	close(errs)
	for appendErr := range errs {
		if appendErr != nil {
			t.Fatalf("concurrent append: %v", appendErr)
		}
	}

	events, err = testHandler.Queries.ListAgentTaskEvents(ctx, taskUUID)
	if err != nil {
		t.Fatalf("list events after concurrent append: %v", err)
	}
	for i, event := range events {
		if event.Sequence != int64(i+1) {
			t.Fatalf("sequence gap at index %d: got %d, want %d", i, event.Sequence, i+1)
		}
	}
}

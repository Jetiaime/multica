package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	taskEventBodyLimit = 64 << 10
	// Keep this aligned with staleThresholdSeconds in
	// cmd/server/runtime_sweeper.go: an online row older than the sweeper's
	// durable-heartbeat window is uncertain, not proof of liveness.
	taskRuntimeFreshnessLimit = 150 * time.Second
)

var taskTokenObservationComponents = map[string]map[string]bool{
	"provider": {
		"provider.started": true,
		"provider.exited":  true,
	},
	"wrapper": {
		"wrapper.exited": true,
	},
	"journal": {
		"journal.delivery_acked": true,
	},
}

var daemonObservationComponents = map[string]map[string]bool{
	"provider": {
		"provider.started": true,
		"provider.exited":  true,
	},
	"wrapper": {
		"wrapper.exited": true,
	},
	"slot": {
		"slot.acquired": true,
		"slot.released": true,
	},
}

type TaskEventResponse struct {
	ID            string          `json:"id"`
	TaskID        string          `json:"task_id"`
	Sequence      int64           `json:"sequence"`
	Type          string          `json:"type"`
	Source        string          `json:"source"`
	SourceEventID *string         `json:"source_event_id,omitempty"`
	Time          time.Time       `json:"time"`
	ObservedAt    time.Time       `json:"observed_at"`
	SchemaVersion int32           `json:"schema_version"`
	Data          json.RawMessage `json:"data"`
}

type TaskCondition struct {
	Type               string    `json:"type"`
	Status             string    `json:"status"`
	Reason             string    `json:"reason"`
	Message            string    `json:"message,omitempty"`
	LastTransitionTime time.Time `json:"last_transition_time"`
}

type TaskRunStatusResponse struct {
	TaskID          string          `json:"task_id"`
	IssueID         string          `json:"issue_id,omitempty"`
	AgentID         string          `json:"agent_id"`
	RuntimeID       string          `json:"runtime_id,omitempty"`
	TaskStatus      string          `json:"task_status"`
	HistoryComplete bool            `json:"history_complete"`
	ObservedAt      time.Time       `json:"observed_at"`
	Conditions      []TaskCondition `json:"conditions"`
}

type recordTaskObservationRequest struct {
	ID            string         `json:"id"`
	Type          string         `json:"type"`
	Component     string         `json:"component"`
	Time          *time.Time     `json:"time,omitempty"`
	SchemaVersion int32          `json:"schema_version,omitempty"`
	Data          map[string]any `json:"data,omitempty"`
}

func taskEventResponse(event db.AgentTaskEvent) TaskEventResponse {
	data := json.RawMessage(event.Data)
	if len(data) == 0 {
		data = json.RawMessage(`{}`)
	}
	return TaskEventResponse{
		ID:            uuidToString(event.ID),
		TaskID:        uuidToString(event.TaskID),
		Sequence:      event.Sequence,
		Type:          event.EventType,
		Source:        event.Source,
		SourceEventID: textToPtr(event.SourceEventID),
		Time:          event.OccurredAt.Time,
		ObservedAt:    event.ObservedAt.Time,
		SchemaVersion: event.SchemaVersion,
		Data:          data,
	}
}

func taskEventResponses(events []db.AgentTaskEvent) []TaskEventResponse {
	out := make([]TaskEventResponse, len(events))
	for i, event := range events {
		out[i] = taskEventResponse(event)
	}
	return out
}

func (h *Handler) loadTaskForCurrentWorkspace(w http.ResponseWriter, r *http.Request, taskID string) (db.AgentTaskQueue, pgtype.UUID, bool) {
	taskUUID, ok := parseUUIDOrBadRequest(w, taskID, "task_id")
	if !ok {
		return db.AgentTaskQueue{}, pgtype.UUID{}, false
	}

	task, err := h.Queries.GetAgentTask(r.Context(), taskUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return db.AgentTaskQueue{}, pgtype.UUID{}, false
	}

	workspaceID := h.TaskService.ResolveTaskWorkspaceID(r.Context(), task)
	if workspaceID == "" || workspaceID != middleware.WorkspaceIDFromContext(r.Context()) {
		writeError(w, http.StatusNotFound, "task not found")
		return db.AgentTaskQueue{}, pgtype.UUID{}, false
	}
	return task, taskUUID, true
}

// RecordTaskObservation accepts low-frequency provider/wrapper/journal facts
// from the task-scoped process. The auth middleware binds X-Task-ID to the
// mat_ token, so a task can only append observations to its own ledger.
func (h *Handler) RecordTaskObservation(w http.ResponseWriter, r *http.Request) {
	taskID := strings.TrimSpace(chiURLParam(r, "taskId"))
	if r.Header.Get("X-Actor-Source") != "task_token" || r.Header.Get("X-Task-ID") != taskID {
		writeError(w, http.StatusForbidden, "task event writes require the task's own token")
		return
	}

	_, taskUUID, ok := h.loadTaskForCurrentWorkspace(w, r, taskID)
	if !ok {
		return
	}

	h.recordTaskObservation(w, r, taskUUID, "task-token://"+taskID+"/", taskTokenObservationComponents)
}

// RecordDaemonTaskObservation accepts the process and capacity facts only the
// daemon can observe authoritatively. Task-token observations remain useful
// forensic evidence, but status projection deliberately ignores them for
// provider and slot liveness because the spawned agent holds that token too.
func (h *Handler) RecordDaemonTaskObservation(w http.ResponseWriter, r *http.Request) {
	taskID := strings.TrimSpace(chiURLParam(r, "taskId"))
	task, ok := h.requireDaemonTaskAccess(w, r, taskID)
	if !ok {
		return
	}

	runtimeID := uuidToString(task.RuntimeID)
	if runtimeID == "" {
		writeError(w, http.StatusConflict, "task has no runtime")
		return
	}
	h.recordTaskObservation(w, r, task.ID, "daemon://"+runtimeID+"/", daemonObservationComponents)
}

func (h *Handler) recordTaskObservation(
	w http.ResponseWriter,
	r *http.Request,
	taskUUID pgtype.UUID,
	sourcePrefix string,
	allowedComponents map[string]map[string]bool,
) {
	r.Body = http.MaxBytesReader(w, r.Body, taskEventBodyLimit)
	var req recordTaskObservationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.ID = strings.TrimSpace(req.ID)
	req.Type = strings.TrimSpace(req.Type)
	req.Component = strings.TrimSpace(req.Component)
	if req.ID == "" || len(req.ID) > 200 {
		writeError(w, http.StatusBadRequest, "id must be 1-200 characters")
		return
	}
	allowedTypes, ok := allowedComponents[req.Component]
	if !ok || !allowedTypes[req.Type] {
		writeError(w, http.StatusBadRequest, "unsupported task observation")
		return
	}
	if req.SchemaVersion == 0 {
		req.SchemaVersion = 1
	}
	if req.SchemaVersion < 1 {
		writeError(w, http.StatusBadRequest, "schema_version must be positive")
		return
	}

	data := []byte(`{}`)
	if req.Data != nil {
		var err error
		data, err = json.Marshal(req.Data)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid event data")
			return
		}
	}
	var occurredAt pgtype.Timestamptz
	if req.Time != nil {
		occurredAt = pgtype.Timestamptz{Time: req.Time.UTC(), Valid: true}
	}

	event, err := h.Queries.AppendAgentTaskEvent(r.Context(), db.AppendAgentTaskEventParams{
		TaskID:        taskUUID,
		EventType:     req.Type,
		Source:        sourcePrefix + req.Component,
		SourceEventID: req.ID,
		OccurredAt:    occurredAt,
		SchemaVersion: req.SchemaVersion,
		Data:          data,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "event id already belongs to another event")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to record task event")
		return
	}
	writeJSON(w, http.StatusOK, taskEventResponse(event))
}

func (h *Handler) ListTaskEventsByUser(w http.ResponseWriter, r *http.Request) {
	taskID := chiURLParam(r, "taskId")
	_, taskUUID, ok := h.loadTaskForCurrentWorkspace(w, r, taskID)
	if !ok {
		return
	}

	var (
		events []db.AgentTaskEvent
		err    error
	)
	if sinceValue := r.URL.Query().Get("since"); sinceValue != "" {
		since, parseErr := strconv.ParseInt(sinceValue, 10, 64)
		if parseErr != nil || since < 0 {
			writeError(w, http.StatusBadRequest, "invalid since parameter")
			return
		}
		events, err = h.Queries.ListAgentTaskEventsSince(r.Context(), db.ListAgentTaskEventsSinceParams{
			TaskID:   taskUUID,
			Sequence: since,
		})
	} else {
		events, err = h.Queries.ListAgentTaskEvents(r.Context(), taskUUID)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list task events")
		return
	}
	writeJSON(w, http.StatusOK, taskEventResponses(events))
}

func (h *Handler) GetTaskRunStatusByUser(w http.ResponseWriter, r *http.Request) {
	taskID := chiURLParam(r, "taskId")
	task, taskUUID, ok := h.loadTaskForCurrentWorkspace(w, r, taskID)
	if !ok {
		return
	}

	events, err := h.Queries.ListAgentTaskEvents(r.Context(), taskUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list task events")
		return
	}

	var runtime *db.AgentRuntime
	if task.RuntimeID.Valid {
		value, runtimeErr := h.Queries.GetAgentRuntime(r.Context(), task.RuntimeID)
		if runtimeErr == nil {
			runtime = &value
		} else if !errors.Is(runtimeErr, pgx.ErrNoRows) {
			writeError(w, http.StatusInternalServerError, "failed to load task runtime")
			return
		}
	}

	now := time.Now().UTC()
	writeJSON(w, http.StatusOK, TaskRunStatusResponse{
		TaskID:          taskID,
		IssueID:         uuidToString(task.IssueID),
		AgentID:         uuidToString(task.AgentID),
		RuntimeID:       uuidToString(task.RuntimeID),
		TaskStatus:      task.Status,
		HistoryComplete: len(events) > 0 && events[0].Sequence == 1 && events[0].Source == "server.task_queue",
		ObservedAt:      now,
		Conditions:      projectTaskConditions(task, runtime, events, now),
	})
}

func projectTaskConditions(task db.AgentTaskQueue, runtime *db.AgentRuntime, events []db.AgentTaskEvent, now time.Time) []TaskCondition {
	runActive := conditionForRunActive(task)
	runtimeAlive := conditionForRuntime(runtime, now)
	providerAlive := conditionForProvider(task, runtimeAlive, events)
	slotHeld := conditionForSlot(task, runtimeAlive, providerAlive, events, now)
	stalled := conditionForStalled(task, runtimeAlive, providerAlive, slotHeld, now)
	return []TaskCondition{runActive, runtimeAlive, providerAlive, slotHeld, stalled}
}

func conditionForRunActive(task db.AgentTaskQueue) TaskCondition {
	if isTerminalTaskStatus(task.Status) {
		return taskCondition("RunActive", "False", "TaskTerminal", "the task is terminal", taskStateTime(task))
	}
	return taskCondition("RunActive", "True", "TaskNonTerminal", "the task has not reached a terminal state", taskStateTime(task))
}

func conditionForRuntime(runtime *db.AgentRuntime, now time.Time) TaskCondition {
	if runtime == nil {
		return taskCondition("RuntimeAlive", "Unknown", "RuntimeMissing", "the task has no resolvable runtime", now)
	}
	if runtime.Status != "online" {
		return taskCondition("RuntimeAlive", "False", "RuntimeOffline", "the runtime is explicitly offline", timestampOr(runtime.UpdatedAt, now))
	}
	if !runtime.LastSeenAt.Valid {
		return taskCondition("RuntimeAlive", "Unknown", "HeartbeatMissing", "the runtime has no durable heartbeat timestamp", timestampOr(runtime.UpdatedAt, now))
	}
	if now.Sub(runtime.LastSeenAt.Time) > taskRuntimeFreshnessLimit {
		return taskCondition("RuntimeAlive", "Unknown", "HeartbeatStale", "the runtime is marked online but its durable heartbeat is stale", runtime.LastSeenAt.Time)
	}
	return taskCondition("RuntimeAlive", "True", "HeartbeatFresh", "the runtime has a fresh durable heartbeat", runtime.LastSeenAt.Time)
}

func conditionForProvider(task db.AgentTaskQueue, runtime TaskCondition, events []db.AgentTaskEvent) TaskCondition {
	started := latestAuthoritativeTaskEvent(events, "provider.started")
	exited := latestAuthoritativeTaskEvent(events, "provider.exited")
	if exited != nil && (started == nil || !exited.OccurredAt.Time.Before(started.OccurredAt.Time)) {
		return taskCondition("ProviderAlive", "False", "ProviderExited", "the provider process reported exit", exited.OccurredAt.Time)
	}
	if started == nil {
		return taskCondition("ProviderAlive", "Unknown", "ProviderUnobserved", "no provider lifecycle observation has been recorded", taskStateTime(task))
	}
	if isTerminalTaskStatus(task.Status) {
		return taskCondition("ProviderAlive", "Unknown", "ExitUnobserved", "the task is terminal but no authoritative provider exit was observed", taskStateTime(task))
	}
	if runtime.Status != "True" {
		return taskCondition("ProviderAlive", "Unknown", "RuntimeUncertain", "the provider started but runtime liveness is not proven", runtime.LastTransitionTime)
	}
	return taskCondition("ProviderAlive", "True", "ProviderStarted", "the provider started and its runtime remains live", started.OccurredAt.Time)
}

func conditionForSlot(task db.AgentTaskQueue, runtime, provider TaskCondition, events []db.AgentTaskEvent, now time.Time) TaskCondition {
	acquired := latestAuthoritativeTaskEvent(events, "slot.acquired")
	released := latestAuthoritativeTaskEvent(events, "slot.released")
	if released != nil && (acquired == nil || !released.OccurredAt.Time.Before(acquired.OccurredAt.Time)) {
		return taskCondition("SlotHeld", "False", "ReleaseObserved", "the daemon reported slot release", released.OccurredAt.Time)
	}
	if acquired != nil {
		return taskCondition("SlotHeld", "True", "AcquisitionObserved", "the daemon reported slot acquisition without a later release", acquired.OccurredAt.Time)
	}
	if task.Status == "queued" || task.Status == "deferred" {
		return taskCondition("SlotHeld", "False", "NotDispatched", "the task has not been dispatched", taskStateTime(task))
	}
	if isTerminalTaskStatus(task.Status) {
		return taskCondition("SlotHeld", "Unknown", "ReleaseUnobserved", "the task is terminal but no authoritative slot release was observed", taskStateTime(task))
	}
	if task.PrepareLeaseExpiresAt.Valid && task.PrepareLeaseExpiresAt.Time.After(now) {
		return taskCondition("SlotHeld", "True", "PrepareLeaseActive", "the pre-start execution lease is active", taskStateTime(task))
	}
	if provider.Status == "True" {
		return taskCondition("SlotHeld", "True", "ProviderObserved", "provider liveness currently proves the slot holder", taskStateTime(task))
	}
	return taskCondition("SlotHeld", "Unknown", "ReleaseUnobserved", "the task is active but no live holder or explicit release is proven", taskStateTime(task))
}

func conditionForStalled(task db.AgentTaskQueue, runtime, provider, slot TaskCondition, now time.Time) TaskCondition {
	if isTerminalTaskStatus(task.Status) {
		return taskCondition("Stalled", "False", "TaskTerminal", "the task is terminal", taskStateTime(task))
	}
	if provider.Status == "False" {
		return taskCondition("Stalled", "True", "ProviderExitedBeforeTerminal", "the provider exited while the task remained non-terminal", provider.LastTransitionTime)
	}
	if task.Status == "dispatched" && task.PrepareLeaseExpiresAt.Valid && !task.PrepareLeaseExpiresAt.Time.After(now) {
		return taskCondition("Stalled", "True", "PrepareLeaseExpired", "the dispatched task's prepare lease expired", task.PrepareLeaseExpiresAt.Time)
	}
	if runtime.Status == "False" {
		return taskCondition("Stalled", "True", "RuntimeOffline", "the task is active on an explicitly offline runtime", runtime.LastTransitionTime)
	}
	if runtime.Status == "Unknown" || slot.Status == "Unknown" {
		return taskCondition("Stalled", "Unknown", "EvidenceIncomplete", "current evidence cannot prove progress or a stall", taskStateTime(task))
	}
	return taskCondition("Stalled", "False", "ExecutionObserved", "current execution evidence is consistent", taskStateTime(task))
}

func taskCondition(kind, status, reason, message string, transitionedAt time.Time) TaskCondition {
	return TaskCondition{
		Type:               kind,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: transitionedAt.UTC(),
	}
}

func latestAuthoritativeTaskEvent(events []db.AgentTaskEvent, eventType string) *db.AgentTaskEvent {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].EventType == eventType && strings.HasPrefix(events[i].Source, "daemon://") {
			return &events[i]
		}
	}
	return nil
}

func isTerminalTaskStatus(status string) bool {
	switch status {
	case "completed", "failed", "cancelled":
		return true
	default:
		return false
	}
}

func taskStateTime(task db.AgentTaskQueue) time.Time {
	switch {
	case task.CompletedAt.Valid:
		return task.CompletedAt.Time
	case task.StartedAt.Valid:
		return task.StartedAt.Time
	case task.DispatchedAt.Valid:
		return task.DispatchedAt.Time
	default:
		return task.CreatedAt.Time
	}
}

func timestampOr(value pgtype.Timestamptz, fallback time.Time) time.Time {
	if value.Valid {
		return value.Time
	}
	return fallback
}

func chiURLParam(r *http.Request, key string) string {
	return strings.TrimSpace(chi.URLParam(r, key))
}

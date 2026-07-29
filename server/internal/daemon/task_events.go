package daemon

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

const taskObservationReportTimeout = 3 * time.Second

func newTaskObservationID() string {
	return uuid.NewString()
}

func (d *Daemon) recordTaskObservation(
	taskID string,
	eventID string,
	eventType string,
	component string,
	occurredAt time.Time,
	data map[string]any,
	taskLog *slog.Logger,
) {
	if d == nil || d.client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), taskObservationReportTimeout)
	defer cancel()
	if err := d.client.RecordTaskObservation(ctx, taskID, TaskObservation{
		ID:            eventID,
		Type:          eventType,
		Component:     component,
		Time:          occurredAt.UTC(),
		SchemaVersion: 1,
		Data:          data,
	}); err != nil {
		// A newer daemon may briefly talk to an older server without the event
		// route during rolling upgrades. Execution remains authoritative; loss
		// of an observation is surfaced at debug level and as Unknown status
		// when no other authoritative evidence can prove the condition.
		taskLog.Debug("record task observation failed", "type", eventType, "error", err)
	}
}

-- Append one event while serializing on the task row. Both status transitions
-- and external lifecycle observations use this function, so sequence allocation
-- is monotonic per task even when daemon callbacks race server-side sweepers.
CREATE OR REPLACE FUNCTION append_agent_task_event(
    p_task_id UUID,
    p_event_type TEXT,
    p_source TEXT,
    p_source_event_id TEXT,
    p_occurred_at TIMESTAMPTZ,
    p_schema_version INT,
    p_data JSONB
)
RETURNS agent_task_event
LANGUAGE plpgsql
AS $$
DECLARE
    v_task agent_task_queue%ROWTYPE;
    v_workspace_id UUID;
    v_event agent_task_event%ROWTYPE;
    v_sequence BIGINT;
BEGIN
    SELECT t.*
      INTO v_task
      FROM agent_task_queue t
     WHERE t.id = p_task_id
     FOR UPDATE;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'agent task % not found', p_task_id USING ERRCODE = 'P0002';
    END IF;

    SELECT a.workspace_id
      INTO v_workspace_id
      FROM agent a
     WHERE a.id = v_task.agent_id;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'agent for task % not found', p_task_id USING ERRCODE = 'P0002';
    END IF;

    IF p_source_event_id IS NOT NULL THEN
        SELECT *
          INTO v_event
          FROM agent_task_event
         WHERE source = p_source
           AND source_event_id = p_source_event_id;
        IF FOUND THEN
            IF v_event.task_id <> p_task_id
               OR v_event.event_type <> p_event_type
               OR v_event.schema_version <> COALESCE(p_schema_version, 1)
               OR v_event.data <> COALESCE(NULLIF(p_data, 'null'::jsonb), '{}'::jsonb)
               OR (
                   p_occurred_at IS NOT NULL
                   AND v_event.occurred_at <> p_occurred_at
               )
            THEN
                RAISE EXCEPTION 'source event id already belongs to another event'
                    USING ERRCODE = '23505';
            END IF;
            RETURN v_event;
        END IF;
    END IF;

    SELECT COALESCE(MAX(sequence), 0) + 1
      INTO v_sequence
      FROM agent_task_event
     WHERE task_id = p_task_id;

    INSERT INTO agent_task_event (
        task_id,
        workspace_id,
        issue_id,
        runtime_id,
        sequence,
        event_type,
        source,
        source_event_id,
        occurred_at,
        schema_version,
        data
    ) VALUES (
        p_task_id,
        v_workspace_id,
        v_task.issue_id,
        v_task.runtime_id,
        v_sequence,
        p_event_type,
        p_source,
        p_source_event_id,
        COALESCE(p_occurred_at, now()),
        COALESCE(p_schema_version, 1),
        COALESCE(NULLIF(p_data, 'null'::jsonb), '{}'::jsonb)
    )
    RETURNING * INTO v_event;

    RETURN v_event;
END;
$$;

CREATE OR REPLACE FUNCTION capture_agent_task_status_event()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    v_occurred_at TIMESTAMPTZ;
    v_from_status TEXT;
BEGIN
    IF TG_OP = 'UPDATE' AND OLD.status IS NOT DISTINCT FROM NEW.status THEN
        RETURN NEW;
    END IF;

    IF TG_OP = 'UPDATE' THEN
        v_from_status := OLD.status;
    END IF;

    v_occurred_at := CASE
        WHEN TG_OP = 'INSERT' THEN NEW.created_at
        WHEN NEW.status = 'dispatched' THEN COALESCE(NEW.dispatched_at, now())
        WHEN NEW.status = 'running' THEN COALESCE(NEW.started_at, now())
        WHEN NEW.status IN ('completed', 'failed', 'cancelled') THEN COALESCE(NEW.completed_at, now())
        ELSE now()
    END;

    PERFORM append_agent_task_event(
        NEW.id,
        'task.' || NEW.status,
        'server.task_queue',
        NULL,
        v_occurred_at,
        1,
        jsonb_strip_nulls(jsonb_build_object(
            'from_status', v_from_status,
            'to_status', NEW.status,
            'failure_reason', NEW.failure_reason,
            'wait_reason', NEW.wait_reason
        ))
    );

    RETURN NEW;
END;
$$;

CREATE TRIGGER capture_agent_task_status_event
AFTER INSERT OR UPDATE OF status ON agent_task_queue
FOR EACH ROW
EXECUTE FUNCTION capture_agent_task_status_event();

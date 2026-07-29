CREATE UNIQUE INDEX CONCURRENTLY agent_task_event_source_event_uidx
    ON agent_task_event (source, source_event_id)
    WHERE source_event_id IS NOT NULL;

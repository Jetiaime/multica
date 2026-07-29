CREATE UNIQUE INDEX CONCURRENTLY agent_task_event_task_sequence_uidx
    ON agent_task_event (task_id, sequence);

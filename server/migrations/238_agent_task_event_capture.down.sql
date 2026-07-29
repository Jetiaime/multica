DROP TRIGGER IF EXISTS capture_agent_task_status_event ON agent_task_queue;
DROP FUNCTION IF EXISTS capture_agent_task_status_event();
DROP FUNCTION IF EXISTS append_agent_task_event(UUID, TEXT, TEXT, TEXT, TIMESTAMPTZ, INT, JSONB);

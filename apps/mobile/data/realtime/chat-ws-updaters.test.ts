import { describe, expect, it, vi } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import type { ChatDonePayload, ChatMessage, ChatPendingTask } from "@multica/core/types";

// chat-ws-updaters imports chatKeys from data/queries/chat, which transitively
// imports the native fetch client. Mock it so the Node test never loads RN
// modules — chatKeys itself is a pure key factory and needs nothing from api.
vi.mock("@/data/api", () => ({ api: {} }));

import {
  applyChatDoneToCache,
  promotePendingTaskToRunning,
  seedPendingTaskFromQueued,
} from "./chat-ws-updaters";
import { chatKeys } from "@/data/queries/chat";

const SESSION = "session-1";

function donePayload(over: Partial<ChatDonePayload> = {}): ChatDonePayload {
  return {
    chat_session_id: SESSION,
    task_id: "task-1",
    message_id: "msg-1",
    content: "here is the chart",
    created_at: "2026-07-09T00:00:00Z",
    elapsed_ms: 1200,
    ...over,
  };
}

describe("applyChatDoneToCache", () => {
  it("patches the assistant bubble inline AND invalidates messages so bound attachments refetch", () => {
    const qc = new QueryClient();
    qc.setQueryData<ChatMessage[]>(chatKeys.messages(SESSION), []);
    const invalidate = vi.spyOn(qc, "invalidateQueries");

    applyChatDoneToCache(qc, donePayload());

    // Inline patch: the bubble lands immediately (no flicker) — but without
    // attachments, because the event payload never carries them.
    const msgs = qc.getQueryData<ChatMessage[]>(chatKeys.messages(SESSION));
    expect(msgs).toHaveLength(1);
    expect(msgs?.[0].id).toBe("msg-1");
    expect(msgs?.[0].attachments).toBeUndefined();

    // Refetch: the authoritative message list (with attachments) is pulled in.
    expect(invalidate).toHaveBeenCalledWith({
      queryKey: chatKeys.messages(SESSION),
    });
    // pendingTask cleared so the status pill unmounts.
    expect(qc.getQueryData(chatKeys.pendingTask(SESSION))).toEqual({});
  });

  it("invalidates even when the payload lacks an inline message (legacy shape)", () => {
    const qc = new QueryClient();
    const invalidate = vi.spyOn(qc, "invalidateQueries");

    applyChatDoneToCache(
      qc,
      donePayload({ message_id: undefined, content: undefined, created_at: undefined }),
    );

    expect(invalidate).toHaveBeenCalledWith({
      queryKey: chatKeys.messages(SESSION),
    });
  });

  it("does not duplicate an echoed message on reconnect", () => {
    const qc = new QueryClient();
    const existing: ChatMessage = {
      id: "msg-1",
      chat_session_id: SESSION,
      role: "assistant",
      content: "here is the chart",
      task_id: "task-1",
      created_at: "2026-07-09T00:00:00Z",
    };
    qc.setQueryData<ChatMessage[]>(chatKeys.messages(SESSION), [existing]);

    applyChatDoneToCache(qc, donePayload());

    expect(qc.getQueryData<ChatMessage[]>(chatKeys.messages(SESSION))).toHaveLength(1);
  });
});

describe("pending task queue WS updates", () => {
  it("does not replace the active task when a follow-up is queued", () => {
    const qc = new QueryClient();
    qc.setQueryData<ChatPendingTask>(chatKeys.pendingTask(SESSION), {
      task_id: "active",
      status: "running",
      created_at: "2026-07-09T00:00:00Z",
      queued_tasks: [],
    });

    seedPendingTaskFromQueued(qc, {
      task_id: "next",
      agent_id: "agent-1",
      issue_id: "",
      chat_session_id: SESSION,
      status: "queued",
    });

    const pending = qc.getQueryData<ChatPendingTask>(chatKeys.pendingTask(SESSION));
    expect(pending?.task_id).toBe("active");
    expect(pending?.queued_tasks?.map((task) => task.task_id)).toEqual(["next"]);
  });

  it("promotes the dispatched follow-up and keeps the remaining FIFO queue", () => {
    const qc = new QueryClient();
    qc.setQueryData<ChatPendingTask>(chatKeys.pendingTask(SESSION), {
      task_id: "active",
      status: "running",
      created_at: "2026-07-09T00:00:00Z",
      queued_tasks: [
        { task_id: "next", status: "queued", created_at: "2026-07-09T00:00:01Z" },
        { task_id: "later", status: "queued", created_at: "2026-07-09T00:00:02Z" },
      ],
    });

    applyChatDoneToCache(qc, donePayload({ task_id: "active" }));
    promotePendingTaskToRunning(qc, {
      task_id: "next",
      agent_id: "agent-1",
      issue_id: "",
      runtime_id: "runtime-1",
      chat_session_id: SESSION,
    });

    const pending = qc.getQueryData<ChatPendingTask>(chatKeys.pendingTask(SESSION));
    expect(pending?.task_id).toBe("next");
    expect(pending?.status).toBe("running");
    expect(pending?.queued_tasks?.map((task) => task.task_id)).toEqual(["later"]);
  });
});

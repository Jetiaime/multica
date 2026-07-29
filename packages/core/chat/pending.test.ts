import { describe, expect, it } from "vitest";
import {
  enqueuePendingChatTask,
  promotePendingChatTask,
  removePendingChatTask,
} from "./pending";

const task = (task_id: string, created_at: string) => ({
  task_id,
  status: "queued",
  created_at,
  content: `message ${task_id}`,
});

describe("pending chat queue", () => {
  it("keeps the active task at the head and follow-ups in FIFO order", () => {
    const active = { ...task("active", "2026-01-01T00:00:00Z"), status: "running" };
    const withLater = enqueuePendingChatTask(active, task("later", "2026-01-01T00:00:02Z"));
    const result = enqueuePendingChatTask(withLater, task("next", "2026-01-01T00:00:01Z"));

    expect(result.task_id).toBe("active");
    expect(result.queued_tasks?.map((item) => item.task_id)).toEqual(["next", "later"]);
  });

  it("keeps a send response preview when a sparse queued event arrives later", () => {
    const current = {
      ...task("active", "2026-01-01T00:00:00Z"),
      status: "running",
      queued_tasks: [
        {
          ...task("next", "2026-01-01T00:00:01Z"),
          message_id: "message-next",
          content: "Keep this visible preview",
        },
      ],
    };

    const result = enqueuePendingChatTask(current, {
      task_id: "next",
      status: "queued",
      created_at: "2026-01-01T00:00:03Z",
    });

    expect(result.queued_tasks).toEqual([
      expect.objectContaining({
        task_id: "next",
        created_at: "2026-01-01T00:00:01Z",
        message_id: "message-next",
        content: "Keep this visible preview",
      }),
    ]);
  });

  it("promotes a queued task without losing later work", () => {
    const current = {
      ...task("active", "2026-01-01T00:00:00Z"),
      status: "running",
      queued_tasks: [
        task("next", "2026-01-01T00:00:01Z"),
        task("later", "2026-01-01T00:00:02Z"),
      ],
    };

    const result = promotePendingChatTask(current, "next", "running");
    expect(result.task_id).toBe("next");
    expect(result.status).toBe("running");
    expect(result.queued_tasks?.map((item) => item.task_id)).toEqual(["later"]);
  });

  it("ignores a stale dispatch for an unknown task while another task is active", () => {
    const current = {
      ...task("active", "2026-01-01T00:00:00Z"),
      status: "running",
      queued_tasks: [task("next", "2026-01-01T00:00:01Z")],
    };

    expect(promotePendingChatTask(current, "stale", "running")).toBe(current);
  });

  it("removes only the selected queued task", () => {
    const current = {
      ...task("active", "2026-01-01T00:00:00Z"),
      status: "running",
      queued_tasks: [task("next", "2026-01-01T00:00:01Z")],
    };

    expect(removePendingChatTask(current, "next")).toEqual({
      ...current,
      queued_tasks: [],
    });
  });

  it("promotes the first queued task when the active task finishes", () => {
    const current = {
      ...task("active", "2026-01-01T00:00:00Z"),
      status: "running",
      queued_tasks: [
        task("next", "2026-01-01T00:00:01Z"),
        task("later", "2026-01-01T00:00:02Z"),
      ],
    };

    const result = removePendingChatTask(current, "active");
    expect(result.task_id).toBe("next");
    expect(result.queued_tasks?.map((item) => item.task_id)).toEqual(["later"]);
  });
});

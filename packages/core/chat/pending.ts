import type { ChatPendingTask, ChatQueuedTask } from "../types/chat";

const EMPTY_PENDING_TASK: ChatPendingTask = {};

function compareQueuedTasks(left: ChatQueuedTask, right: ChatQueuedTask): number {
  const leftTime = Date.parse(left.created_at);
  const rightTime = Date.parse(right.created_at);
  if (Number.isFinite(leftTime) && Number.isFinite(rightTime) && leftTime !== rightTime) {
    return leftTime - rightTime;
  }
  return left.task_id.localeCompare(right.task_id);
}

function normalizeQueue(tasks: ChatQueuedTask[]): ChatQueuedTask[] {
  const byID = new Map<string, ChatQueuedTask>();
  // Keep the first observation of a task. The send response carries the
  // authoritative created_at plus the message preview, while the later
  // task:queued event only carries an id/status. Letting that sparse event win
  // would replace visible text with the fallback label.
  for (const task of tasks) {
    if (!byID.has(task.task_id)) byID.set(task.task_id, task);
  }
  return [...byID.values()].sort(compareQueuedTasks);
}

function asSummary(task: ChatPendingTask): ChatQueuedTask | undefined {
  if (!task.task_id) return undefined;
  return {
    task_id: task.task_id,
    status: task.status ?? "queued",
    created_at: task.created_at ?? "",
  };
}

export function enqueuePendingChatTask(
  current: ChatPendingTask | undefined,
  task: ChatQueuedTask,
): ChatPendingTask {
  if (!current?.task_id) {
    return { ...task, queued_tasks: [] };
  }
  if (current.task_id === task.task_id) {
    return {
      ...current,
      status: task.status,
      created_at: current.created_at || task.created_at,
      queued_tasks: current.queued_tasks ?? [],
    };
  }
  return {
    ...current,
    queued_tasks: normalizeQueue([...(current.queued_tasks ?? []), task]),
  };
}

export function promotePendingChatTask(
  current: ChatPendingTask | undefined,
  taskID: string,
  status: string,
  createdAt?: string,
): ChatPendingTask {
  if (current?.task_id === taskID) return { ...current, status };

  const queue = current?.queued_tasks ?? [];
  const promoted = queue.find((task) => task.task_id === taskID);
  if (current?.task_id && current.status !== "queued" && !promoted) {
    return current;
  }
  const previousHead = asSummary(current ?? {});
  return {
    ...(promoted ?? {
      task_id: taskID,
      status,
      created_at: createdAt ?? "",
    }),
    status,
    created_at: promoted?.created_at ?? createdAt ?? "",
    queued_tasks: normalizeQueue([
      ...(previousHead?.status === "queued" ? [previousHead] : []),
      ...queue.filter((task) => task.task_id !== taskID),
    ]),
  };
}

export function removePendingChatTask(
  current: ChatPendingTask | undefined,
  taskID: string,
): ChatPendingTask {
  if (!current?.task_id) return EMPTY_PENDING_TASK;
  if (current.task_id !== taskID) {
    return {
      ...current,
      queued_tasks: (current.queued_tasks ?? []).filter((task) => task.task_id !== taskID),
    };
  }

  const [next, ...rest] = normalizeQueue(current.queued_tasks ?? []);
  if (!next) return EMPTY_PENDING_TASK;
  return { ...next, queued_tasks: rest };
}

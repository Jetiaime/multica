import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import { describe, expect, it, vi } from "vitest";
import enChat from "../../locales/en/chat.json";
import { ChatQueue } from "./chat-queue";

const TEST_RESOURCES = { en: { chat: enChat } };

function renderQueue(onRemove = vi.fn()) {
  render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <ChatQueue
        tasks={[
          {
            task_id: "task-2",
            status: "queued",
            content: "First follow-up",
            created_at: "2026-07-01T00:01:00Z",
          },
          {
            task_id: "task-3",
            status: "queued",
            content: "",
            created_at: "2026-07-01T00:02:00Z",
          },
        ]}
        onRemove={onRemove}
      />
    </I18nProvider>,
  );
  return onRemove;
}

describe("ChatQueue", () => {
  it("renders queued messages in order and falls back for empty content", () => {
    renderQueue();

    expect(screen.getByText("2 queued messages")).toBeInTheDocument();
    expect(screen.getByText("First follow-up")).toBeInTheDocument();
    expect(screen.getByText("Queued message")).toBeInTheDocument();
    expect(screen.getAllByLabelText("Remove queued message")).toHaveLength(2);
  });

  it("removes the selected queued task", async () => {
    const onRemove = renderQueue(vi.fn().mockResolvedValue(undefined));

    fireEvent.click(screen.getAllByLabelText("Remove queued message")[1]!);

    await waitFor(() => {
      expect(onRemove).toHaveBeenCalledWith("task-3");
    });
  });
});

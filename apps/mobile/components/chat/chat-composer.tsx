/**
 * Chat composer — thin wrapper around the shared `<MessageComposer>` with
 * chat-specific wiring:
 *
 *   - **Controlled text**: parent (chat.tsx) owns the draft via
 *     `useChatDraftsStore` so switching sessions rehydrates the right
 *     draft. Pass `value` + `onChangeText` through.
 *   - **Stop + queue buttons**: while an agent task is running for the active
 *     session, `sending` keeps Stop visible and leaves Send beside it so a
 *     follow-up can join the FIFO queue.
 *   - **Mention picker mode=chat**: chat is user ↔ single agent so
 *     @member / @agent / @squad / @all are noise + would notify the
 *     wrong people. Picker route honors `?mode=chat` and surfaces only
 *     Issues (useful for "reference this ticket for context").
 *   - **No reply target**: chat is a flat conversation; passes no
 *     reply chip.
 *   - **No upload context**: chat attachments are session-scoped; the
 *     server back-fills `chat_message_id` on each row when the message
 *     persists (server-side). `MessageComposer` calls `api.uploadFile`
 *     without `{ issueId, commentId }`.
 *   - **Parent owns keyboard**: chat.tsx wraps in KeyboardAvoidingView +
 *     SafeAreaView, so `manageKeyboard={false}` prevents the composer
 *     from double-stacking its own keyboard handling.
 *
 * Previously a hand-written 400-LOC twin of inline-comment-composer.tsx;
 * now ~50 LOC plus the StopButton subcomponent.
 */
import { useCallback } from "react";
import { Pressable, Text, View } from "react-native";
import Animated, { FadeIn, FadeOut } from "react-native-reanimated";
import { Ionicons } from "@expo/vector-icons";
import * as Haptics from "expo-haptics";
import { MessageComposer } from "@/components/composer/message-composer";
import { useWorkspaceStore } from "@/data/workspace-store";
import { useColorScheme } from "@/lib/use-color-scheme";
import { THEME } from "@/lib/theme";
import type { ChatQueuedTask } from "@multica/core/types";

interface Props {
  /** Current draft text (controlled). Empty string = no draft. */
  value: string;
  /** Fired on every keystroke. The caller writes to the drafts store. */
  onChangeText: (next: string) => void;
  /** Send the serialised markdown content + the completed attachments'
   *  server ids. Caller resets the input by setting `value=""` after a
   *  successful send. */
  onSend: (content: string, attachmentIds: string[]) => Promise<void> | void;
  /** Cancel the in-flight agent task. Only callable while `sending===true`. */
  onStop: () => void;
  /** FIFO follow-ups waiting behind the active task. */
  queuedTasks: ChatQueuedTask[];
  /** Remove a queued task without stopping the active task. */
  onRemoveQueuedTask: (taskId: string) => Promise<void> | void;
  /** True while an agent task is running for the active session. */
  sending: boolean;
  /** Hard-disable typing + send. Used when there's no usable agent in the
   *  workspace or the session is archived (legacy). */
  disabled?: boolean;
  /** When `disabled`, replaces the pill label with the reason. */
  disabledReason?: string;
}

const IS_IOS = process.env.EXPO_OS === "ios";

export function ChatComposer({
  value,
  onChangeText,
  onSend,
  onStop,
  queuedTasks,
  onRemoveQueuedTask,
  sending,
  disabled = false,
  disabledReason,
}: Props) {
  const wsSlug = useWorkspaceStore((s) => s.currentWorkspaceSlug);
  const { colorScheme } = useColorScheme();
  const theme = THEME[colorScheme];

  const onSubmit = useCallback(
    async ({
      content,
      attachmentIds,
    }: {
      content: string;
      attachmentIds: string[];
    }) => {
      // `onSend` may be sync or async; await is safe in both cases. If it
      // throws, MessageComposer's catch restores text + chips.
      await onSend(content, attachmentIds);
    },
    [onSend],
  );

  const handleStop = useCallback(() => {
    if (IS_IOS) {
      void Haptics.impactAsync(Haptics.ImpactFeedbackStyle.Medium);
    }
    onStop();
  }, [onStop]);

  return (
    <>
      {queuedTasks.length > 0 ? (
        <View className="mx-3 mb-1 rounded-xl border border-border bg-secondary/50 px-3 py-2">
          <Text className="mb-1 text-xs font-medium text-muted-foreground">
            {queuedTasks.length} queued
          </Text>
          {queuedTasks.map((task, index) => (
            <View key={task.task_id} className="h-8 flex-row items-center gap-2">
              <Text className="w-4 text-center text-xs text-muted-foreground">
                {index + 1}
              </Text>
              <Text className="flex-1 text-sm text-foreground" numberOfLines={1}>
                {task.content?.trim() || "Queued message"}
              </Text>
              <Pressable
                onPress={() => void onRemoveQueuedTask(task.task_id)}
                hitSlop={10}
                accessibilityRole="button"
                accessibilityLabel="Remove queued message"
                className="h-7 w-7 items-center justify-center rounded-full active:bg-secondary"
              >
                <Ionicons name="close" size={17} color={theme.mutedForeground} />
              </Pressable>
            </View>
          ))}
        </View>
      ) : null}
      <MessageComposer
        value={value}
        onChangeText={onChangeText}
        onSubmit={onSubmit}
        mentionPickerPath={{
          pathname: "/[workspace]/mention-picker",
          params: { workspace: wsSlug ?? "", mode: "chat" },
        }}
        placeholder={sending ? "Queue a follow-up…" : "Message…"}
        pillLabel={
          sending
            ? "Queue a follow-up…"
            : disabled
              ? (disabledReason ?? "Chat unavailable")
              : "Message…"
        }
        pillIcon="chatbubble-ellipses-outline"
        disabled={disabled}
        disabledReason={disabledReason}
        isSending={sending}
        renderStop={() => <StopButton onPress={handleStop} />}
        allowSubmitWhileSending
        manageKeyboard={false}
      />
    </>
  );
}

function StopButton({ onPress }: { onPress: () => void }) {
  const { colorScheme } = useColorScheme();
  const theme = THEME[colorScheme];
  return (
    <Animated.View
      key="stop"
      entering={FadeIn.duration(120)}
      exiting={FadeOut.duration(120)}
    >
      <Pressable
        onPress={onPress}
        className="h-8 w-8 items-center justify-center rounded-full bg-foreground active:opacity-80"
        hitSlop={12}
        accessibilityRole="button"
        accessibilityLabel="Stop agent"
      >
        <View
          style={{
            width: 10,
            height: 10,
            backgroundColor: theme.background,
            borderRadius: 1.5,
          }}
        />
      </Pressable>
    </Animated.View>
  );
}

import { vi } from "vitest";

type SSEHandler = (chatID: string, payload: unknown) => void;
const sseHandlers = new Map<string, SSEHandler>();

export function fireSSE(event: string, chatID: string, payload: unknown): void {
  const handler = sseHandlers.get(event);
  if (handler) {
    handler(chatID, payload);
  }
}

export function createBusMock(extras: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    // bus.ts's full named surface: Browser Mode links ESM for real, so every imported name must
    // EXIST. `undefined` is what a namespace read yields, so no exercised path changes.
    dispatch: undefined,
    registerSSEDecoder: undefined,
    lookupSSEDecoder: undefined,
    onBus: undefined,
    emitBus: undefined,
    BUS_TURN_IDLE: undefined,
    BUS_RECONCILE: undefined,
    decodeEnvelope: undefined,
    BUS_PAGE_RESUMED: undefined,
    BUS_USER_INPUT_ANSWERED: undefined,
    BUS_KEYS_ESCAPE: undefined,
    BUS_ACTIVATE_CHAT: undefined,
    BUS_RUNS_CHANGED: undefined,
    BUS_TAB_CHANGED: undefined,
    BUS_EDITOR_VIEW_CHANGED: undefined,
    BUS_COMMAND_FAILED: undefined,
    onSSE: vi.fn((event: string, handler: SSEHandler) => {
      sseHandlers.set(event, handler);
    }),
    ...extras,
  };
}

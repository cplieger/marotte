// Public surface of the actions framework: re-exports @cplieger/reactive/actions for local imports.

import {
  apiAction as libApiAction,
  defineAction as libDefineAction,
  transportAction as libTransportAction,
  type Action,
  type ActionDefinition,
  type ApiActionDefinition,
} from "@cplieger/reactive/actions";
import { flushFrames } from "../frame-dispatch.js";
import { captureSubjectNames } from "./subject.js";

/** An action decides from every frame the page has received: its first step (`optimistic`, run
 *  after any scope wait) and each attempt's body apply the queued frames first. */
function afterFrames<D extends { optimistic?: ((args: never) => unknown) | undefined }>(def: D): D {
  const { optimistic } = def;
  return {
    ...def,
    optimistic: (args: never) => {
      flushFrames();
      return optimistic?.(args);
    },
  };
}

export function defineAction<TArgs, TResult, TOp = unknown>(
  def: ActionDefinition<TArgs, TResult, TOp>,
): Action<TArgs, TResult> {
  const { run } = def;
  return libDefineAction(
    captureSubjectNames<TArgs, typeof def>(
      afterFrames({
        ...def,
        run: (args, signal, ctx) => {
          flushFrames();
          return run(args, signal, ctx);
        },
      }),
    ),
  );
}

export function apiAction<TArgs, TResult = unknown, TOp = unknown>(
  def: ApiActionDefinition<TArgs, TResult, TOp>,
): Action<TArgs, TResult> {
  const { request } = def;
  return libApiAction(
    captureSubjectNames<TArgs, typeof def>(
      afterFrames({
        ...def,
        request: (args) => {
          flushFrames();
          return request(args);
        },
      }),
    ),
  );
}

export function transportAction<TArgs, TOp = unknown>(
  def: Parameters<typeof libTransportAction<TArgs, TOp>>[0],
): Action<TArgs, void> {
  const { command } = def;
  return libTransportAction(
    captureSubjectNames<TArgs, typeof def>(
      afterFrames({
        ...def,
        command: (args) => {
          flushFrames();
          return command(args);
        },
      }),
    ),
  );
}

export {
  ActionError,
  hasErrorString,
  classifyFetchError,
  retryNetwork,
  subscribeToActions,
  subscribeByName,
  isPending,
  bindLoadingState,
  registerCleanup,
  debouncedDispatch,
  pollAction,
  pollUntil,
  RETRY_STANDARD,
  IDEMPOTENCY_HEADER,
  IDEMPOTENCY_COMMAND_FIELD,
} from "@cplieger/reactive/actions";

export { withTimeout, API_TIMEOUT_MS } from "@cplieger/reactive/fetch";

export type {
  ActionContext,
  ActionErrorLike,
  ActionOutcome,
  DebouncedDispatch,
  ApiErrorInfo,
  ApiErrorDecision,
} from "@cplieger/reactive/actions";

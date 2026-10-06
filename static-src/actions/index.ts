// Public surface of the actions framework: re-exports @cplieger/actions for local imports.

import {
  apiAction as libApiAction,
  defineAction as libDefineAction,
  transportAction as libTransportAction,
  type Action,
  type ActionDefinition,
  type ApiActionDefinition,
} from "@cplieger/actions";
import { captureSubjectNames } from "./subject.js";

export function defineAction<TArgs, TResult, TOp = unknown>(
  def: ActionDefinition<TArgs, TResult, TOp>,
): Action<TArgs, TResult> {
  return libDefineAction(captureSubjectNames<TArgs, typeof def>(def));
}

export function apiAction<TArgs, TResult = unknown, TOp = unknown>(
  def: ApiActionDefinition<TArgs, TResult, TOp>,
): Action<TArgs, TResult> {
  return libApiAction(captureSubjectNames<TArgs, typeof def>(def));
}

export function transportAction<TArgs, TOp = unknown>(
  def: Parameters<typeof libTransportAction<TArgs, TOp>>[0],
): Action<TArgs, void> {
  return libTransportAction(captureSubjectNames<TArgs, typeof def>(def));
}

export {
  configure,
  configureApi,
  configureTransport,
  ActionError,
  hasErrorString,
  classifyFetchError,
  retryNetwork,
  subscribeToActions,
  subscribeByName,
  pendingCount,
  isPending,
  bindLoadingState,
  registerCleanup,
  debouncedDispatch,
  pollAction,
  pollUntil,
  getActionLog,
  RETRY_STANDARD,
  IDEMPOTENCY_HEADER,
  IDEMPOTENCY_COMMAND_FIELD,
} from "@cplieger/actions";

// Timeout composition lives in @cplieger/fetch (actions v3 no longer re-exports it).
export { withTimeout, API_TIMEOUT_MS } from "@cplieger/fetch";

export type {
  Action,
  ActionDefinition,
  ActionContext,
  ActionErrorLike,
  ActionInstance,
  ActionLifecycleStatus,
  ActionOutcome,
  DispatchOptions,
  DispatchHandle,
  DebouncedDispatch,
  PollOptions,
  RetryConfig,
  RequestSpec,
  Notifier,
  NotifierRetry,
  NotificationSpec,
  RegistryListener,
  TransportCommand,
  TransportSendFn,
  TransportSendResult,
  ApiConfig,
  ApiErrorInfo,
  ApiErrorDecision,
} from "@cplieger/actions";

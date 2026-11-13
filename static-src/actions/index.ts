// Public surface of the actions framework: re-exports @cplieger/reactive/actions for local imports.

import {
  apiAction as libApiAction,
  defineAction as libDefineAction,
  transportAction as libTransportAction,
  type Action,
  type ActionDefinition,
  type ApiActionDefinition,
} from "@cplieger/reactive/actions";
import type { RequestSpec } from "@cplieger/reactive/actions";
import type { EndpointSpec } from "../api-client.js";
import { captureSubjectNames } from "./subject.js";

/** An apiAction request from a generated endpoint descriptor's request (wire/client.gen.ts). */
export function fromEndpoint(spec: EndpointSpec): RequestSpec {
  const headers = spec.headers === undefined ? {} : { headers: spec.headers };
  if (spec.method === "GET") {
    return { method: "GET", path: spec.path, ...headers };
  }
  const body = spec.body === undefined ? {} : { body: spec.body };
  return { method: spec.method, path: spec.path, ...body, ...headers };
}

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

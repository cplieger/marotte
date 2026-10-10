// A leaf: the action modules sit below the tab strip that names a subject, so the
// notice door is injected (configureSubjectNotice).

import type { ActionErrorLike, NotifierRetry } from "@cplieger/actions";

type SubjectNotice = (
  subject: string,
  message: string,
  level: "success" | "error",
  retry?: NotifierRetry,
  name?: string,
) => void;

interface StagedSubject {
  readonly subject: string;
  readonly name: string;
}

const NONE: StagedSubject = { subject: "", name: "" };

let staged = NONE;
let sink: SubjectNotice = () => undefined;
let nameOf: (subject: string) => string = () => "";

const subjectOfSpec = new WeakMap<object, (args: never) => string>();
const begun = new Map<unknown, string>();

function stage(subject: string, args: unknown): void {
  staged = { subject, name: begun.get(args) ?? "" };
}

export function errorAbout<A>(
  subjectOf: (args: A) => string,
  prefix: string | ((args: A, err: ActionErrorLike) => string),
): (args: A, err: ActionErrorLike) => string {
  const spec = (args: A, err: ActionErrorLike): string => {
    const message = typeof prefix === "string" ? `${prefix}: ${err.message}` : prefix(args, err);
    stage(subjectOf(args), args);
    return message;
  };
  subjectOfSpec.set(spec, subjectOf);
  return spec;
}

export function successAbout<A>(
  subjectOf: (args: A) => string,
  message: string,
): (args: A) => string {
  const spec = (args: A): string => {
    stage(subjectOf(args), args);
    return message;
  };
  subjectOfSpec.set(spec, subjectOf);
  return spec;
}

/** The library calls a spec and then its notifier in one synchronous step, so the
 *  notifier takes its own spec's subject; the read clears it. */
export function takeSubject(): StagedSubject {
  const subject = staged;
  staged = NONE;
  return subject;
}

interface SubjectHooks<A> {
  readonly error?: unknown;
  readonly success?: unknown;
  readonly optimistic?: (args: A) => unknown;
  readonly onSettled?: (args: A) => void;
}

function specSubject(spec: unknown): ((args: never) => string) | undefined {
  return typeof spec === "function" ? subjectOfSpec.get(spec) : undefined;
}

/** `optimistic` is the library's one synchronous hook before run(), so the name is
 *  captured there; `onSettled` runs after the notifier and releases it. */
export function captureSubjectNames<A, D extends SubjectHooks<A>>(def: D): D {
  const found = specSubject(def.error) ?? specSubject(def.success);
  if (found === undefined) {
    return def;
  }
  // The resolver was built beside this def's own spec, so its parameter is the def's A.
  const subjectOf = found as (args: A) => string;
  const { optimistic, onSettled } = def;
  return {
    ...def,
    optimistic: (args: A) => {
      begun.set(args, nameOf(subjectOf(args)));
      return optimistic?.(args);
    },
    onSettled: (args: A) => {
      begun.delete(args);
      onSettled?.(args);
    },
  };
}

export function configureSubjectNotice(
  fn: SubjectNotice,
  resolveName: (subject: string) => string,
): void {
  sink = fn;
  nameOf = resolveName;
}

export function noticeAbout(subject: string, message: string, level: "success" | "error"): void {
  sink(subject, message, level, undefined, nameOf(subject));
}

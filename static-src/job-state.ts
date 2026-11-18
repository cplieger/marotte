// The client's view of kiro-cli's background jobs: each chat's running jobs (job_update and GET /api/jobs) and the
// ends its log holds (job_ended entries). State only, so the card, the row, the strip and the chat list read one
// source without importing each other.

import { signal, SignalMap, type Signal } from "@cplieger/reactive";
import { join } from "@cplieger/keyenc";
import type {
  BackgroundJobPayload,
  BackgroundJobState,
  EntryJobEnded,
  ToolCall,
} from "./wire/types.gen.js";

/** How a job's outcome reads, sentence-cased; a row inside a sentence lowercases it (as the export does). */
export const JOB_OUTCOME_WORDS: Readonly<Record<BackgroundJobState, string>> = {
  running: "Running",
  completed: "Completed",
  failed: "Failed",
  cancelled: "Cancelled",
  timed_out: "Timed out",
  outcome_unknown: "Outcome unknown",
};

/** Every chat's running jobs, oldest first; a chat with none has no key. */
export const runningJobs: Signal<ReadonlyMap<string, readonly BackgroundJobPayload[]>> = signal(new Map());

/** Counts job_updates, so a list read can tell which chats moved while it was in flight. */
let updateSeq = 0;
const updatedAt = new Map<string, number>();

/** Apply one job_update: a running job joins or replaces its row, any other state removes it. */
export function applyJobUpdate(chatID: string, job: BackgroundJobPayload): void {
  updatedAt.set(chatID, ++updateSeq);
  const rows = (runningJobs.peek().get(chatID) ?? []).filter((j) => j.id !== job.id);
  if (job.status === "running") {
    rows.push(job);
    rows.sort((a, b) => (a.started_at ?? "").localeCompare(b.started_at ?? "") || a.id.localeCompare(b.id));
  }
  const next = new Map(runningJobs.peek());
  if (rows.length === 0) {
    next.delete(chatID);
  } else {
    next.set(chatID, rows);
  }
  runningJobs.value = next;
}

/** The mark a list read takes before it is sent (`setRunningJobs`). */
export function jobsReadMark(): number {
  return updateSeq;
}

/** Replace every chat's rows with the server's list (GET /api/jobs, at connect), except a chat a job_update moved
 *  after `mark`: its rows are newer than the list. */
export function setRunningJobs(jobs: readonly BackgroundJobPayload[], mark: number): void {
  const next = new Map<string, readonly BackgroundJobPayload[]>();
  for (const j of jobs) {
    const chatID = j.chat_id ?? "";
    if (chatID === "") {
      continue;
    }
    next.set(chatID, [...(next.get(chatID) ?? []), j]);
  }
  for (const [chatID, at] of updatedAt) {
    if (at <= mark) {
      continue;
    }
    next.delete(chatID);
    const held = runningJobs.peek().get(chatID);
    if (held !== undefined) {
      next.set(chatID, held);
    }
  }
  runningJobs.value = next;
}

/** Whether the chat has a running background job; tracked. */
export function chatRunsJob(chatID: string): boolean {
  return (runningJobs.value.get(chatID)?.length ?? 0) > 0;
}

const jobEnds = new SignalMap<EntryJobEnded | undefined>();

/** Record a job_ended entry the chat's log holds. The first one per job stands (the server files one). */
export function noteJobEnded(chatID: string, end: EntryJobEnded): void {
  const sig = jobEnds.ensure(join(chatID, end.job_id), undefined);
  if (sig.peek() === undefined) {
    sig.value = end;
  }
}

/** The end the chat's log holds for a job, or undefined; untracked. */
export function peekJobEnd(chatID: string, jobID: string): EntryJobEnded | undefined {
  return jobEnds.get(join(chatID, jobID))?.peek();
}

/** The end the chat's log holds for a job, or undefined; tracked. */
export function jobEndFor(chatID: string, jobID: string): EntryJobEnded | undefined {
  return jobEnds.ensure(join(chatID, jobID), undefined).value;
}

/** The call as its card paints it once the log holds its job's end: the end on the job mark, and the job's result
 *  as the output when the end carried one. The call itself when it is no job or its end is unknown. */
export function withJobEnd(tc: ToolCall, ended: EntryJobEnded | undefined): ToolCall {
  if (tc.job === undefined || ended === undefined || tc.job.end !== undefined) {
    return tc;
  }
  const output = ended.output ?? "";
  return { ...tc, job: { ...tc.job, end: ended.end }, ...(output !== "" && { output }) };
}

/** The status a job's card paints: running until it ends, then its outcome in the card's tones. */
export function jobPaintStatus(state: BackgroundJobState | undefined): ToolCall["status"] {
  switch (state) {
    case undefined:
    case "running":
      return "in_progress";
    case "completed":
      return "completed";
    case "failed":
    case "timed_out":
      return "failed";
    case "cancelled":
    case "outcome_unknown":
      return "aborted";
  }
}

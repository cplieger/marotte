// The client's view of kiro-cli's background jobs: each chat's running jobs (job_update and GET /api/jobs) and the
// ends its log holds (job_ended entries). State only, so the card, the row, the jobs tracker and the chat list read one
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
  detached: "Left running",
};

/** Every chat's running jobs, oldest first; a chat with none has no key. */
export const runningJobs: Signal<ReadonlyMap<string, readonly BackgroundJobPayload[]>> = signal(
  new Map(),
);

/** Counts job_updates; a list read's mark is the count when it was sent. */
let updateSeq = 0;
/** The marks of the list reads in flight. */
const openMarks: number[] = [];
/** The job_updates newer than the oldest open mark, oldest first, replayed onto the list that read missed them. */
let missed: { seq: number; chatID: string; job: BackgroundJobPayload }[] = [];

/** A running job joins or replaces its row in `jobs`, any other state removes it. */
function placeJob(
  jobs: Map<string, readonly BackgroundJobPayload[]>,
  chatID: string,
  job: BackgroundJobPayload,
): void {
  const rows = (jobs.get(chatID) ?? []).filter((j) => j.id !== job.id);
  if (job.status === "running") {
    rows.push(job);
    rows.sort(
      (a, b) => (a.started_at ?? "").localeCompare(b.started_at ?? "") || a.id.localeCompare(b.id),
    );
  }
  if (rows.length === 0) {
    jobs.delete(chatID);
  } else {
    jobs.set(chatID, rows);
  }
}

/** Apply one job_update. */
export function applyJobUpdate(chatID: string, job: BackgroundJobPayload): void {
  updateSeq++;
  if (openMarks.length > 0) {
    missed.push({ seq: updateSeq, chatID, job });
  }
  const next = new Map(runningJobs.peek());
  placeJob(next, chatID, job);
  runningJobs.value = next;
}

/** Start a list read (GET /api/jobs): call it before the request is sent, and call what it returns once, with the
 *  server's list or null when the read failed. The list replaces every chat's rows, and the job_updates that arrived
 *  while it was in flight are replayed onto it. */
export function beginJobsRead(): (jobs: readonly BackgroundJobPayload[] | null) => void {
  const mark = updateSeq;
  openMarks.push(mark);
  return (jobs) => {
    openMarks.splice(openMarks.indexOf(mark), 1);
    if (jobs !== null) {
      const next = new Map<string, readonly BackgroundJobPayload[]>();
      for (const j of jobs) {
        const chatID = j.chat_id ?? "";
        if (chatID !== "") {
          next.set(chatID, [...(next.get(chatID) ?? []), j]);
        }
      }
      for (const u of missed) {
        if (u.seq > mark) {
          placeJob(next, u.chatID, u.job);
        }
      }
      runningJobs.value = next;
    }
    const oldest = Math.min(...openMarks);
    missed = missed.filter((u) => u.seq > oldest);
  };
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
    case "detached":
      return "aborted";
  }
}

// The jobs tracker: the active chat's running background jobs, one compact row each above the message box under the
// run bar's fold, with Stop and Show output. Hidden while the chat runs none. Show output opens a modal with the job's
// last lines.

import { effect, el } from "@cplieger/reactive";

import { apiAction } from "./actions/index.js";
import { apiGetTyped, apiGetTypedOrError } from "./api-client.js";
import { BUS_RECONCILE, onBus, onSSE } from "./bus.js";
import { jobsOpen, setJobsOpen } from "./device-view.js";
import { byId } from "./dom.js";
import { applyJobUpdate, jobsReadMark, runningJobs, setRunningJobs } from "./job-state.js";
import { reconcile } from "./reconcile.js";
import { watchActiveId } from "./store.js";
import { formatElapsed } from "./strings.js";
import { trackerFold } from "./tracker-fold.js";
import { decodeBackgroundJobOutput, decodeBackgroundJobsView } from "./wire/decoders.gen.js";
import type { BackgroundJobPayload } from "./wire/types.gen.js";

/** The lines Show output reads, the most kiro-cli serves. */
const OUTPUT_LINES = 1000;

/** How often the elapsed times repaint; the text changes, nothing moves. */
const ELAPSED_TICK_MS = 5_000;

// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for an action with no result
const stopJob = apiAction<{ chatID: string; jobID: string }, void>({
  name: "jobs.stop",
  request: ({ chatID, jobID }) => ({ method: "POST", path: jobPath(chatID, jobID, "cancel") }),
  error: false,
});

/** A row's transient state: Stop sent and not yet answered by the job's end, or a message to show. */
interface RowState {
  stopping: boolean;
  message: string;
}

/** One running job's elapsed time, from its start to now. */
function jobElapsed(job: BackgroundJobPayload, now: number): string {
  const started = Date.parse(job.started_at ?? "");
  return Number.isFinite(started) ? formatElapsed(Math.max(0, now - started)) : "";
}

function jobPath(chatID: string, jobID: string, verb: string): string {
  return `/api/chats/${encodeURIComponent(chatID)}/jobs/${encodeURIComponent(jobID)}/${verb}`;
}

/** Mounts the tracker on `#jobs-tracker`, its header and its list; call once at boot. */
export function mountJobsTracker(): void {
  const tracker = byId<HTMLElement>("jobs-tracker");
  const list = byId<HTMLUListElement>("jobs-tracker-list");
  const states = new Map<string, RowState>();
  const output = createOutputDialog();

  const stateOf = (jobID: string): RowState => {
    let st = states.get(jobID);
    if (st === undefined) {
      st = { stopping: false, message: "" };
      states.set(jobID, st);
    }
    return st;
  };

  let shownChat = "";
  let shownJobs: readonly BackgroundJobPayload[] = [];
  const fold = trackerFold({
    list,
    header: byId<HTMLElement>("jobs-tracker-fold"),
    glyph: el("span", {
      className: "tab-status-dot",
      "data-status": "background",
      "aria-hidden": "true",
    }),
    count: () => shownJobs.length,
    stored: jobsOpen,
    store: setJobsOpen,
    repaint: () => {
      paint();
    },
  });
  const paint = (): void => {
    const now = Date.now();
    const count = shownJobs.length;
    tracker.hidden = count === 0;
    const before = list.childElementCount;
    reconcile(list, shownJobs, {
      key: (job) => job.id,
      mount: (job) => {
        const li = buildRow(job);
        paintRow(li, job, stateOf(job.id), now);
        return li;
      },
      update: (li, job) => {
        paintRow(li, job, stateOf(job.id), now);
      },
    });
    fold.paint(
      count,
      `${String(count)} background jobs running`,
      list.childElementCount !== before,
    );
  };

  const buildRow = (job: BackgroundJobPayload): HTMLElement => {
    const desc = el("span", { className: "jobs-tracker-desc" });
    const elapsed = el("span", { className: "jobs-tracker-elapsed" });
    const message = el("span", { className: "jobs-tracker-message", role: "status" });
    const stop = el(
      "button",
      { type: "button", className: "btn-small jobs-tracker-stop" },
      "Stop",
    ) as HTMLButtonElement;
    const show = el(
      "button",
      { type: "button", className: "btn-small jobs-tracker-show" },
      "Show output",
    ) as HTMLButtonElement;
    stop.addEventListener("click", () => {
      const st = stateOf(job.id);
      st.stopping = true;
      st.message = "";
      paint();
      void stopJob.dispatch({ chatID: shownChat, jobID: job.id }).outcome.then((o) => {
        if (o.status !== "error") {
          return;
        }
        st.stopping = false;
        st.message = o.error.message;
        paint();
      });
    });
    show.addEventListener("click", () => {
      output.open(shownChat, job);
    });
    return el(
      "li",
      { className: "jobs-tracker-row", "data-job-id": job.id },
      desc,
      elapsed,
      message,
      stop,
      show,
    );
  };

  effect(() => {
    shownChat = watchActiveId();
    shownJobs = runningJobs.value.get(shownChat) ?? [];
    for (const id of states.keys()) {
      if (!shownJobs.some((j) => j.id === id)) {
        states.delete(id);
      }
    }
    paint();
  });
  window.setInterval(() => {
    if (shownJobs.length > 0) {
      paint();
    }
  }, ELAPSED_TICK_MS);

  onSSE("job_update", (chatID, job) => {
    if (chatID !== "") {
      applyJobUpdate(chatID, job);
    }
  });
  const read = (signal?: AbortSignal): void => {
    const mark = jobsReadMark();
    void apiGetTyped("/api/jobs", decodeBackgroundJobsView, signal).then((v) => {
      if (v !== null) {
        setRunningJobs(v.jobs, mark);
      }
    });
  };
  onBus(BUS_RECONCILE, ({ signal }) => {
    read(signal);
  });
  read();
}

function paintRow(li: HTMLElement, job: BackgroundJobPayload, st: RowState, now: number): void {
  const desc = li.querySelector<HTMLElement>(".jobs-tracker-desc");
  const elapsed = li.querySelector<HTMLElement>(".jobs-tracker-elapsed");
  const message = li.querySelector<HTMLElement>(".jobs-tracker-message");
  const stop = li.querySelector<HTMLButtonElement>(".jobs-tracker-stop");
  if (desc === null || elapsed === null || message === null || stop === null) {
    return;
  }
  const what = job.description === "" ? `Job ${job.id}` : job.description;
  desc.textContent = what;
  desc.title = what;
  elapsed.textContent = st.stopping ? "Stopping" : jobElapsed(job, now);
  message.textContent = st.message;
  message.hidden = st.message === "";
  stop.disabled = st.stopping;
  stop.setAttribute("aria-label", `Stop ${what}`);
  li.querySelector(".jobs-tracker-show")?.setAttribute("aria-label", `Show the output of ${what}`);
}

interface OutputDialog {
  open(chatID: string, job: BackgroundJobPayload): void;
}

/** The output modal: the job's last lines as machine text, with Copy and Refresh. Escape or Close returns focus to
 *  the Show output that opened it, by the dialog's own focus restoration (HTML "dialog focusing steps"). */
function createOutputDialog(): OutputDialog {
  const title = el("h3", { className: "job-output-title", id: "job-output-title" });
  const well = el("pre", { className: "job-output-well", tabIndex: 0 });
  const status = el("p", { className: "job-output-status", role: "status" });
  const copy = el(
    "button",
    { type: "button", className: "btn-small" },
    "Copy",
  ) as HTMLButtonElement;
  const refresh = el(
    "button",
    { type: "button", className: "btn-small" },
    "Refresh",
  ) as HTMLButtonElement;
  const close = el(
    "button",
    { type: "button", className: "btn-small" },
    "Close",
  ) as HTMLButtonElement;
  const dialog = el(
    "dialog",
    { className: "job-output-dialog", "aria-labelledby": "job-output-title" },
    el("header", { className: "job-output-header" }, title),
    well,
    status,
    el("footer", { className: "job-output-actions" }, copy, refresh, close),
  ) as HTMLDialogElement;
  document.body.appendChild(dialog);

  let chatID = "";
  let jobID = "";
  const load = (): void => {
    status.textContent = "";
    refresh.disabled = true;
    const path = `${jobPath(chatID, jobID, "output")}?lines=${String(OUTPUT_LINES)}`;
    void apiGetTypedOrError(path, decodeBackgroundJobOutput).then((r) => {
      refresh.disabled = false;
      if (!r.ok || r.data === null) {
        status.textContent = r.error === "" ? "kiro-cli did not read the job's output." : r.error;
        return;
      }
      well.textContent = r.data.lines.join("\n");
      status.textContent =
        r.data.gap === true ? "Some lines could not be read whole and were left out." : "";
      well.scrollTop = well.scrollHeight;
    });
  };
  copy.addEventListener("click", () => {
    void navigator.clipboard.writeText(well.textContent).then(
      () => {
        status.textContent = "Copied.";
      },
      () => {
        status.textContent = "The browser did not allow copying.";
      },
    );
  });
  refresh.addEventListener("click", load);
  close.addEventListener("click", () => {
    dialog.close();
  });
  return {
    open(chat, job) {
      chatID = chat;
      jobID = job.id;
      title.textContent = `Output of ${job.description === "" ? `job ${job.id}` : job.description}`;
      well.textContent = "";
      dialog.showModal();
      load();
    },
  };
}

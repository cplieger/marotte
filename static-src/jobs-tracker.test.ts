// The jobs tracker: the active chat's running jobs as compact rows under the run tracker's fold, Stop and its
// refusal, and the output modal.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { dispatch } from "./bus.js";
import { applyJobUpdate, runningJobs } from "./job-state.js";
import { mountJobsTracker } from "./jobs-tracker.js";
import { LS_UI_STATE_KEY } from "./ls-keys.js";
import { setActive } from "./store.js";
import type { BackgroundJobPayload } from "./wire/types.gen.js";

function job(id: string, over: Partial<BackgroundJobPayload> = {}): BackgroundJobPayload {
  return {
    id,
    kind: "shell",
    description: `sleep ${id}`,
    status: "running",
    started_at: new Date().toISOString(),
    ...over,
  };
}

describe("mountJobsTracker", () => {
  const requests: { method: string; path: string; search: string }[] = [];
  let answer: (path: string) => Response;
  let style: HTMLStyleElement | null = null;

  beforeEach(() => {
    document.body.innerHTML =
      `<div id="jobs-tracker" class="tracker-host" hidden>` +
      `<div id="jobs-tracker-fold" class="tracker-fold" hidden></div>` +
      `<ul id="jobs-tracker-list" class="jobs-tracker-list"></ul></div>`;
    requests.length = 0;
    answer = () => new Response(null, { status: 202 });
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = new URL(input instanceof Request ? input.url : String(input), location.href);
        requests.push({ method: init?.method ?? "GET", path: url.pathname, search: url.search });
        if (url.pathname === "/api/jobs") {
          return Promise.resolve(Response.json({ jobs: [] }));
        }
        return Promise.resolve(answer(url.pathname));
      }),
    );
    localStorage.removeItem(LS_UI_STATE_KEY);
    runningJobs.value = new Map();
    setActive("c1");
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    document.body.innerHTML = "";
    style?.remove();
    style = null;
    localStorage.removeItem(LS_UI_STATE_KEY);
    runningJobs.value = new Map();
  });

  const tracker = (): HTMLElement => document.getElementById("jobs-tracker") as HTMLElement;
  const fold = (): HTMLElement => document.getElementById("jobs-tracker-fold") as HTMLElement;
  const list = (): HTMLElement => document.getElementById("jobs-tracker-list") as HTMLElement;
  const toggle = (): HTMLElement | null => fold().querySelector<HTMLElement>(".tracker-toggle");
  const rows = (): HTMLElement[] => [
    ...document.querySelectorAll<HTMLElement>(".jobs-tracker-row"),
  ];

  it("is hidden while the active chat runs no job, and lists another chat's jobs nowhere", () => {
    mountJobsTracker();
    expect(tracker().hidden).toBe(true);

    applyJobUpdate("c2", job("9"));
    expect(tracker().hidden).toBe(true);

    dispatch({ type: "job_update", chat_id: "c1", payload: job("7") });
    expect(tracker().hidden).toBe(false);
    expect(rows().map((r) => r.querySelector(".jobs-tracker-desc")?.textContent)).toEqual([
      "sleep 7",
    ]);

    dispatch({ type: "job_update", chat_id: "c1", payload: job("7", { status: "completed" }) });
    expect(tracker().hidden).toBe(true);
  });

  it("lists one job with no header, and two open under a header that counts them", () => {
    mountJobsTracker();
    applyJobUpdate("c1", job("7"));
    expect(fold().hidden).toBe(true);
    expect(list().hasAttribute("data-folded")).toBe(false);

    applyJobUpdate("c1", job("8"));
    expect(fold().hidden).toBe(false);
    expect(toggle()?.textContent).toBe("2 background jobs running");
    expect(toggle()?.getAttribute("aria-expanded")).toBe("true");
    expect(list().hasAttribute("data-folded")).toBe(false);
    expect(rows()).toHaveLength(2);
  });

  it("folds three jobs when this screen never chose, and opens them from the header", () => {
    style = mountAppCSS();
    mountJobsTracker();
    for (const id of ["7", "8", "9"]) {
      applyJobUpdate("c1", job(id));
    }
    expect(toggle()?.textContent).toBe("3 background jobs running");
    expect(toggle()?.getAttribute("aria-expanded")).toBe("false");
    expect(list().hasAttribute("data-folded")).toBe(true);
    expect(list().getBoundingClientRect().height).toBe(0);

    toggle()?.click();

    expect(toggle()?.getAttribute("aria-expanded")).toBe("true");
    expect(list().hasAttribute("data-folded")).toBe(false);
    expect(rows().every((r) => r.checkVisibility())).toBe(true);
    expect(JSON.parse(localStorage.getItem(LS_UI_STATE_KEY) ?? "{}")).toEqual({
      jobs_open: true,
    });
  });

  it("keeps the reader's fold of the jobs apart from the run tracker's", () => {
    localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify({ run_bar_open: false }));
    mountJobsTracker();
    applyJobUpdate("c1", job("7"));
    applyJobUpdate("c1", job("8"));
    expect(toggle()?.getAttribute("aria-expanded")).toBe("true");

    localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify({ jobs_open: false }));
    applyJobUpdate("c1", job("9"));
    applyJobUpdate("c1", job("9", { status: "completed" }));
    expect(toggle()?.getAttribute("aria-expanded")).toBe("false");
    expect(list().hasAttribute("data-folded")).toBe(true);
  });

  it("reads Stopping with Stop disabled once Stop is sent, and shows a refusal in the row", async () => {
    answer = () => Response.json({ error: "the session has no job '7'" }, { status: 409 });
    mountJobsTracker();
    applyJobUpdate("c1", job("7"));
    const stop = rows()[0]?.querySelector<HTMLButtonElement>(".jobs-tracker-stop");

    stop?.click();

    expect(rows()[0]?.querySelector(".jobs-tracker-elapsed")?.textContent).toBe("Stopping");
    expect(stop?.disabled).toBe(true);
    expect(requests).toContainEqual({
      method: "POST",
      path: "/api/chats/c1/jobs/7/cancel",
      search: "",
    });
    await vi.waitFor(() => {
      expect(rows()[0]?.querySelector(".jobs-tracker-message")?.textContent).toBe(
        "the session has no job '7'",
      );
    });
    expect(stop?.disabled).toBe(false);
  });

  it("opens the output modal with the job's last lines, and Close returns focus to Show output", async () => {
    answer = () => Response.json({ status: "running", lines: ["one", "two"] });
    mountJobsTracker();
    applyJobUpdate("c1", job("7"));
    const show = rows()[0]?.querySelector<HTMLButtonElement>(".jobs-tracker-show");
    show?.focus();

    show?.click();

    const dialog = document.querySelector<HTMLDialogElement>(".job-output-dialog");
    expect(dialog?.open).toBe(true);
    await vi.waitFor(() => {
      expect(dialog?.querySelector(".job-output-well")?.textContent).toBe("one\ntwo");
    });
    expect(requests).toContainEqual({
      method: "GET",
      path: "/api/chats/c1/jobs/7/output",
      search: "?lines=1000",
    });
    [...(dialog?.querySelectorAll("button") ?? [])].find((b) => b.textContent === "Close")?.click();
    await vi.waitFor(() => {
      expect(document.activeElement).toBe(show);
    });
  });

  it("keeps the open job's lines when another job's read lands after them", async () => {
    let releaseA: () => void = () => undefined;
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = new URL(input instanceof Request ? input.url : String(input), location.href);
        if (url.pathname === "/api/chats/c1/jobs/7/output") {
          // Answers whatever the read's signal says, as a response already on its way would.
          return new Promise<Response>((resolve) => {
            releaseA = () => {
              resolve(Response.json({ status: "running", lines: ["a"] }));
            };
          });
        }
        if (url.pathname === "/api/chats/c1/jobs/8/output") {
          return Promise.resolve(Response.json({ status: "running", lines: ["b"] }));
        }
        return Promise.resolve(Response.json({ jobs: [] }));
      }),
    );
    mountJobsTracker();
    applyJobUpdate("c1", job("7"));
    applyJobUpdate("c1", job("8"));
    const show = (id: string): HTMLButtonElement | null | undefined =>
      rows()
        .find((r) => r.textContent.includes(`sleep ${id}`))
        ?.querySelector<HTMLButtonElement>(".jobs-tracker-show");
    const dialog = (): HTMLDialogElement | null =>
      document.querySelector<HTMLDialogElement>(".job-output-dialog");
    const well = (): string | null | undefined =>
      dialog()?.querySelector(".job-output-well")?.textContent;

    show("7")?.click();
    [...(dialog()?.querySelectorAll("button") ?? [])]
      .find((b) => b.textContent === "Close")
      ?.click();
    show("8")?.click();
    await vi.waitFor(() => {
      expect(well()).toBe("b");
    });
    releaseA();
    await new Promise((r) => setTimeout(r, 20));

    expect(well()).toBe("b");
    expect(dialog()?.querySelector("header")?.textContent).toBe("Output of sleep 8");
  });

  it("shows a failed read in the modal", async () => {
    answer = () =>
      Response.json({ error: "kiro-cli did not read the job's output." }, { status: 502 });
    mountJobsTracker();
    applyJobUpdate("c1", job("7"));

    rows()[0]?.querySelector<HTMLButtonElement>(".jobs-tracker-show")?.click();

    await vi.waitFor(() => {
      expect(document.querySelector(".job-output-status")?.textContent).toBe(
        "kiro-cli did not read the job's output.",
      );
    });
  });

  it("keeps a job_update that lands while the list read is in flight", async () => {
    let release: () => void = () => undefined;
    vi.stubGlobal(
      "fetch",
      vi.fn(
        () =>
          new Promise<Response>((resolve) => {
            release = () => {
              resolve(Response.json({ jobs: [{ ...job("5"), chat_id: "c2" }] }));
            };
          }),
      ),
    );
    mountJobsTracker();
    applyJobUpdate("c1", job("7"));

    release();

    await vi.waitFor(() => {
      expect(runningJobs.value.get("c2")?.map((j) => j.id)).toEqual(["5"]);
    });
    expect(runningJobs.value.get("c1")?.map((j) => j.id)).toEqual(["7"]);
  });

  /** Holds GET /api/jobs until the returned function answers it with `jobs`. */
  const holdJobsRead = (): ((jobs: BackgroundJobPayload[]) => void) => {
    let answerRead: (jobs: BackgroundJobPayload[]) => void = () => undefined;
    vi.stubGlobal(
      "fetch",
      vi.fn(
        () =>
          new Promise<Response>((resolve) => {
            answerRead = (jobs) => {
              resolve(Response.json({ jobs }));
            };
          }),
      ),
    );
    return (jobs) => {
      answerRead(jobs);
    };
  };

  it("keeps the list's other jobs of a chat a job_update moved while the read was in flight", async () => {
    const answerRead = holdJobsRead();
    mountJobsTracker();
    applyJobUpdate("c1", job("7", { description: "newer" }));

    answerRead([
      { ...job("7"), chat_id: "c1" },
      { ...job("8"), chat_id: "c1" },
    ]);

    await vi.waitFor(() => {
      expect(runningJobs.value.get("c1")?.map((j) => j.id)).toEqual(["7", "8"]);
    });
    expect(runningJobs.value.get("c1")?.[0]?.description).toBe("newer");
  });

  it("drops a held job the list no longer runs when no job_update moved it during the read", async () => {
    applyJobUpdate("c1", job("7"));
    const answerRead = holdJobsRead();
    mountJobsTracker();

    answerRead([{ ...job("8"), chat_id: "c1" }]);

    await vi.waitFor(() => {
      expect(runningJobs.value.get("c1")?.map((j) => j.id)).toEqual(["8"]);
    });
  });

  it("removes a job whose end arrived while the read was in flight", async () => {
    const answerRead = holdJobsRead();
    mountJobsTracker();
    applyJobUpdate("c1", job("7", { status: "completed" }));

    answerRead([
      { ...job("7"), chat_id: "c1" },
      { ...job("8"), chat_id: "c1" },
    ]);

    await vi.waitFor(() => {
      expect(runningJobs.value.get("c1")?.map((j) => j.id)).toEqual(["8"]);
    });
  });
});

// The jobs strip: the active chat's running jobs as compact rows, Stop and its refusal, and the output modal.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { dispatch } from "./bus.js";
import { applyJobUpdate, runningJobs } from "./job-state.js";
import { mountJobsStrip } from "./jobs-strip.js";
import { setActive } from "./store.js";
import type { BackgroundJobPayload } from "./wire/types.gen.js";

function job(id: string, over: Partial<BackgroundJobPayload> = {}): BackgroundJobPayload {
  return { id, kind: "shell", description: `sleep ${id}`, status: "running", started_at: new Date().toISOString(), ...over };
}

describe("mountJobsStrip", () => {
  const requests: { method: string; path: string; search: string }[] = [];
  let answer: (path: string) => Response;

  beforeEach(() => {
    document.body.innerHTML = `<div id="jobs-strip" hidden><ul id="jobs-strip-list"></ul></div>`;
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
    runningJobs.value = new Map();
    setActive("c1");
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    document.body.innerHTML = "";
    runningJobs.value = new Map();
  });

  const strip = (): HTMLElement => document.getElementById("jobs-strip") as HTMLElement;
  const rows = (): HTMLElement[] => [...document.querySelectorAll<HTMLElement>(".jobs-strip-row")];

  it("is hidden while the active chat runs no job, and lists another chat's jobs nowhere", () => {
    mountJobsStrip();
    expect(strip().hidden).toBe(true);

    applyJobUpdate("c2", job("9"));
    expect(strip().hidden).toBe(true);

    dispatch({ type: "job_update", chat_id: "c1", payload: job("7") });
    expect(strip().hidden).toBe(false);
    expect(rows().map((r) => r.querySelector(".jobs-strip-desc")?.textContent)).toEqual(["sleep 7"]);

    dispatch({ type: "job_update", chat_id: "c1", payload: job("7", { status: "completed" }) });
    expect(strip().hidden).toBe(true);
  });

  it("reads Stopping with Stop disabled once Stop is sent, and shows a refusal in the row", async () => {
    answer = () => Response.json({ error: "the session has no job '7'" }, { status: 409 });
    mountJobsStrip();
    applyJobUpdate("c1", job("7"));
    const stop = rows()[0]?.querySelector<HTMLButtonElement>(".jobs-strip-stop");

    stop?.click();

    expect(rows()[0]?.querySelector(".jobs-strip-elapsed")?.textContent).toBe("Stopping");
    expect(stop?.disabled).toBe(true);
    expect(requests).toContainEqual({ method: "POST", path: "/api/chats/c1/jobs/7/cancel", search: "" });
    await vi.waitFor(() => {
      expect(rows()[0]?.querySelector(".jobs-strip-message")?.textContent).toBe("the session has no job '7'");
    });
    expect(stop?.disabled).toBe(false);
  });

  it("opens the output modal with the job's last lines, and Close returns focus to Show output", async () => {
    answer = () => Response.json({ status: "running", lines: ["one", "two"] });
    mountJobsStrip();
    applyJobUpdate("c1", job("7"));
    const show = rows()[0]?.querySelector<HTMLButtonElement>(".jobs-strip-show");
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

  it("shows a failed read in the modal", async () => {
    answer = () => Response.json({ error: "kiro-cli did not read the job's output." }, { status: 502 });
    mountJobsStrip();
    applyJobUpdate("c1", job("7"));

    rows()[0]?.querySelector<HTMLButtonElement>(".jobs-strip-show")?.click();

    await vi.waitFor(() => {
      expect(document.querySelector(".job-output-status")?.textContent).toBe("kiro-cli did not read the job's output.");
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
    mountJobsStrip();
    applyJobUpdate("c1", job("7"));

    release();

    await vi.waitFor(() => {
      expect(runningJobs.value.get("c2")?.map((j) => j.id)).toEqual(["5"]);
    });
    expect(runningJobs.value.get("c1")?.map((j) => j.id)).toEqual(["7"]);
  });
});

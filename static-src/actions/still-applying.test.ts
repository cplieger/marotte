// A change kiro-cli has not answered yet reads as still applying, never as failed: the server's
// 504 still_applying, or the browser's own timeout. The actions run for real over
// `configureApi`'s fetch seam.

import { vi, describe, it, expect, beforeEach } from "vitest";
import type * as RunStore from "../run-store.js";

const m = vi.hoisted(() => ({
  answer: (): Promise<Response> => Promise.resolve(new Response("{}", { status: 200 })),
  calls: 0,
  errors: [] as string[],
}));

vi.mock("../run-store.js", async (importOriginal) => ({
  ...(await importOriginal<typeof RunStore>()),
  invalidateRun: vi.fn(),
}));

import { configure, configureApi } from "@cplieger/reactive/actions";
import { setHookEnabled } from "./hooks.js";
import { deleteMemory, updateMemory } from "./memory.js";
import {
  addKnowledge,
  cancelKnowledgeIndexing,
  clearKnowledge,
  reindexKnowledge,
  removeKnowledge,
} from "./knowledge.js";
import { launchRun, retryRun } from "./runs.js";
import { configureSubjectNotice } from "./subject.js";

function json(status: number, body: unknown): () => Promise<Response> {
  return () =>
    Promise.resolve(
      new Response(JSON.stringify(body), {
        status,
        headers: { "content-type": "application/json" },
      }),
    );
}

// The body internal/agent/engine_mutation.go writes once clientAnswerBudget passes.
const stillApplying = json(504, {
  error:
    "kiro-cli has not answered yet and may still apply the change; the list updates when it does",
  code: "still_applying",
});
const browserTimeout = (): Promise<Response> =>
  Promise.reject(new DOMException("The operation timed out.", "TimeoutError"));
const refused = json(502, { error: "kiro-cli refused" });

beforeEach(() => {
  m.answer = () => Promise.resolve(new Response("{}", { status: 200 }));
  m.calls = 0;
  m.errors.length = 0;
  configureApi({
    fetchFn: () => {
      m.calls++;
      return m.answer();
    },
  });
  configure({
    success: vi.fn(),
    error: (msg) => {
      m.errors.push(msg);
    },
  });
  configureSubjectNotice(
    (_subject, msg, level) => {
      if (level !== "success") {
        m.errors.push(msg);
      }
    },
    () => "",
  );
});

const cases: { what: string; run: () => Promise<unknown>; still: string; failed: string }[] = [
  {
    what: "a hook toggle",
    run: () => setHookEnabled.dispatch({ id: "lint", enabled: true }),
    still: "Still updating the hook; the list updates when it lands",
    failed: "Could not update hook: kiro-cli refused",
  },
  {
    what: "a memory edit",
    run: () => updateMemory.dispatch({ id: "m1", edit: { title: "t" } }),
    still: "Still saving the memory; the list updates when it lands",
    failed: "Couldn't save the memory: kiro-cli refused",
  },
  {
    what: "a memory delete",
    run: () => deleteMemory.dispatch({ id: "m1" }),
    still: "Still deleting the memory; the list updates when it lands",
    failed: "Couldn't delete the memory: kiro-cli refused",
  },
  {
    what: "a knowledge base removal",
    run: () => removeKnowledge.dispatch({ name: "docs" }),
    still: "Still removing the knowledge base; the list updates when it lands",
    failed: "Could not remove knowledge base: kiro-cli refused",
  },
  {
    what: "a re-index",
    run: () => reindexKnowledge.dispatch({ name: "docs" }),
    still: "Still starting the re-index; the list updates when it lands",
    failed: "Could not re-index knowledge base: kiro-cli refused",
  },
  {
    what: "a stop of indexing",
    run: () => cancelKnowledgeIndexing.dispatch({ name: "docs" }),
    still: "Still stopping the index; the list updates when it lands",
    failed: "Couldn't stop indexing: kiro-cli refused",
  },
  {
    what: "a clear of every knowledge base",
    run: () => clearKnowledge.dispatch(undefined),
    still: "Still clearing the knowledge bases; the list updates when it lands",
    failed: "Couldn't clear knowledge bases: kiro-cli refused",
  },
  {
    what: "a run retry",
    run: () => retryRun.dispatch("wf_1"),
    still: "Still retrying the run; it updates if kiro-cli takes the retry",
    failed: "kiro-cli refused",
  },
  {
    what: "a run launch",
    run: () => launchRun.dispatch({ source: "agent://reviewer", inputs: { prompt: "go" } }),
    still: "Still starting the run; it appears if kiro-cli starts it",
    failed: "kiro-cli refused",
  },
];

describe.each(cases)("$what", ({ run, still, failed }) => {
  it("says it is still applying when the server answers still_applying", async () => {
    m.answer = stillApplying;
    await run();
    expect(m.errors).toEqual([still]);
  });

  it("says it is still applying when the browser gives up waiting", async () => {
    m.answer = browserTimeout;
    await run();
    expect(m.errors).toEqual([still]);
  });

  it("still says it failed for a refusal", async () => {
    m.answer = refused;
    await run();
    expect(m.errors).toEqual([failed]);
  });
});

describe("a knowledge add", () => {
  // A 5xx clears the Idempotency-Key slot (internal/server/idempotency.go), so a retry would add twice.
  it("is not retried while the server says it is still applying", async () => {
    m.answer = stillApplying;
    const out = await addKnowledge.dispatch({ path: "docs" }).outcome;
    expect(out.status).toBe("error");
    expect(m.calls).toBe(1);
  });

  it("is not retried after a refusal", async () => {
    m.answer = json(422, { error: "path does not exist: docs" });
    await addKnowledge.dispatch({ path: "docs" }).outcome;
    expect(m.calls).toBe(1);
  });

  it("is still retried after a transient failure", async () => {
    m.answer = json(503, { error: "busy" });
    await addKnowledge.dispatch({ path: "docs" }).outcome;
    expect(m.calls).toBe(3);
  });
});

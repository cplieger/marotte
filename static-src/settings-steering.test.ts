// The Instructions panel's two writers on one textarea, and the ordering between them. A save
// carries the box as the WHOLE document and an empty one DELETES the file, so the box must not be
// typeable before the read lands; the read is what unlocks it.
import { vi, describe, it, expect, beforeEach, afterEach } from "vitest";
import { userEvent } from "vitest/browser";
import type { ActionInstance } from "./actions/index.js";

const H = vi.hoisted(() => ({
  get: vi.fn(),
  save: vi.fn(),
  flush: vi.fn(),
  pending: vi.fn(() => false),
  showError: vi.fn(),
  showSaved: vi.fn(),
  toast: vi.fn(),
  /** The lifecycle listener `initSteeringEditor` registered. */
  onSave: null as ((inst: ActionInstance) => void) | null,
}));

vi.mock("./api-client.js", () => ({ apiGetWithHeaders: H.get }));
vi.mock("./toast.js", () => ({ showToast: H.toast }));
vi.mock("./save-indicator.js", () => ({
  showSaving: vi.fn(),
  showSaved: H.showSaved,
  showError: H.showError,
  STEERING_SAVE_KEY: "steering",
}));
vi.mock("./actions/settings.js", () => ({ saveSteering: {} }));
vi.mock("./actions/index.js", () => ({
  registerCleanup: vi.fn(),
  subscribeByName: vi.fn((_name: string, fn: (inst: ActionInstance) => void) => {
    H.onSave = fn;
    return () => undefined;
  }),
  debouncedDispatch: vi.fn(() =>
    Object.assign((...a: unknown[]) => H.save(...a), {
      isPending: H.pending,
      flush: H.flush,
    }),
  ),
}));

const { initSteeringEditor, loadSteeringDoc, _resetSteeringForTest } =
  await import("./settings-steering.js");

/** The textarea `$` resolves, mounted at the id index.html declares. */
function mount(): HTMLTextAreaElement {
  const ta = document.createElement("textarea");
  ta.id = "steering-input";
  document.body.appendChild(ta);
  return ta;
}

/** What the GET answers: a body plus whatever validator the server sent. */
function answers(content: string, tag = 'W/"12-34"'): void {
  H.get.mockResolvedValue({ data: { content }, headers: new Headers({ ETag: tag }) });
}

/** A read left open, with its resolver. */
function openRead(): (d: { content: string } | null) => void {
  let land = (_: { content: string } | null): void => undefined;
  H.get.mockReturnValue(
    new Promise<{ data: { content: string } | null; headers: Headers }>((resolve) => {
      land = (d) => {
        resolve({ data: d, headers: new Headers({ ETag: 'W/"12-34"' }) });
      };
    }),
  );
  return land;
}

/** Report the outcome of one dispatch. `args` defaults to the last one the module made; a case
 *  that needs an EARLIER dispatch's outcome states it. `result` is what the action decoded off
 *  the 200 — the validator the write produced. */
function settleSave(
  status: "success" | "error",
  opts: { httpStatus?: number; args?: unknown; result?: unknown } = {},
): void {
  const args = opts.args ?? H.save.mock.calls.at(-1)?.[0] ?? {};
  H.onSave?.({
    id: "1",
    name: "settings.save_steering",
    status,
    args,
    dispatchedAt: 0,
    startedAt: 0,
    ...(opts.result !== undefined && { result: opts.result }),
    ...(opts.httpStatus !== undefined && {
      error: { message: "refused", status: opts.httpStatus },
    }),
  } as ActionInstance);
}

/** Two microtask hops: the promise's own, then the `.then` body's. */
async function settle(): Promise<void> {
  await Promise.resolve();
  await Promise.resolve();
}

describe("the steering document's read, and the box it unlocks", () => {
  let ta: HTMLTextAreaElement;

  beforeEach(() => {
    _resetSteeringForTest();
    vi.clearAllMocks();
    H.onSave = null;
    H.pending.mockReturnValue(false);
    ta = mount();
    initSteeringEditor();
  });

  afterEach(() => {
    ta.remove();
  });

  it("locks the box at init and opens it when the document lands", async () => {
    expect(ta.readOnly, "the box is not typeable before the read").toBe(true);
    answers("# on the server");
    loadSteeringDoc();
    await vi.waitFor(() => {
      expect(ta.value).toBe("# on the server");
    });
    expect(ta.readOnly).toBe(false);
  });

  it("takes no keystroke while the read is in flight", async () => {
    // A real keystroke through the browser, because `readOnly` is a claim about what the ENGINE
    // does with one: assigning `.value` in a test would sail past the very thing being pinned.
    const land = openRead();
    loadSteeringDoc();

    ta.focus();
    await userEvent.keyboard("half a sentence");

    expect(ta.value, "the keystrokes did not reach a locked box").toBe("");
    expect(H.save, "and nothing was queued to overwrite the document").not.toHaveBeenCalled();

    land({ content: "# on the server" });
    await settle();
    expect(ta.value).toBe("# on the server");
    expect(ta.readOnly).toBe(false);
  });

  it("saves what the reader types, carrying the validator the read answered", async () => {
    answers("# on the server", 'W/"7-9"');
    loadSteeringDoc();
    await vi.waitFor(() => {
      expect(ta.readOnly).toBe(false);
    });

    ta.focus();
    await userEvent.keyboard("!");

    expect(ta.value).toBe("# on the server!");
    expect(H.save).toHaveBeenLastCalledWith({ content: "# on the server!", etag: 'W/"7-9"' });
  });

  it("sends no validator when the server answered none", async () => {
    // The honest answer for a server with no ETag, and the reason the action omits the header
    // rather than inventing one: `If-Match` on such a server is a permanent 428 against a save that
    // would otherwise work.
    H.get.mockResolvedValue({ data: { content: "x" }, headers: new Headers() });
    loadSteeringDoc();
    await vi.waitFor(() => {
      expect(ta.readOnly).toBe(false);
    });

    ta.focus();
    await userEvent.keyboard("!");

    expect(H.save).toHaveBeenLastCalledWith({ content: "x!", etag: "" });
  });

  it("keeps the box locked when the read fails, and retries on focus", async () => {
    // The read collapses every failure to a null body. An empty box is not an empty document, so
    // opening it here would let the first keystroke delete the file.
    H.get.mockResolvedValueOnce({ data: null, headers: null });
    loadSteeringDoc();
    await settle();

    expect(ta.readOnly).toBe(true);
    expect(H.showError).toHaveBeenCalledWith("steering");

    answers("# on the server");
    ta.focus();
    await vi.waitFor(() => {
      expect(ta.value).toBe("# on the server");
    });
    expect(ta.readOnly).toBe(false);
  });

  it("issues one read while one is in flight", () => {
    openRead();
    loadSteeringDoc();
    loadSteeringDoc();
    ta.focus();
    expect(H.get).toHaveBeenCalledTimes(1);
  });

  it("re-seeds a clean box on the next activation", async () => {
    answers("# first");
    loadSteeringDoc();
    await vi.waitFor(() => {
      expect(ta.value).toBe("# first");
    });

    answers("# an agent wrote this");
    loadSteeringDoc();
    await vi.waitFor(() => {
      expect(ta.value).toBe("# an agent wrote this");
    });
  });

  it("leaves unsaved text alone, and keeps the validator it was typed against", async () => {
    answers("# first", 'W/"1-1"');
    loadSteeringDoc();
    await vi.waitFor(() => {
      expect(ta.readOnly).toBe(false);
    });
    ta.focus();
    await userEvent.keyboard("!");

    // The file moved while the reader was typing. Adopting either half would decide the conflict
    // silently; the stale validator is what makes the save take the 409 instead.
    answers("# somebody else", 'W/"2-2"');
    loadSteeringDoc();
    await settle();

    expect(ta.value).toBe("# first!");
    await userEvent.keyboard("?");
    expect(H.save).toHaveBeenLastCalledWith({ content: "# first!?", etag: 'W/"1-1"' });
  });

  it("adopts the validator the save answered with, and reads nothing", async () => {
    answers("# first", 'W/"1-1"');
    loadSteeringDoc();
    await vi.waitFor(() => {
      expect(ta.readOnly).toBe(false);
    });
    ta.focus();
    await userEvent.keyboard("!");

    H.get.mockClear();
    settleSave("success", { result: 'W/"2-2"' });
    await settle();

    // The write's own 200 carried the replacement, so a debounced run of saves needs no read
    // between keystrokes.
    expect(H.get).not.toHaveBeenCalled();
    await userEvent.keyboard("?");
    expect(H.save).toHaveBeenLastCalledWith({ content: "# first!?", etag: 'W/"2-2"' });
  });

  it("keeps the validator it had when the save could not read one back", async () => {
    answers("# first", 'W/"1-1"');
    loadSteeringDoc();
    await vi.waitFor(() => {
      expect(ta.readOnly).toBe(false);
    });
    ta.focus();
    await userEvent.keyboard("!");

    // "" is the server saying it could not stat the file it just wrote. Adopting it would send no
    // If-Match at all and take a 428, which reports this module as broken; keeping the stale one
    // takes the 409 that re-seeds the box.
    settleSave("success", { result: "" });
    await settle();

    await userEvent.keyboard("?");
    expect(H.save).toHaveBeenLastCalledWith({ content: "# first!?", etag: 'W/"1-1"' });
  });

  it("records what the WRITE carried, not the box, so a keystroke under it is not lost", async () => {
    answers("# first", 'W/"1-1"');
    loadSteeringDoc();
    await vi.waitFor(() => {
      expect(ta.readOnly).toBe(false);
    });
    ta.focus();
    await userEvent.keyboard("!");
    // A keystroke lands between the dispatch and its answer. Reading the BOX here would record "#
    // first!?" as agreed, and the next activation's re-seed would then treat the box as clean and
    // put the server's older text over it.
    await userEvent.keyboard("?");

    // The server answers the text the FIRST keystroke's save landed.
    settleSave("success", { args: { content: "# first!", etag: 'W/"1-1"' }, result: 'W/"2-2"' });
    await settle();

    answers("# first!", 'W/"2-2"');
    loadSteeringDoc();
    await settle();

    expect(ta.value).toBe("# first!?");
  });

  it("tells the reader and re-seeds when the file moved under the box", async () => {
    answers("# first", 'W/"1-1"');
    loadSteeringDoc();
    await vi.waitFor(() => {
      expect(ta.readOnly).toBe(false);
    });
    ta.focus();
    await userEvent.keyboard("!");

    answers("# somebody else", 'W/"2-2"');
    settleSave("error", { httpStatus: 409 });
    await vi.waitFor(() => {
      expect(ta.value).toBe("# somebody else");
    });

    expect(H.showError).toHaveBeenCalledWith("steering");
    expect(H.toast.mock.calls[0]?.[0]).toContain("were not saved");
    expect(H.toast.mock.calls[0]?.[1]).toBe("error");
  });

  it("reports a missing validator as this client's own fault", async () => {
    const spy = vi.spyOn(console, "error").mockImplementation(() => undefined);
    answers("# first");
    loadSteeringDoc();
    await vi.waitFor(() => {
      expect(ta.readOnly).toBe(false);
    });
    ta.focus();
    await userEvent.keyboard("!");

    settleSave("error", { httpStatus: 428 });

    // No toast: 428 means the PUT carried no If-Match, which is a bug in this module rather than
    // anything the reader did or can fix.
    expect(H.toast).not.toHaveBeenCalled();
    expect(spy.mock.calls[0]?.[0]).toContain("If-Match");
    spy.mockRestore();
  });
});

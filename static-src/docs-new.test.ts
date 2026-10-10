import { describe, it, expect, vi, beforeAll, beforeEach, afterEach, afterAll } from "vitest";
import { http, HttpResponse } from "msw";
import { setupWorker } from "msw/browser";

import { mountNewDocButton } from "./docs-new.js";
import { resetActionFramework } from "./actions/__test-helpers__/action-test-setup.js";

const CREATE = "/api/workspace/kiro-docs/new";

interface Sent {
  readonly path: string;
  readonly method: string;
  readonly key: string | null;
  readonly body: unknown;
}

const worker = setupWorker();
let sent: Sent[] = [];

async function record(request: Request): Promise<void> {
  sent.push({
    path: new URL(request.url).pathname,
    method: request.method,
    key: request.headers.get("Idempotency-Key"),
    body: await request.json(),
  });
}

beforeAll(async () => {
  await worker.start({ quiet: true });
});

afterAll(() => {
  worker.stop();
});

let slot: HTMLElement;
let onCreated: ReturnType<typeof vi.fn<(path: string) => void>>;
let startSpec: ReturnType<typeof vi.fn<(prompt: string, shown: string) => void>>;
let sync: ReturnType<typeof mountNewDocButton>;

beforeEach(() => {
  resetActionFramework();
  sent = [];
  // Any other API request is recorded too, so a drifted URL or method fails an assertion.
  worker.use(
    http.all("/api/*", async ({ request }) => {
      await record(request);
      return HttpResponse.json({ error: "unexpected request" }, { status: 500 });
    }),
  );
  slot = document.createElement("div");
  document.body.append(slot);
  onCreated = vi.fn<(path: string) => void>();
  startSpec = vi.fn<(prompt: string, shown: string) => void>();
  sync = mountNewDocButton(slot, onCreated, startSpec);
});

afterEach(() => {
  worker.resetHandlers();
  sync("memories");
  slot.remove();
  for (const p of document.querySelectorAll(".docs-new-popover")) {
    p.remove();
  }
});

function button(): HTMLButtonElement | null {
  return slot.querySelector<HTMLButtonElement>(".docs-new-btn");
}

function openForm(tab: Parameters<typeof sync>[0]): HTMLFormElement {
  sync(tab);
  button()?.click();
  const form = document.querySelector<HTMLFormElement>(".docs-new-popover form");
  if (form === null) {
    throw new Error(`no form opened on ${tab}`);
  }
  return form;
}

function labelled(
  form: HTMLFormElement,
  label: string,
): HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement {
  for (const l of form.querySelectorAll("label")) {
    if (l.firstChild?.textContent === label) {
      const c = l.querySelector<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>(
        "input, select, textarea",
      );
      if (c !== null) {
        return c;
      }
    }
  }
  throw new Error(`no field labelled ${label}`);
}

function labels(form: HTMLFormElement): string[] {
  return [...form.querySelectorAll("label")].map((l) => l.firstChild?.textContent ?? "");
}

function setValue(c: HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement, v: string): void {
  c.value = v;
  c.dispatchEvent(
    new Event(c instanceof HTMLSelectElement ? "change" : "input", { bubbles: true }),
  );
}

function submit(form: HTMLFormElement): void {
  form.requestSubmit(form.querySelector<HTMLButtonElement>("button[type=submit]"));
}

function settled(): Promise<void> {
  return new Promise((r) => setTimeout(r, 0));
}

function answer(status: number, body: Readonly<Record<string, string>>): void {
  worker.use(
    http.post(CREATE, async ({ request }) => {
      await record(request);
      return HttpResponse.json(body, { status });
    }),
  );
}

function posted(): Sent {
  expect(sent).toHaveLength(1);
  const [one] = sent;
  if (one === undefined) {
    throw new Error("no request was sent");
  }
  return one;
}

describe("the New button", () => {
  it("is present on each tab with a create flow and named for it", () => {
    for (const [tab, name] of [
      ["steering", "New steering doc"],
      ["skills", "New skill"],
      ["prompts", "New prompt"],
      ["agents", "New agent"],
      ["specs", "New spec"],
      ["hooks", "New hook"],
    ] as const) {
      sync(tab);
      expect(button()?.getAttribute("aria-label"), tab).toBe(name);
    }
  });

  it("is absent on Workflows, Memories and Powers", () => {
    for (const tab of ["workflows", "memories", "powers"] as const) {
      sync("steering");
      sync(tab);
      expect(button(), tab).toBeNull();
    }
  });

  it("closes an open form when the tab changes", () => {
    openForm("steering");
    sync("skills");
    expect(document.querySelector(".docs-new-popover")).toBeNull();
  });
});

describe("a file kind's form", () => {
  it("posts the steering name and type, then hands on the created path", async () => {
    answer(201, { path: "workspace/.kiro/steering/a.md" });
    const form = openForm("steering");
    setValue(labelled(form, "Type"), "manual");
    setValue(labelled(form, "Name"), "a");
    submit(form);
    await vi.waitFor(() => {
      expect(onCreated).toHaveBeenCalled();
    });
    const req = posted();
    expect(req.path).toBe(CREATE);
    expect(req.method).toBe("POST");
    expect(req.key).toMatch(/.+/);
    expect(req.body).toEqual({ category: "steering", name: "a", inclusion: "manual" });
    expect(onCreated).toHaveBeenCalledWith("workspace/.kiro/steering/a.md");
    expect(document.querySelector(".docs-new-popover")).toBeNull();
  });

  it("shows the server's refusal in the form and keeps it open", async () => {
    answer(409, { error: "Prompt 'p' already exists." });
    const form = openForm("prompts");
    setValue(labelled(form, "Name"), "p");
    submit(form);
    await vi.waitFor(() => {
      expect(form.querySelector(".docs-new-error")?.textContent).toBe("Prompt 'p' already exists.");
    });
    expect(onCreated).not.toHaveBeenCalled();
    expect(form.isConnected).toBe(true);
  });

  it("disables its submit while a create is in flight", async () => {
    let finish: () => void = () => undefined;
    const held = new Promise<void>((r) => (finish = r));
    worker.use(
      http.post(CREATE, async ({ request }) => {
        await record(request);
        await held;
        return HttpResponse.json({ error: "no" }, { status: 400 });
      }),
    );
    const form = openForm("agents");
    setValue(labelled(form, "Name"), "x");
    submit(form);
    await vi.waitFor(() => {
      expect(sent).toHaveLength(1);
    });
    submit(form);
    await settled();
    expect(sent).toHaveLength(1);
    finish();
    await vi.waitFor(() => {
      expect(form.querySelector<HTMLButtonElement>("button[type=submit]")?.disabled).toBe(false);
    });
  });

  it("leaves file-name length to the server, which counts the bytes of the name it writes", async () => {
    answer(400, { error: "That name makes a 256-byte file name; the limit is 255 bytes." });
    const form = openForm("steering");
    const name = labelled(form, "Name") as HTMLInputElement;
    expect(name.maxLength).toBe(-1);
    expect((labelled(openForm("agents"), "Name") as HTMLInputElement).maxLength).toBe(-1);
    expect((labelled(openForm("hooks"), "Title") as HTMLInputElement).maxLength).toBe(-1);
    const again = openForm("steering");
    setValue(labelled(again, "Name"), "s".repeat(253));
    submit(again);
    await vi.waitFor(() => {
      expect(again.querySelector(".docs-new-error")?.textContent).toBe(
        "That name makes a 256-byte file name; the limit is 255 bytes.",
      );
    });
    expect(posted().body).toMatchObject({ name: "s".repeat(253) });
  });

  it("asks a skill for the name and description Kiro requires", async () => {
    answer(201, { path: "p" });
    const form = openForm("skills");
    setValue(labelled(form, "Name"), "pr-review");
    setValue(labelled(form, "Description"), "Review PRs.");
    submit(form);
    await vi.waitFor(() => {
      expect(onCreated).toHaveBeenCalledWith("p");
    });
    expect(posted().body).toEqual({
      category: "skill",
      name: "pr-review",
      description: "Review PRs.",
    });
  });
});

describe("the hook form", () => {
  it("shows the matcher only for tool and file triggers, with its own wording", () => {
    const form = openForm("hooks");
    expect(labels(form)).toContain("File path pattern");
    setValue(labelled(form, "Trigger"), "PreToolUse");
    expect(labels(form)).toContain("Tool name pattern");
    setValue(labelled(form, "Trigger"), "Stop");
    expect(labels(form).some((l) => l.endsWith("pattern"))).toBe(false);
  });

  it("shows the timeout only for Run Command", () => {
    const form = openForm("hooks");
    expect(labels(form)).toContain("Timeout (seconds)");
    expect(labels(form)).toContain("Command to execute");
    setValue(labelled(form, "Action"), "agent");
    expect(labels(form)).not.toContain("Timeout (seconds)");
    expect(labels(form)).toContain("Instructions for Kiro agent");
  });

  it("keeps Create Hook disabled until the hook has a title", () => {
    const form = openForm("hooks");
    const create = form.querySelector<HTMLButtonElement>("button[type=submit]");
    expect(create?.disabled).toBe(true);
    setValue(labelled(form, "Title"), "Lint");
    expect(create?.disabled).toBe(false);
  });

  it("leaves the matcher to the server and shows its refusal", async () => {
    answer(400, { error: "Matcher must be a valid regular expression." });
    const form = openForm("hooks");
    setValue(labelled(form, "Title"), "Lint");
    setValue(labelled(form, "File path pattern"), "(");
    setValue(labelled(form, "Command to execute"), "npm run lint");
    submit(form);
    await vi.waitFor(() => {
      expect(form.querySelector(".docs-new-error")?.textContent).toBe(
        "Matcher must be a valid regular expression.",
      );
    });
    expect(posted().body).toMatchObject({ matcher: "(" });
  });

  it("posts the IDE's fields, dropping a matcher and timeout the choice does not use", async () => {
    answer(201, { path: "p" });
    const form = openForm("hooks");
    setValue(labelled(form, "Title"), "Greet");
    setValue(labelled(form, "Timeout (seconds)"), "9");
    setValue(labelled(form, "Trigger"), "SessionStart");
    setValue(labelled(form, "Action"), "agent");
    setValue(labelled(form, "Instructions for Kiro agent"), "Say hello");
    submit(form);
    await vi.waitFor(() => {
      expect(onCreated).toHaveBeenCalled();
    });
    expect(posted().body).toEqual({
      category: "hook",
      name: "Greet",
      description: "",
      trigger: "SessionStart",
      action: "agent",
      content: "Say hello",
    });
  });

  it("posts a command hook's matcher and timeout", async () => {
    answer(201, { path: "p" });
    const form = openForm("hooks");
    setValue(labelled(form, "Title"), "Lint");
    setValue(labelled(form, "File path pattern"), "\\.ts$");
    setValue(labelled(form, "Command to execute"), "npm run lint");
    setValue(labelled(form, "Timeout (seconds)"), "30");
    submit(form);
    await vi.waitFor(() => {
      expect(onCreated).toHaveBeenCalled();
    });
    expect(posted().body).toEqual(
      expect.objectContaining({
        trigger: "PostFileSave",
        matcher: "\\.ts$",
        action: "command",
        timeout: 30,
      }),
    );
  });
});

describe("the spec form", () => {
  it("starts kiro-cli's spec chat with the description as the shown message", () => {
    const form = openForm("specs");
    setValue(labelled(form, "Name"), "user-auth");
    setValue(labelled(form, "Description"), "Sign in with passkeys");
    submit(form);
    expect(startSpec).toHaveBeenCalledWith(
      'Start a new spec called "user-auth". Create the .kiro/specs/user-auth/ directory and draft the initial requirements document.\n\n' +
        "The user described what this spec should cover \u2014 treat this as the ground truth for the requirements:\nSign in with passkeys",
      "Sign in with passkeys",
    );
    expect(sent).toHaveLength(0);
    expect(document.querySelector(".docs-new-popover")).toBeNull();
  });

  it("refuses a name outside Kiro's spec-name rule", async () => {
    const form = openForm("specs");
    setValue(labelled(form, "Name"), "user auth");
    setValue(labelled(form, "Description"), "x");
    submit(form);
    await settled();
    expect(form.querySelector(".docs-new-error")?.textContent).toBe(
      "Name can only contain letters, numbers, hyphens, and underscores.",
    );
    expect(startSpec).not.toHaveBeenCalled();
  });

  it("refuses a blank description as kiro-cli does", async () => {
    const form = openForm("specs");
    setValue(labelled(form, "Name"), "ok");
    setValue(labelled(form, "Description"), "   ");
    form.querySelector<HTMLTextAreaElement>("textarea")?.removeAttribute("required");
    submit(form);
    await settled();
    expect(form.querySelector(".docs-new-error")?.textContent).toBe(
      "Describe the spec in words first.",
    );
  });
});

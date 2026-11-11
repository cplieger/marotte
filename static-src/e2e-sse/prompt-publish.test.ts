// Against the REAL binary: what a prompt's open publishes, and the digest that covers a stream cut
// inside it. The open sends `turn_opened` (unstamped), then `draft_changed` (stamped `chat`) when
// the prompt cleared a page's draft, then one coalesced `chat_updated` (stamped `chats`). Each cut
// case kills the stream after `turn_opened` (close-after hook), reconnects FRESH (no replay) and
// requires the hello's digest to answer `changed`. Node, not browser: the fixture refuses
// cross-site POSTs and sets no CORS header.

import { afterEach, beforeEach, describe, expect, inject, it } from "vitest";

import {
  type DigestResult,
  type LifecycleEvent,
  type Stream,
  createDigestClient,
  createOnlineManager,
  createStream,
  createVisibilityManager,
} from "@cplieger/sse";

import { decodeEnvelope } from "../bus.js";
import { _resetForTest, observeStamp, versionMap } from "../subject-versions.js";
import type { ServerEvent } from "../types.js";
import { decodeChatHeader, decodeSubjectStamp } from "../wire/decoders.gen.js";
import type { ChatHeader } from "../wire/types.gen.js";

const SKIP_REASON =
  "marotte fixture not started: set SSE_FIXTURE to a marotte binary built with -tags marotte_test";
const FIXTURE = process.env["SSE_FIXTURE"];
if (FIXTURE === undefined || FIXTURE === "") {
  console.warn(`[vitest] ${SKIP_REASON}`);
}

/** Frames a FRESH connect writes before any live frame (hello, connected, pending_snapshot,
 *  status_snapshot); the hook counts data frames, so a live frame's ordinal is HOOK_FRAMES + n. */
const HOOK_FRAMES = 4;

/** How long a test waits after the open's last expected frame before it counts header frames:
 *  ten times the server's 25 ms header coalescing window, so a second publish would have landed. */
const QUIET_MS = 250;

interface Client {
  readonly stream: Stream;
  readonly frames: ServerEvent[];
  readonly lifecycle: LifecycleEvent[];
  readonly digests: DigestResult[];
}

interface ChatRead {
  readonly version: string;
  readonly draft: string;
  /** The client-minted prompt id of every turn the window holds: a turn's own id is
   *  server-minted, and `turn_open.prompt.id` is what the command sent. */
  readonly promptIDs: string[];
}

function until(predicate: () => boolean, what: string, timeoutMs = 10_000): Promise<void> {
  return new Promise((resolve, reject) => {
    const deadline = Date.now() + timeoutMs;
    const tick = (): void => {
      if (predicate()) {
        resolve();
        return;
      }
      if (Date.now() > deadline) {
        reject(new Error(`timed out waiting for ${what}`));
        return;
      }
      setTimeout(tick, 10);
    };
    tick();
  });
}

function pause(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function messageID(): string {
  return `m-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`;
}

function subjects(states: readonly { kind: string; ref: string }[]): string[] {
  return states.map((s) => `${s.kind}:${s.ref}`).sort();
}

describe.skipIf(FIXTURE === undefined || FIXTURE === "")("a prompt's publish", () => {
  const base = (): string => inject("marotteURL");
  let client: Client | null = null;

  const unlisten = (): (() => void) => () => undefined;
  const visibility = createVisibilityManager({ visible: () => true, listen: unlisten });
  const online = createOnlineManager({ online: () => true, listen: unlisten });

  /** Sent as the page sends it (`Sec-Fetch-Site: same-origin`), so a prompt settles the composer. */
  async function command(type: string, chatID: string, payload: unknown): Promise<unknown> {
    const res = await fetch(`${base()}/api/command`, {
      method: "POST",
      headers: { "Content-Type": "application/json", "Sec-Fetch-Site": "same-origin" },
      body: JSON.stringify({ type, chat_id: chatID, payload }),
    });
    if (!res.ok) {
      throw new Error(`${type} answered ${String(res.status)}: ${await res.text()}`);
    }
    return res.json();
  }

  /** A named chat, so the prompt's composer settle has no default name to replace. */
  async function createChat(c: Client): Promise<string> {
    const body = (await command("create_chat", "", { name: "prompt-publish" })) as {
      chat: { id: string };
    };
    const id = body.chat.id;
    await until(
      () => c.frames.some((f) => f.type === "chat_created" && f.chat_id === id),
      "chat_created",
    );
    return id;
  }

  /** `GET /api/chats/{id}`: the client's own load, observing every stamp as store-load does. */
  async function loadChat(id: string): Promise<ChatRead> {
    const res = await fetch(`${base()}/api/chats/${encodeURIComponent(id)}?limit=50`);
    const body = (await res.json()) as {
      subject?: unknown;
      draft?: unknown;
      entries?: { kind?: string; payload?: { prompt?: { id?: string } } }[];
    };
    if (!Array.isArray(body.subject)) {
      throw new Error(
        `GET /api/chats/${id} answered subject ${JSON.stringify(body.subject)}, want an array`,
      );
    }
    const stamps = body.subject.map((s) => decodeSubjectStamp(s));
    for (const stamp of stamps) {
      observeStamp(stamp);
    }
    const own = stamps.find((s) => s.kind === "chat" && s.ref === id);
    if (own === undefined) {
      throw new Error(`GET /api/chats/${id} carried no chat stamp: ${JSON.stringify(stamps)}`);
    }
    return {
      version: own.version,
      draft: typeof body.draft === "string" ? body.draft : "",
      promptIDs: (body.entries ?? [])
        .filter((e) => e.kind === "turn_open")
        .map((e) => e.payload?.prompt?.id ?? ""),
    };
  }

  async function closeAfter(n: number): Promise<void> {
    const res = await fetch(`${base()}/api/test/sse/close-after`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ after: n }),
    });
    expect(res.status).toBe(204);
  }

  function connect(): Client {
    const frames: ServerEvent[] = [];
    const lifecycle: LifecycleEvent[] = [];
    const digests: DigestResult[] = [];
    const digest = createDigestClient({ url: `${base()}/api/sync`, timeoutMs: 10_000 });
    const versions = versionMap();
    const stream = createStream({
      url: `${base()}/api/events`,
      headers: { "SSE-Client": "e2e_prompt_publish_" },
      versions,
      visibility,
      online,
      onFrame(frame) {
        const evt = decodeEnvelope(JSON.parse(frame.data));
        frames.push(evt);
        // The adapter's rule, with the chat always resident here.
        observeStamp(evt.subject);
      },
      onLifecycle(ev) {
        lifecycle.push(ev);
      },
      async revalidate(ctx) {
        const snapshot = versions.snapshot();
        if (ctx.full || snapshot.held.length === 0) {
          return;
        }
        digests.push(await digest.check(snapshot, ctx.signal));
      },
    });
    const c: Client = { stream, frames, lifecycle, digests };
    stream.start();
    return c;
  }

  function hellos(c: Client): Extract<LifecycleEvent, { kind: "hello" }>[] {
    return c.lifecycle.filter((e) => e.kind === "hello");
  }

  function dropped(c: Client): number {
    return c.lifecycle.filter((e) => e.kind === "state" && e.from === "open").length;
  }

  function hooksSeen(c: Client): number {
    return c.frames.filter((f) => f.type === "status_snapshot").length;
  }

  function heldVersion(id: string): string | undefined {
    return versionMap()
      .snapshot()
      .held.find((h) => h.kind === "chat" && h.ref === id)?.version;
  }

  /** The frames for `id` after `from`, with every header frame decoded. */
  function chatFrames(c: Client, id: string, from: number): ServerEvent[] {
    return c.frames.slice(from).filter((f) => f.chat_id === id);
  }

  function headers(frames: readonly ServerEvent[]): ChatHeader[] {
    return frames.filter((f) => f.type === "chat_updated").map((f) => decodeChatHeader(f.payload));
  }

  /** Arms the cut for the NEXT connection, then makes this client's fresh reconnect be it. */
  async function reconnectArmed(c: Client, after: number): Promise<void> {
    const before = hellos(c).length;
    await closeAfter(after);
    c.stream.resetCursor();
    c.stream.reconnect();
    await until(() => hooksSeen(c) === before + 1, "the armed connection's hook");
  }

  /** Reconnects fresh and returns the digest that hello runs. */
  async function reconnectFresh(c: Client): Promise<DigestResult> {
    const before = c.digests.length;
    c.stream.resetCursor();
    c.stream.reconnect();
    await until(() => c.digests.length > before, "the fresh hello's digest");
    const hello = hellos(c).at(-1);
    expect(hello?.resumed).toBe(false);
    const verdict = c.digests.at(-1);
    if (verdict === undefined) {
      throw new Error("no digest");
    }
    return verdict;
  }

  beforeEach(() => {
    _resetForTest();
  });

  afterEach(() => {
    client?.stream.stop();
    client = null;
  });

  it("the open sends turn_opened, then one header frame bound to the turn", async () => {
    client = connect();
    const c = client;
    await until(() => hooksSeen(c) === 1, "the first hook");
    const id = await createChat(c);
    await pause(QUIET_MS);
    const from = c.frames.length;

    // The fixture's kiro-cli never answers, so the open is all this prompt publishes.
    await command("prompt", id, { text: "hello", message_id: messageID() });
    await until(() => headers(chatFrames(c, id, from)).length > 0, "the open's chat_updated");
    await pause(QUIET_MS);

    const frames = chatFrames(c, id, from);
    expect(frames.map((f) => f.type)).toEqual(["turn_opened", "chat_updated"]);
    expect(frames[0]?.subject).toBeUndefined();
    expect(frames[1]?.subject?.kind).toBe("chats");
    const [header] = headers(frames);
    expect(header?.turn_count).toBe(1);
    expect(header?.run.entries).toHaveLength(1);
    expect(header?.run.entries[0]?.turn).toMatch(/^t-/);
  });

  it("a prompt that clears a page's draft adds one draft_changed and still one header frame", async () => {
    client = connect();
    const c = client;
    await until(() => hooksSeen(c) === 1, "the first hook");
    const id = await createChat(c);
    await command("set_draft", id, { text: "typed on B" });
    await until(
      () => c.frames.some((f) => f.type === "draft_changed" && f.chat_id === id),
      "the draft's draft_changed",
    );
    await pause(QUIET_MS);
    const from = c.frames.length;

    await command("prompt", id, { text: "typed on B", message_id: messageID() });
    await until(() => headers(chatFrames(c, id, from)).length > 0, "the open's chat_updated");
    await pause(QUIET_MS);

    const frames = chatFrames(c, id, from);
    expect(frames.map((f) => f.type)).toEqual(["turn_opened", "draft_changed", "chat_updated"]);
    expect(frames[1]?.subject?.kind).toBe("chat");
    expect(frames[2]?.subject?.kind).toBe("chats");
  });

  it("transcript variant: the stream dies after turn_opened, the fresh digest says changed", async () => {
    client = connect();
    const c = client;
    await until(() => hooksSeen(c) === 1, "the first hook");
    const id = await createChat(c);
    await pause(QUIET_MS);
    const before = await loadChat(id);
    expect(before.promptIDs).toEqual([]);
    expect(heldVersion(id)).toBe(before.version);

    // Frame 5 is `turn_opened`; frame 6, the coalesced header frame, is the write that fails.
    await reconnectArmed(c, HOOK_FRAMES + 1);
    const seenBefore = c.frames.length;
    const dropsBefore = dropped(c);

    const sent = messageID();
    await command("prompt", id, { text: "hello", message_id: sent });
    await until(() => dropped(c) === dropsBefore + 1, "the armed connection to be cut");

    const delivered = c.frames.slice(seenBefore).map((f) => f.type);
    expect(delivered).toContain("turn_opened");
    expect(delivered).not.toContain("chat_updated");
    // `turn_opened` carries no stamp: the held chat version is unmoved.
    expect(heldVersion(id)).toBe(before.version);

    const verdict = await reconnectFresh(c);
    expect(verdict.kind).toBe("ok");
    if (verdict.kind === "ok") {
      // The open moved chat:X, and the header frame that would have moved `chats` was lost.
      expect(subjects(verdict.changed)).toEqual([`chat:${id}`, "chats:"]);
      expect(verdict.removed).toEqual([]);
    }
    // And the refetch lands the message the stream lost.
    const after = await loadChat(id);
    expect(after.promptIDs).toEqual([sent]);
    expect(after.version).not.toBe(before.version);
  });

  it("composer variant: device A holds B's draft, the stream dies before draft_changed", async () => {
    client = connect();
    const c = client;
    await until(() => hooksSeen(c) === 1, "the first hook");
    const id = await createChat(c);
    // Device B types; A loads the chat and holds the draft with its version.
    await command("set_draft", id, { text: "typed on B" });
    await until(
      () => c.frames.some((f) => f.type === "draft_changed" && f.chat_id === id),
      "B's draft_changed",
    );
    await pause(QUIET_MS);
    const before = await loadChat(id);
    expect(before.draft).toBe("typed on B");
    expect(heldVersion(id)).toBe(before.version);

    // Frame 5 is `turn_opened`; frame 6, `draft_changed`, is the write that fails.
    await reconnectArmed(c, HOOK_FRAMES + 1);
    const seenBefore = c.frames.length;
    const dropsBefore = dropped(c);

    // The prompt leaves the composer and clears the draft.
    const sent = messageID();
    await command("prompt", id, { text: "typed on B", message_id: sent });
    await until(() => dropped(c) === dropsBefore + 1, "the armed connection to be cut");

    const delivered = c.frames.slice(seenBefore);
    expect(delivered.map((f) => f.type)).toContain("turn_opened");
    expect(delivered.map((f) => f.type)).not.toContain("draft_changed");
    expect(heldVersion(id)).toBe(before.version);

    const verdict = await reconnectFresh(c);
    expect(verdict.kind).toBe("ok");
    if (verdict.kind === "ok") {
      // The cut lands before the header window closes, so this digest may run before the `chats`
      // bump; the header frame then reaches the new stream instead.
      const changed = subjects(verdict.changed);
      expect(changed).toContain(`chat:${id}`);
      expect(changed.filter((s) => s !== `chat:${id}` && s !== "chats:")).toEqual([]);
      expect(verdict.removed).toEqual([]);
    }
    // The refetch clears A's composer and lands the message.
    const after = await loadChat(id);
    expect(after.draft).toBe("");
    expect(after.promptIDs).toEqual([sent]);
    expect(after.version).not.toBe(before.version);
  });
});

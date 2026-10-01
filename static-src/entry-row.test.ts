import { describe, it, expect, vi } from "vitest";

import { entryRow, entryList, entrySkeleton, entryDetail } from "./entry-row.js";

// ---------------------------------------------------------------------------
// What the builder ENFORCES, as opposed to what it copies through. Each case is
// a rule a page could otherwise get wrong on its own: the door/inert branch, the
// accessible name, the badge cap, the three subtitle shapes, the list roles, and
// the phrasing-content rule a `<button>` body imposes.
// ---------------------------------------------------------------------------

/** A door needs an opener; only one case asserts it is called. */
const noop = (): void => undefined;

const badge = (text: string): HTMLElement => {
  const b = document.createElement("span");
  b.className = "docs-badge";
  b.textContent = text;
  return b;
};

describe("entryRow: the door", () => {
  it("makes the body a button and names it after the row", () => {
    const row = entryRow({
      key: "k",
      title: "Fix the parser",
      open: { name: "Fix the parser", onOpen: noop },
    });
    const body = row.querySelector(".entry-open");
    expect(body?.tagName).toBe("BUTTON");
    expect(body?.getAttribute("aria-label")).toBe("Open Fix the parser");
    expect(body?.getAttribute("type")).toBe("button");
  });

  it("opens on click", () => {
    const onOpen = vi.fn();
    const row = entryRow({ key: "k", title: "t", open: { name: "t", onOpen } });
    row.querySelector<HTMLButtonElement>(".entry-open")?.click();
    expect(onOpen).toHaveBeenCalledTimes(1);
  });

  it("leaves an inert row a plain div with no role, tabindex or listener", () => {
    const row = entryRow({ key: "k", title: "bundled-goal" });
    const body = row.querySelector(".entry-body");
    expect(body?.tagName).toBe("DIV");
    expect(row.querySelector(".entry-open")).toBeNull();
    expect(body?.hasAttribute("role")).toBe(false);
    expect(body?.hasAttribute("tabindex")).toBe(false);
  });
});

describe("entryRow: the title line", () => {
  it("caps the badges at two and drops the third", () => {
    const row = entryRow({
      key: "k",
      title: "t",
      badges: [badge("fileMatch"), badge("override"), badge("global")],
    });
    const badges = [...(row.querySelector(".entry-badges")?.children ?? [])];
    expect(badges.map((b) => b.textContent)).toEqual(["fileMatch", "override"]);
  });

  it("seats a mark in the name group after the title, outside the badge cap", () => {
    const mark = document.createElement("span");
    mark.className = "docs-git-letter";
    mark.textContent = "M";
    const row = entryRow({
      key: "k",
      title: "t",
      mark,
      badges: [badge("fileMatch"), badge("override")],
    });
    const line = [...(row.querySelector(".entry-line")?.children ?? [])];
    expect(line[0]?.classList.contains("entry-name")).toBe(true);
    const name = [...(line[0]?.children ?? [])];
    expect(name[0]?.classList.contains("entry-title")).toBe(true);
    expect(name[1]).toBe(mark);
    expect(line[1]?.classList.contains("entry-badges")).toBe(true);
    expect(row.querySelector(".entry-badges")?.children).toHaveLength(2);
  });

  it("mounts no badge slot when there are none", () => {
    const row = entryRow({ key: "k", title: "t", badges: [] });
    expect(row.querySelector(".entry-badges")).toBeNull();
  });

  it("renders a timestamp as a <time> with the absolute form as its tooltip", () => {
    const ms = Date.now() - 3 * 60 * 60 * 1000;
    const row = entryRow({ key: "k", title: "t", time: { ms } });
    const time = row.querySelector(".entry-time");
    expect(time?.tagName).toBe("TIME");
    expect(time?.textContent).toBe("3 hours ago");
    expect(time?.getAttribute("datetime")).toBe(new Date(ms).toISOString());
    expect(time?.getAttribute("data-tooltip")).toBe(new Date(ms).toLocaleString());
  });

  it("renders a borrowed count as a span with no tooltip and no datetime", () => {
    const row = entryRow({ key: "k", title: "t", time: { text: "12 hits" } });
    const slot = row.querySelector(".entry-time");
    expect(slot?.tagName).toBe("SPAN");
    expect(slot?.textContent).toBe("12 hits");
    expect(slot?.hasAttribute("data-tooltip")).toBe(false);
  });
});

describe("entryRow: the subtitle region", () => {
  it("renders one ellipsised line", () => {
    const row = entryRow({
      key: "k",
      title: "t",
      sub: { kind: "line", text: "gpt-5 · Plan · 4 turns" },
    });
    const sub = row.querySelector(".entry-sub");
    expect(sub?.textContent).toBe("gpt-5 · Plan · 4 turns");
    expect(sub?.classList.contains("entry-sub-clamp")).toBe(false);
  });

  it("marks a description for the two-line clamp", () => {
    const row = entryRow({
      key: "k",
      title: "t",
      sub: { kind: "clamp", text: "A long description." },
    });
    expect(row.querySelector(".entry-sub")?.classList.contains("entry-sub-clamp")).toBe(true);
  });

  it("renders a pair of facts as two lines, mono where asked", () => {
    const row = entryRow({
      key: "k",
      title: "t",
      sub: { kind: "lines", lines: [{ text: "PostFileSave", mono: true }, { text: "npm test" }] },
    });
    const lines = [...(row.querySelector(".entry-lines")?.children ?? [])];
    expect(lines.map((l) => l.textContent)).toEqual(["PostFileSave", "npm test"]);
    expect(lines[0]?.classList.contains("entry-sub-mono")).toBe(true);
    expect(lines[1]?.classList.contains("entry-sub-mono")).toBe(false);
  });

  it("reserves an empty subtitle line when the row has none", () => {
    // The body centres in the row, so a row with NO line under its title would
    // sit that title lower than its neighbours'.
    const row = entryRow({ key: "k", title: "t" });
    const sub = row.querySelector(".entry-sub");
    expect(sub?.textContent).toBe("");
    expect(sub?.classList.contains("entry-sub-clamp")).toBe(false);
    expect(row.querySelector(".entry-lines")).toBeNull();
  });
});

describe("entryRow: the row's own contract", () => {
  it("carries its key and the page's own attributes", () => {
    const row = entryRow({ key: "s:abc", title: "t", data: { "data-hook-id": "h1" } });
    expect(row.getAttribute("data-key")).toBe("s:abc");
    expect(row.getAttribute("data-hook-id")).toBe("h1");
  });

  it("is a listitem", () => {
    expect(entryRow({ key: "k", title: "t" }).getAttribute("role")).toBe("listitem");
  });

  it("puts the lead before the body and the actions after it", () => {
    const lead = document.createElement("span");
    lead.className = "entry-lead";
    const action = document.createElement("button");
    const row = entryRow({ key: "k", title: "t", lead, actions: [action] });
    const kids = [...row.children];
    expect(kids[0]).toBe(lead);
    expect(kids[1]?.classList.contains("entry-body")).toBe(true);
    expect(kids[2]?.classList.contains("entry-actions")).toBe(true);
  });

  // A `div` anywhere under the control is invalid HTML the parser hoists OUT of
  // the button, so the rule is checked once per subtitle shape rather than once:
  // each shape builds a different element and only its own case can see it.
  it("puts NO div inside the open button of a two-fact row", () => {
    const row = entryRow({
      key: "k",
      title: "t",
      badges: [badge("model"), badge("3 tools")],
      time: { ms: Date.now() },
      sub: { kind: "lines", lines: [{ text: "a" }, { text: "b", mono: true }] },
      open: { name: "t", onOpen: noop },
    });
    expect(row.querySelector(".entry-open")?.querySelectorAll("div, p, ul, li")).toHaveLength(0);
  });

  it("puts NO div inside the open button of a described row", () => {
    const row = entryRow({
      key: "k",
      title: "t",
      time: { text: "3 hits" },
      sub: { kind: "clamp", text: "A described row." },
      open: { name: "t", onOpen: noop },
    });
    expect(row.querySelector(".entry-open")?.querySelectorAll("div, p, ul, li")).toHaveLength(0);
  });
});

describe("entryList / entrySkeleton / entryDetail", () => {
  it("builds the list card as a role=list container", () => {
    const list = entryList();
    expect(list.className).toBe("list-container");
    expect(list.getAttribute("role")).toBe("list");
  });

  it("builds N placeholder rows on the row's own class, hidden from the a11y tree", () => {
    const rows = entrySkeleton(4);
    expect(rows).toHaveLength(4);
    for (const r of rows) {
      expect(r.classList.contains("entry")).toBe(true);
      expect(r.getAttribute("aria-hidden")).toBe("true");
      expect(r.querySelectorAll(".skeleton")).toHaveLength(2);
    }
  });

  it("builds a detail region the list may own, holding what it was given", () => {
    // A `role="list"` may own nothing but list items: a bare region under a row
    // made the list own the form's own controls (axe aria-required-children).
    const form = document.createElement("form");
    const detail = entryDetail(form);
    expect(detail.className).toBe("entry-detail");
    expect(detail.getAttribute("role")).toBe("listitem");
    expect(detail.firstElementChild).toBe(form);
  });
});

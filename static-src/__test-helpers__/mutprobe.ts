// Counts the page's mutation records and finds the same-value writes among them: an attribute set to the value it
// held, a text node given the text it held, an element's text replaced by the same text. A same-value write still
// queues a record and, for a selector-read attribute, a restyle (dom.ts `setAttr`, `setText`).

import { pageRoots, queryAll } from "../dom.js";

interface SameValueWrite {
  readonly kind: "attribute" | "text";
  /** The attribute's name, or `#text`. */
  readonly name: string;
  /** The nearest element with an id holding the target, as `#id`, else the target's tag. */
  readonly where: string;
  /** Whether the target sits in a parked surface (dom.ts `park`) when the record is read. */
  readonly parked: boolean;
}

interface MutationTally {
  readonly records: number;
  readonly sameValue: readonly SameValueWrite[];
}

export interface MutationProbe {
  /** Discard every record so far, observe the surfaces added since, and open a counting window. */
  reset(): void;
  /** The records since the last reset, and the same-value writes among them. */
  take(): MutationTally;
  dispose(): void;
}

const OPTIONS: MutationObserverInit = {
  subtree: true,
  childList: true,
  attributes: true,
  attributeOldValue: true,
  characterData: true,
  characterDataOldValue: true,
};

// A parked surface lives in a windowless document the page's registration cannot see, so the probe also observes
// that document and every surface park moves. One observer gets one record per mutation however many of its
// registrations hold the target, so the extra registrations never count a write twice.
const SURFACES = "[data-tab-view], dialog";

/** Observe the page and its parked surfaces until `dispose`. */
export function installMutationProbe(): MutationProbe {
  const pending: MutationRecord[] = [];
  const observer = new MutationObserver((records) => {
    pending.push(...records);
  });
  const observeAll = (): void => {
    for (const root of [...pageRoots(), ...queryAll(SURFACES)]) {
      observer.observe(root, OPTIONS);
    }
  };
  observeAll();
  const drain = (): MutationRecord[] => {
    const all = [...pending, ...observer.takeRecords()];
    pending.length = 0;
    return all;
  };
  return {
    reset() {
      drain();
      observeAll();
    },
    take() {
      const records = drain();
      return { records: records.length, sameValue: sameValueWrites(records) };
    },
    dispose() {
      observer.disconnect();
      pending.length = 0;
    },
  };
}

// A record holds only the old value, so a write's new value is the next record's old value on the same attribute or
// text node, or the value now for the last one: A to B to A in one batch is two changes, not a same-value write.
function sameValueWrites(records: readonly MutationRecord[]): SameValueWrite[] {
  const found: SameValueWrite[] = [];
  const later = new Map<Node, Map<string, string | null>>();
  const valueAfter = (r: MutationRecord, name: string, now: () => string | null): string | null => {
    const byName = later.get(r.target) ?? new Map<string, string | null>();
    later.set(r.target, byName);
    const after = byName.has(name) ? (byName.get(name) ?? null) : now();
    byName.set(name, r.oldValue);
    return after;
  };
  for (let i = records.length - 1; i >= 0; i--) {
    const r = records[i];
    if (r === undefined) {
      continue;
    }
    if (r.type === "attributes" && r.attributeName !== null && r.target instanceof Element) {
      const { target, attributeName } = r;
      if (valueAfter(r, attributeName, () => target.getAttribute(attributeName)) === r.oldValue) {
        found.push(write("attribute", attributeName, r.target));
      }
    } else if (r.type === "characterData") {
      if (valueAfter(r, "#text", () => r.target.nodeValue) === r.oldValue) {
        found.push(write("text", "#text", r.target));
      }
    } else if (r.type === "childList" && sameText(r)) {
      found.push(write("text", "#text", r.target));
    }
  }
  return found.reverse();
}

/** An element's text replaced by the same text: `textContent` or `innerText` set to what it read. */
function sameText(r: MutationRecord): boolean {
  const removed = [...r.removedNodes];
  const added = [...r.addedNodes];
  const text = (nodes: Node[]): string | null =>
    nodes.length > 0 && nodes.every((n) => n.nodeType === Node.TEXT_NODE)
      ? nodes.map((n) => n.nodeValue).join("")
      : null;
  const before = text(removed);
  return before !== null && before === text(added);
}

function write(kind: SameValueWrite["kind"], name: string, target: Node): SameValueWrite {
  let e: Element | null = target instanceof Element ? target : target.parentElement;
  const tag = e?.localName ?? "#detached";
  while (e !== null && e.id === "") {
    e = e.parentElement;
  }
  return {
    kind,
    name,
    where: e === null ? tag : `#${e.id}`,
    parked: target.ownerDocument !== document,
  };
}

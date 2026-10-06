// Licensed-code attribution footnote (v3 `_kiro/code_references`). The wire carries {license_name, repository, url}
// per reference with no content span, so an attribution annotates the whole turn.

import { el } from "@cplieger/reactive";
import type { CodeReference } from "./types.js";
import { isSafeURL } from "./url-safety.js";
import { iconEl } from "./icon-el.js";
import { ICON_SCALE, ICON_EXTERNAL } from "./icons.js";
import { featureDisabled } from "./governance.js";

const CLS = "code-refs";

/**
 * Make `wrap`'s footnote match `refs`: append, remove, or rebuild only when the count changed, preserving open state.
 * Takes the resolved list (`turn_close.code_references`, else store.ts `codeReferencesFor`), so precedence has one home.
 */
export function syncCodeReferences(wrap: HTMLElement, refs?: readonly CodeReference[]): void {
  const existing = wrap.querySelector<HTMLDetailsElement>(`:scope > .${CLS}`);
  // Hidden when governance reports the tracker off, even against a stray persisted reference.
  const shown = featureDisabled("code_reference_tracker") ? [] : (refs ?? []);
  if (shown.length === 0) {
    existing?.remove();
    return;
  }
  // Count is a safe signature: the server sends a monotonically growing, deduped list.
  if (existing !== null && existing.dataset["count"] === String(shown.length)) {
    return;
  }
  const wasOpen = existing?.open ?? false;
  const built = buildCodeRefs(shown, wasOpen);
  if (existing === null) {
    wrap.appendChild(built);
  } else {
    existing.replaceWith(built);
  }
}

function buildCodeRefs(refs: readonly CodeReference[], open: boolean): HTMLDetailsElement {
  const count = refs.length;
  const details = el("details", {
    className: CLS,
    "data-count": String(count),
    title:
      "This turn reproduced code recognized as referenced open-source. " +
      "Review the license before reusing it.",
  }) as HTMLDetailsElement;
  details.open = open;

  const summaryText = count === 1 ? "1 code reference" : `${String(count)} code references`;
  const summary = el(
    "summary",
    { className: "code-refs-summary" },
    iconEl(ICON_SCALE),
    el("span", { className: "code-refs-count" }, summaryText),
  );
  details.appendChild(summary);

  const list = el("ul", { className: "code-refs-list" });
  for (const ref of refs) {
    list.appendChild(buildItem(ref));
  }
  details.appendChild(list);
  return details;
}

/** A link only when the URL passes `isSafeURL`; otherwise the plain source label. */
function buildItem(ref: CodeReference): HTMLLIElement {
  const item = el("li", { className: "code-refs-item" }) as HTMLLIElement;
  item.appendChild(el("span", { className: "code-refs-license" }, ref.license_name));

  const url = ref.url ?? "";
  const safe = url !== "" && isSafeURL(url);
  const label = sourceLabel(ref, safe ? url : "");
  if (safe) {
    item.appendChild(
      el(
        "a",
        {
          className: "code-refs-link",
          href: url,
          target: "_blank",
          rel: "noopener noreferrer",
        },
        label,
        iconEl(ICON_EXTERNAL),
      ),
    );
  } else {
    item.appendChild(el("span", { className: "code-refs-source" }, label));
  }
  return item;
}

/** Never the raw URL. `safeURL` is empty unless `isSafeURL` confirmed it, so the parse cannot throw. */
function sourceLabel(ref: CodeReference, safeURL: string): string {
  const repo = (ref.repository ?? "").trim();
  if (repo !== "") {
    return repo;
  }
  if (safeURL !== "") {
    return new URL(safeURL).host || "source";
  }
  return "source";
}

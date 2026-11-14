// The sidebar's tab column as `static/index.html` nests it, bare: `tabs.ts` seats Open rows in
// `#tab-list`, pinned rows in `#pinned-list`, and scrolls `#tab-sections`.

export const TAB_SECTIONS_HTML =
  '<div id="tab-sections">' +
  '<section id="open-section" class="side-section"><div id="tab-list" class="side-section-list"></div></section>' +
  '<section id="pinned-section" class="side-section" hidden><div id="pinned-list" class="side-section-list"></div></section>' +
  "</div>";

/** Every strip row, Open's then Pinned's, as `selector` narrows them. */
export function stripRows(selector = "[data-tab-id]"): HTMLElement[] {
  return [
    ...document.querySelectorAll<HTMLElement>(
      `#tab-list > ${selector}, #pinned-list > ${selector}`,
    ),
  ];
}

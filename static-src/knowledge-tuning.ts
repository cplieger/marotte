// The knowledge index's glob lists and passage size under Knowledge base search. The index type and
// file limit are settings.ts's plain select and count rows.

import { maybeEl } from "./dom.js";
import { joinGlobList, splitGlobList } from "./glob-list.js";
import { settingsAsShown } from "./persist.js";
import type { EffectiveSettings } from "./persist.js";
import { saveControl } from "./save-control.js";
import type { KnowledgeChunking } from "./wire/types.gen.js";

const patternInputs = [
  { inputID: "knowledge-include-patterns", key: "knowledge_include_patterns" },
  { inputID: "knowledge-exclude-patterns", key: "knowledge_exclude_patterns" },
] as const;

/** settings.ValidKnowledgeChunking's floor for the passage size, kiro-cli's own. */
const MIN_CHUNK_SIZE = 64;

/** The chunking two typed numbers ask for, the size raised to 64 words and the overlap kept from 0
 *  to below the size; null when either box is empty or unreadable. */
function chunkingFrom(sizeRaw: string, overlapRaw: string): KnowledgeChunking | null {
  if (sizeRaw.trim() === "" || overlapRaw.trim() === "") {
    return null;
  }
  const size = Math.round(Number(sizeRaw.trim()));
  const overlap = Math.round(Number(overlapRaw.trim()));
  if (!Number.isSafeInteger(size) || !Number.isSafeInteger(overlap)) {
    return null;
  }
  const bounded = Math.max(size, MIN_CHUNK_SIZE);
  return { size: bounded, overlap: Math.min(Math.max(overlap, 0), bounded - 1) };
}

interface ChunkBoxes {
  readonly size: HTMLInputElement;
  readonly overlap: HTMLInputElement;
}

function chunkBoxes(): ChunkBoxes | null {
  const size = maybeEl<HTMLInputElement>("knowledge-chunk-size");
  const overlap = maybeEl<HTMLInputElement>("knowledge-chunk-overlap");
  return size === null || overlap === null ? null : { size, overlap };
}

function paintChunkBoxes(boxes: ChunkBoxes, s: EffectiveSettings, except: Element | null): void {
  if (boxes.size !== except) {
    boxes.size.value = String(s.knowledge_chunking.size);
  }
  if (boxes.overlap !== except) {
    boxes.overlap.value = String(s.knowledge_chunking.overlap);
  }
}

/** Paints every box but the one the reader is in, for retention's reason in settings.ts. */
export function paintKnowledgeTuning(s: EffectiveSettings): void {
  const focused = document.activeElement;
  for (const row of patternInputs) {
    const input = maybeEl<HTMLInputElement>(row.inputID);
    if (input !== null && input !== focused) {
      input.value = joinGlobList(s[row.key]);
    }
  }
  const boxes = chunkBoxes();
  if (boxes !== null) {
    paintChunkBoxes(boxes, s, focused);
  }
}

export function initKnowledgeTuningControls(): void {
  for (const row of patternInputs) {
    const input = maybeEl<HTMLInputElement>(row.inputID);
    input?.addEventListener("change", () => {
      const patterns = splitGlobList(input.value);
      input.value = joinGlobList(patterns);
      saveControl(input, { [row.key]: patterns }, (shown) => {
        input.value = joinGlobList(shown[row.key]);
      });
    });
  }
  const boxes = chunkBoxes();
  if (boxes === null) {
    return;
  }
  // Either box saves both numbers: knowledge_chunking is one value, as kiro-cli's knowledgeChunking
  // row is, because an overlap is only valid against its size.
  for (const input of [boxes.size, boxes.overlap]) {
    const other = input === boxes.size ? boxes.overlap : boxes.size;
    const typingInOther = (): Element | null => (document.activeElement === other ? other : null);
    input.addEventListener("change", () => {
      const chunking = chunkingFrom(boxes.size.value, boxes.overlap.value);
      if (chunking === null) {
        const shown = settingsAsShown();
        if (shown !== null) {
          paintChunkBoxes(boxes, shown, typingInOther());
        }
        return;
      }
      boxes.size.value = String(chunking.size);
      boxes.overlap.value = String(chunking.overlap);
      saveControl(input, { knowledge_chunking: chunking }, (shown) => {
        paintChunkBoxes(boxes, shown, typingInOther());
      });
    });
  }
}

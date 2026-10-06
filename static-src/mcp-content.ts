// Pure extraction of insertable text from MCP prompt/resource results, DOM-free so mcp-ui.ts stays a thin view.

import type { MCPPromptResult, MCPResourceResult, MCPContentBlock } from "./actions/mcp.js";

/** Join the text blocks of one MCP content field (a single block or an array). Non-text blocks are skipped. */
export function blockText(content: MCPContentBlock | MCPContentBlock[] | undefined): string {
  if (content === undefined) {
    return "";
  }
  const blocks = Array.isArray(content) ? content : [content];
  return blocks
    .filter((b) => b.type === undefined || b.type === "text")
    .map((b) => b.text ?? "")
    .filter((t) => t !== "")
    .join("\n");
}

/** Flatten an MCP GetPromptResult's messages into insertable text. */
export function promptResultToText(res: MCPPromptResult): string {
  const parts: string[] = [];
  for (const m of res.messages ?? []) {
    const t = blockText(m.content);
    if (t !== "") {
      parts.push(t);
    }
  }
  return parts.join("\n\n").trim();
}

/** Flatten an MCP ReadResourceResult's text contents (binary blobs skipped). */
export function resourceResultToText(res: MCPResourceResult): string {
  const parts: string[] = [];
  for (const c of res.contents ?? []) {
    if (c.text !== undefined && c.text !== "") {
      parts.push(c.text);
    }
  }
  return parts.join("\n\n").trim();
}

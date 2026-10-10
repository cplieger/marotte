// The editor view exactly as static/index.html declares it, inside the shell chain that gives
// `.editor-body` a definite height, so a geometry assertion measures the shipped markup.
import indexHtml from "../../static/index.html?raw";

/** Mount the editor view (shown) and return its host, which the caller removes. */
export function mountEditorView(): HTMLElement {
  const parsed = new DOMParser().parseFromString(indexHtml, "text/html");
  const view = parsed.getElementById("editor-view");
  if (view === null) {
    throw new Error("static/index.html has no #editor-view");
  }
  const host = document.createElement("div");
  host.id = "app";
  const main = document.createElement("main");
  main.id = "chat-area";
  const imported = document.importNode(view, true);
  imported.classList.remove("hidden");
  main.append(imported);
  host.append(main);
  document.body.append(host);
  return host;
}

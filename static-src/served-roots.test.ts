// Which agent-written links open in the app follows the live workspace root: under a custom work dir
// its files route like `/workspace`'s do by default, and a `/` work dir, which the server never
// grants as a mount, routes nothing beyond the uploads mount.
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { initLinkifyCallbacks } from "./linkify.js";
import { renderMarkdownInto } from "./markdown.js";
import { setWorkspaceRoot, _resetForTest as resetWorkspace } from "./workspace.js";

const opened: [string, number | undefined][] = [];

beforeEach(() => {
  opened.length = 0;
  initLinkifyCallbacks({
    open: (path, line) => {
      opened.push([path, line]);
    },
  });
});

afterEach(() => {
  resetWorkspace();
});

function render(md: string): HTMLElement {
  const host = document.createElement("div");
  renderMarkdownInto(host, md);
  return host;
}

function href(md: string): string | null | undefined {
  return render(md).querySelector("a")?.getAttribute("href");
}

function imgSrc(md: string): string | null | undefined {
  return render(md).querySelector("img")?.getAttribute("src");
}

describe("under a custom work dir", () => {
  it("opens a markdown file link in the editor", () => {
    setWorkspaceRoot("/srv/proj");
    expect(href("[notes](/srv/proj/docs/notes.md)")).toBe("/file//srv/proj/docs/notes.md");
  });

  it("opens a page's #L link at its source line", () => {
    setWorkspaceRoot("/srv/proj");
    expect(href("[demo](/srv/proj/demo/index.html#L7)")).toBe("/file//srv/proj/demo/index.html#L7");
  });

  it("opens a page linked with a query in its Preview tab", () => {
    setWorkspaceRoot("/srv/proj");
    expect(href("[demo](/srv/proj/demo/index.html?v=2)")).toBe("/web/srv/proj/demo/index.html");
  });

  it("serves an inline image through the byte route", () => {
    setWorkspaceRoot("/srv/proj");
    expect(imgSrc("![shot](/srv/proj/out/shot.png)")).toBe(
      "/api/file/download?path=%2Fsrv%2Fproj%2Fout%2Fshot.png",
    );
  });

  it("serves an inline audio file through the byte route", () => {
    setWorkspaceRoot("/srv/proj");
    expect(
      render("![clip](/srv/proj/out/clip.mp3)").querySelector("audio")?.getAttribute("src"),
    ).toBe("/api/file/download?path=%2Fsrv%2Fproj%2Fout%2Fclip.mp3");
  });

  it("no longer routes the default root, which is not a mount there", () => {
    setWorkspaceRoot("/srv/proj");
    expect(href("[notes](/workspace/docs/notes.md)")).toBe("/workspace/docs/notes.md");
    expect(imgSrc("![shot](/workspace/out/shot.png)")).toBe("/workspace/out/shot.png");
  });
});

describe("under a / work dir", () => {
  it("leaves a config-dir markdown link a plain link", () => {
    setWorkspaceRoot("/");
    expect(href("[chat](/config/chats/c1/notes.md)")).toBe("/config/chats/c1/notes.md");
  });

  it("leaves a config-dir page #L link a plain link", () => {
    setWorkspaceRoot("/");
    expect(href("[page](/config/home/demo/index.html#L7)")).toBe("/config/home/demo/index.html#L7");
  });

  it("leaves a config-dir page link with a query a plain link", () => {
    setWorkspaceRoot("/");
    expect(href("[page](/config/home/demo/index.html?v=2)")).toBe(
      "/config/home/demo/index.html?v=2",
    );
  });

  it("leaves a config-dir inline image untouched", () => {
    setWorkspaceRoot("/");
    expect(imgSrc("![shot](/config/home/shot.png)")).toBe("/config/home/shot.png");
  });

  it("still routes the uploads mount", () => {
    setWorkspaceRoot("/");
    expect(href("[note](/uploads/note.md)")).toBe("/file//uploads/note.md");
    expect(imgSrc("![shot](/uploads/shot.png)")).toBe(
      "/api/file/download?path=%2Fuploads%2Fshot.png",
    );
  });

  it("leaves a protocol-relative link pointing off-site", () => {
    setWorkspaceRoot("/");
    expect(href("[doc](//example.com/docs/a.md)")).toBe("//example.com/docs/a.md");
  });
});

// The handshake and the first paint race: a boot snapshot or a chat read can render before the root is known, and
// those links and images were decided against the default root.
describe("rendered before the handshake names a custom work dir", () => {
  it("routes a markdown file link once the root lands", () => {
    const host = render("[notes](/srv/proj/docs/notes.md)");
    expect(host.querySelector("a")?.getAttribute("href")).toBe("/srv/proj/docs/notes.md");
    setWorkspaceRoot("/srv/proj");
    const a = host.querySelector("a");
    expect(a?.getAttribute("href")).toBe("/file//srv/proj/docs/notes.md");
    expect(a?.textContent).toBe("notes");
    a?.click();
    expect(opened).toEqual([["/srv/proj/docs/notes.md", undefined]]);
  });

  it("routes a page's #L link to its source line once the root lands", () => {
    const host = render("[demo](/srv/proj/demo/index.html#L7)");
    setWorkspaceRoot("/srv/proj");
    expect(host.querySelector("a")?.getAttribute("href")).toBe(
      "/file//srv/proj/demo/index.html#L7",
    );
  });

  it("routes a page linked with a query to its Preview tab once the root lands", () => {
    const host = render("[demo](/srv/proj/demo/index.html?v=2)");
    setWorkspaceRoot("/srv/proj");
    expect(host.querySelector("a")?.getAttribute("href")).toBe("/web/srv/proj/demo/index.html");
  });

  it("serves an inline image through the byte route once the root lands", () => {
    const host = render("![shot](/srv/proj/out/shot.png)");
    expect(host.querySelector("img")?.getAttribute("src")).toBe("/srv/proj/out/shot.png");
    setWorkspaceRoot("/srv/proj");
    const img = host.querySelector("img");
    expect(img?.getAttribute("src")).toBe("/api/file/download?path=%2Fsrv%2Fproj%2Fout%2Fshot.png");
    expect(img?.getAttribute("alt")).toBe("shot");
  });

  it("serves an image whose first load already failed", () => {
    const host = render("![shot](/srv/proj/out/shot.png)");
    host.querySelector("img")?.dispatchEvent(new Event("error"));
    expect(host.querySelector(".img-missing")).not.toBeNull();
    setWorkspaceRoot("/srv/proj");
    expect(host.querySelector(".img-missing")).toBeNull();
    expect(host.querySelector("img")?.getAttribute("src")).toBe(
      "/api/file/download?path=%2Fsrv%2Fproj%2Fout%2Fshot.png",
    );
  });

  it("plays an inline audio file once the root lands", () => {
    const host = render("![clip](/srv/proj/out/clip.mp3)");
    setWorkspaceRoot("/srv/proj");
    expect(host.querySelector("img")).toBeNull();
    expect(host.querySelector("audio")?.getAttribute("src")).toBe(
      "/api/file/download?path=%2Fsrv%2Fproj%2Fout%2Fclip.mp3",
    );
  });

  it("turns a page link into its preview card, label and title kept, once the root lands", () => {
    const host = render('[the demo](/srv/proj/demo/index.html "Open it")');
    expect(host.querySelector(".preview-card")).toBeNull();
    setWorkspaceRoot("/srv/proj");
    const card = host.querySelector(".preview-card");
    expect(card?.querySelector(".preview-card-label")?.textContent).toBe("the demo");
    expect(card?.querySelector(".preview-card-folder")?.textContent).toBe("demo");
    expect(card?.getAttribute("data-tooltip")).toBe("Open it");
    expect(host.querySelector("a")).toBeNull();
  });

  it("unroutes the default root's links, which name no mount there", () => {
    const host = render("[notes](/workspace/docs/notes.md) and [demo](/workspace/demo/index.html)");
    expect(host.querySelectorAll(".preview-card")).toHaveLength(1);
    setWorkspaceRoot("/srv/proj");
    expect(host.querySelector(".preview-card")).toBeNull();
    const [notes, demo] = [...host.querySelectorAll("a")];
    expect(notes?.getAttribute("href")).toBe("/workspace/docs/notes.md");
    expect(demo?.getAttribute("href")).toBe("/workspace/demo/index.html");
    expect(demo?.textContent).toBe("demo");
    host.addEventListener("click", (e) => {
      e.preventDefault();
    });
    notes?.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
    expect(opened).toEqual([]);
  });

  it("labels a card painted before the handshake with its folder under the default root", () => {
    // The card is kept when the root lands as the default, so its label is right from the first paint.
    const host = render("[demo](/workspace/demo/index.html)");
    setWorkspaceRoot("/workspace");
    expect(host.querySelector(".preview-card-folder")?.textContent).toBe("demo");
  });

  it("keeps every element when the root is the default", () => {
    const host = render("[notes](/workspace/docs/notes.md) ![shot](/workspace/out/shot.png)");
    const [a, img] = [host.querySelector("a"), host.querySelector("img")];
    setWorkspaceRoot("/workspace");
    expect(host.querySelector("a")).toBe(a);
    expect(host.querySelector("img")).toBe(img);
  });

  it("leaves a relative link alone, which no root decides", () => {
    const host = render("[notes](docs/notes.md)");
    const a = host.querySelector("a");
    setWorkspaceRoot("/srv/proj");
    expect(host.querySelector("a")).toBe(a);
  });
});

// A page stays open across a server restart, so a later handshake can name another work dir.
describe("rendered under one root when a later handshake names another", () => {
  it("routes a markdown file link under the new root, and unroutes it under the next", () => {
    setWorkspaceRoot("/workspace");
    const host = render("[notes](/srv/proj/docs/notes.md)");
    setWorkspaceRoot("/srv/proj");
    expect(host.querySelector("a")?.getAttribute("href")).toBe("/file//srv/proj/docs/notes.md");
    host.querySelector("a")?.click();
    expect(opened).toEqual([["/srv/proj/docs/notes.md", undefined]]);
    setWorkspaceRoot("/srv/other");
    expect(host.querySelector("a")?.getAttribute("href")).toBe("/srv/proj/docs/notes.md");
    expect(host.querySelector("a")?.textContent).toBe("notes");
  });

  it("routes a page's #L link to its source line under the new root", () => {
    setWorkspaceRoot("/workspace");
    const host = render("[demo](/srv/proj/demo/index.html#L7)");
    setWorkspaceRoot("/srv/proj");
    expect(host.querySelector("a")?.getAttribute("href")).toBe(
      "/file//srv/proj/demo/index.html#L7",
    );
  });

  it("routes a page linked with a query to its Preview tab under the new root", () => {
    setWorkspaceRoot("/workspace");
    const host = render("[demo](/srv/proj/demo/index.html?v=2)");
    setWorkspaceRoot("/srv/proj");
    expect(host.querySelector("a")?.getAttribute("href")).toBe("/web/srv/proj/demo/index.html");
  });

  it("serves an inline image through the byte route under the new root, and stops under the next", () => {
    setWorkspaceRoot("/workspace");
    const host = render("![shot](/srv/proj/out/shot.png)");
    setWorkspaceRoot("/srv/proj");
    expect(host.querySelector("img")?.getAttribute("src")).toBe(
      "/api/file/download?path=%2Fsrv%2Fproj%2Fout%2Fshot.png",
    );
    setWorkspaceRoot("/srv/other");
    expect(host.querySelector("img")?.getAttribute("src")).toBe("/srv/proj/out/shot.png");
  });

  it("routes a link again when the root comes back", () => {
    setWorkspaceRoot("/srv/proj");
    const host = render("[notes](/srv/proj/docs/notes.md)");
    setWorkspaceRoot("/workspace");
    expect(host.querySelector("a")?.getAttribute("href")).toBe("/srv/proj/docs/notes.md");
    setWorkspaceRoot("/srv/proj");
    expect(host.querySelector("a")?.getAttribute("href")).toBe("/file//srv/proj/docs/notes.md");
  });

  it("relabels a preview card with its folder under a root nested in the old one", () => {
    setWorkspaceRoot("/srv");
    const host = render("[demo](/srv/proj/demo/index.html)");
    expect(host.querySelector(".preview-card-folder")?.textContent).toBe("proj/demo");
    setWorkspaceRoot("/srv/proj");
    expect(host.querySelector(".preview-card-folder")?.textContent).toBe("demo");
    expect(host.querySelector(".preview-card-label")?.textContent).toBe("demo");
  });

  it("keeps a link whose route the new root leaves unchanged, and still follows it", () => {
    setWorkspaceRoot("/srv");
    const host = render("[notes](/srv/proj/docs/notes.md)");
    const a = host.querySelector("a");
    setWorkspaceRoot("/srv/proj");
    expect(host.querySelector("a")).toBe(a);
    setWorkspaceRoot("/srv/other");
    expect(host.querySelector("a")?.getAttribute("href")).toBe("/srv/proj/docs/notes.md");
  });
});

import { describe, it, expect } from "vitest";
import fileQueryRaw from "../internal/command/testdata/file_query.json?raw";
import {
  carriesContextMention,
  fileQuery,
  hashQuery,
  matchProviders,
  mentionToken,
  mentionable,
  parseTemplate,
  replaceSpan,
  splitFileQuery,
} from "./context-mentions.js";

describe("hashQuery", () => {
  it("opens on a # at a word boundary, before a provider is chosen", () => {
    expect(hashQuery("#", 1)).toEqual({ start: 0, head: "", provider: null, query: "" });
    expect(hashQuery("look at #fi", 11)).toEqual({
      start: 8,
      head: "fi",
      provider: null,
      query: "",
    });
  });

  it("reads the provider and everything after its first colon", () => {
    expect(hashQuery("#file:src/a.go", 14)).toEqual({
      start: 0,
      head: "file",
      provider: "file",
      query: "src/a.go",
    });
    expect(hashQuery("x #mcp:gh:gh://issues/1", 23)?.query).toBe("gh:gh://issues/1");
  });

  it("stays shut inside a word, past the caret's word, and over a written token", () => {
    expect(hashQuery("a#file", 6)).toBeNull();
    expect(hashQuery("#fi rest", 2)).toBeNull();
    expect(hashQuery("see #[[file:a.go]]", 18)).toBeNull();
    expect(hashQuery("#nope:x", 7)).toBeNull();
  });
});

describe("matchProviders", () => {
  it("puts prefix matches first and lists all eight for an empty filter", () => {
    expect(matchProviders("").map((p) => p.id)).toEqual([
      "file",
      "folder",
      "attach",
      "git",
      "terminal",
      "spec",
      "steering",
      "mcp",
    ]);
    expect(matchProviders("s").map((p) => p.id)).toEqual(["spec", "steering", "mcp"]);
  });

  it("offers the Terminal provider and recognises its prefix", () => {
    expect(matchProviders("term").map((p) => p.id)).toEqual(["terminal"]);
    expect(hashQuery("#terminal:50", 12)).toEqual({
      start: 0,
      head: "terminal",
      provider: "terminal",
      query: "50",
    });
  });
});

describe("tokens", () => {
  it("writes the server's grammar", () => {
    expect(mentionToken("file", "src/a.go:3-4")).toBe("#[[file:src/a.go:3-4]]");
    expect(carriesContextMention("see #[[file:src/a.go:3-4]] please")).toBe(true);
    expect(carriesContextMention("#file:a.go and #[[]]")).toBe(false);
  });

  it("refuses to write a query the grammar cannot carry", () => {
    expect(mentionable("a]b.md")).toBe(false);
    expect(mentionable("a\nb")).toBe(false);
    expect(mentionable("a\rb")).toBe(false);
    expect(mentionable("docs/a b.md")).toBe(true);
    expect(mentionToken("mcp", "srv:http://[::1]/x")).toBeNull();
    expect(mentionToken("file", "a\nb")).toBeNull();
  });

  it("does not read a line-broken or malformed token as one, as the server does not", () => {
    expect(carriesContextMention("#[[file:a\nb]]")).toBe(false);
    expect(carriesContextMention("#[[file:a\rb]]")).toBe(false);
    expect(carriesContextMention("#[[File:a]]")).toBe(false);
    expect(carriesContextMention("#[[file:a]")).toBe(false);
  });

  it("replaces only the typed span and puts the caret after the insert", () => {
    expect(replaceSpan("see #fi now", 4, 7, "#[[file:a]] ")).toEqual({
      value: "see #[[file:a]]  now",
      caret: 16,
    });
  });
});

interface FileQueryCase {
  readonly path: string;
  readonly range: string;
  readonly query: string;
}

const FILE_QUERY_CASES = (JSON.parse(fileQueryRaw) as { cases: FileQueryCase[] }).cases;

describe("file queries (internal/command/testdata/file_query.json)", () => {
  it.each(FILE_QUERY_CASES)("writes $path ($range) as $query and reads it back", (c) => {
    expect(fileQuery(c.path, c.range)).toBe(c.query);
    expect(splitFileQuery(c.query)).toEqual({ path: c.path, range: c.range });
  });
});

// RFC 6570 section 3.2's own variables and expected expansions.
const RFC_VALUES: Record<string, string> = {
  var: "value",
  hello: "Hello World!",
  path: "/foo/bar",
  empty: "",
  x: "1024",
  y: "768",
  base: "http://example.com/home/",
  half: "50%",
};

function expand(template: string, values: Record<string, string> = RFC_VALUES): string {
  const t = parseTemplate(template);
  if (t === null) {
    throw new Error(`parseTemplate(${template}) = null`);
  }
  return t.expand(values);
}

describe("RFC 6570 URI templates", () => {
  it.each([
    ["{var}", "value"],
    ["{hello}", "Hello%20World%21"],
    ["{half}", "50%25"],
    ["O{empty}X", "OX"],
    ["O{undef}X", "OX"],
    ["{x,y}", "1024,768"],
    ["{x,hello,y}", "1024,Hello%20World%21,768"],
    ["?{x,empty}", "?1024,"],
    ["{var:3}", "val"],
    ["{var:30}", "value"],
    ["{base}index", "http%3A%2F%2Fexample.com%2Fhome%2Findex"],
    ["{+var}", "value"],
    ["{+hello}", "Hello%20World!"],
    ["{+half}", "50%25"],
    ["{+base}index", "http://example.com/home/index"],
    ["{+path}/here", "/foo/bar/here"],
    ["{+path:6}/here", "/foo/b/here"],
    ["{#var}", "#value"],
    ["{#hello}", "#Hello%20World!"],
    ["{#path:6}/here", "#/foo/b/here"],
    ["X{.var}", "X.value"],
    ["X{.x,y}", "X.1024.768"],
    ["X{.var:3}", "X.val"],
    ["{/var}", "/value"],
    ["{/var,x}/here", "/value/1024/here"],
    ["{/var:1,var}", "/v/value"],
    ["{;x,y}", ";x=1024;y=768"],
    ["{;x,y,empty}", ";x=1024;y=768;empty"],
    ["{;hello:5}", ";hello=Hello"],
    ["{?x,y}", "?x=1024&y=768"],
    ["{?x,y,empty}", "?x=1024&y=768&empty="],
    ["{?var:3}", "?var=val"],
    ["?fixed=yes{&x}", "?fixed=yes&x=1024"],
    ["{&x,y,empty}", "&x=1024&y=768&empty="],
    ["{&var:3}", "&var=val"],
    ["{var*}", "value"],
  ])("expands %s to %s", (template, want) => {
    expect(expand(template)).toBe(want);
  });

  it("encodes the punctuation simple expansion must escape", () => {
    expect(expand("{v}", { v: "!'()*" })).toBe("%21%27%28%29%2A");
    expect(expand("{v}", { v: "é" })).toBe("%C3%A9");
    expect(expand("{+v}", { v: "a%20b" })).toBe("a%20b");
  });

  it("lists each variable once, in order", () => {
    expect(parseTemplate("gh://{owner}/{repo}/issues{?state,owner}{/n*}")?.vars).toEqual([
      "owner",
      "repo",
      "state",
      "n",
    ]);
  });

  it.each([
    "{=x}",
    "{,x}",
    "{!x}",
    "{@x}",
    "{|x}",
    "{x",
    "x}",
    "{}",
    "{x:0}",
    "{x:10000}",
    "{x:3*}",
    "{a b}",
  ])("rejects the malformed or reserved template %s", (template) => {
    expect(parseTemplate(template)).toBeNull();
  });
});

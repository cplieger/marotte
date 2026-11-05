// KAS's `_kiro/search/text_search` and `_kiro/search/find_files`, gated on
// clientCapabilities._meta.kiro.textSearch and findFiles: grep_search and file_search run here,
// through internal/kirosearch, instead of in KAS's rg and Node walker fallback.

package agent

import (
	"context"
	"log/slog"
	"sync"

	"github.com/cplieger/marotte/internal/kirosearch"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/systembin"
)

const (
	methodKiroTextSearch = "_kiro/search/text_search"
	methodKiroFindFiles  = "_kiro/search/find_files"
)

// resolveRipgrep is the rg the searches run, and the seam a test stages one through. The image's
// /usr/bin/rg, never the tools engine's, which an agent can replace.
var resolveRipgrep = func() (string, bool) { return systembin.Resolve("rg") }

var ripgrepMissing sync.Once

// kiroTextSearchParams is text_search's request; KAS 3.0.0 always sends caseSensitive.
type kiroTextSearchParams struct {
	Pattern        string `json:"pattern"`
	IncludePattern string `json:"includePattern"`
	ExcludePattern string `json:"excludePattern"`
	CaseSensitive  bool   `json:"caseSensitive"`
}

// kiroFindFilesParams is find_files's request; exclude is absent unless the tool call set one.
type kiroFindFilesParams struct {
	Pattern    string `json:"pattern"`
	Exclude    string `json:"exclude"`
	MaxResults int    `json:"maxResults"`
}

type kiroTextSearchBody struct {
	Matches []kirosearch.Match `json:"matches"`
}

type kiroFindFilesBody struct {
	Files []string `json:"files"`
}

// handleKiroSearchRequest answers both verbs async under inflight, like the fs verbs: a search can
// take the whole deadline and the per-event ctx ends sooner.
func (in *inbound) handleKiroSearchRequest(_ context.Context, chatID marotte.ChatID, origin acpResponder, msg *marotte.RPCResponse) bool {
	if msg.Method != methodKiroTextSearch && msg.Method != methodKiroFindFiles {
		return false
	}
	in.lifetime.inflight.Go(func() {
		ctx, cancel := in.lifetime.derivedContext()
		defer cancel()
		in.respondKiroSearch(ctx, chatID, origin, msg)
	})
	return true
}

// respondKiroSearch answers with the search's own context, which searchCancels ends; the answer
// still goes out on ctx.
func (in *inbound) respondKiroSearch(ctx context.Context, chatID marotte.ChatID, origin acpResponder, msg *marotte.RPCResponse) {
	searchCtx, release := in.bus.searches.track(ctx, chatID, in.searchRun(chatID, sessionIDOfParams(msg.Params)), origin)
	defer release()
	in.answerKiroSearch(ctx, searchCtx, chatID, origin, msg)
}

// searchRun is the run whose step sent a search from sessionID, "" for the chat's own. A step of a
// chat-parented run sends its requests on the chat's session, so the session id decides, not chatID.
func (in *inbound) searchRun(chatID marotte.ChatID, sessionID marotte.SessionID) string {
	if wf, ok := in.stepRun(sessionID); ok {
		return wf
	}
	return workflowIDOf(chatID)
}

// answerKiroSearch never answers with an error, a malformed request and a panic included: KAS
// 3.0.0 reruns a search whose client answer is an error or lacks its matches or files member with
// its own Node walker, which has no time limit, ignores includePattern while walking and cannot
// be cancelled.
func (in *inbound) answerKiroSearch(ctx, searchCtx context.Context, chatID marotte.ChatID, origin acpResponder, msg *marotte.RPCResponse) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("kiro search handler panic", "chat_id", chatID, "method", msg.Method, "panic", r)
			in.respondBridge(ctx, chatID, origin, msg, emptyKiroSearchBody(msg.Method), nil)
		}
	}()
	in.respondBridge(ctx, chatID, origin, msg, in.kiroSearch(searchCtx, msg), nil)
}

func emptyKiroSearchBody(method string) any {
	if method == methodKiroTextSearch {
		return kiroTextSearchBody{Matches: []kirosearch.Match{}}
	}
	return kiroFindFilesBody{Files: []string{}}
}

// kiroSearch searches the session's roots: every marotte session opens on the workspace with the
// granted additional directories, the same roots lifetime.confine admits.
func (in *inbound) kiroSearch(ctx context.Context, msg *marotte.RPCResponse) any {
	bin, ok := resolveRipgrep()
	if !ok {
		ripgrepMissing.Do(func() {
			slog.Warn("search: ripgrep is not installed in /usr/bin; grep_search and file_search find nothing")
		})
	}
	s := kirosearch.Searcher{Bin: bin}
	roots := append([]string{in.lifetime.workDir}, in.lifetime.grantedDirs(ctx)...)
	if msg.Method == methodKiroTextSearch {
		var p kiroTextSearchParams
		if err := parseRequest(msg, &p); err != nil {
			slog.Warn("search: undecodable text_search request; answering empty", "error", err)
			return emptyKiroSearchBody(msg.Method)
		}
		return kiroTextSearchBody{Matches: s.Text(ctx, roots, kirosearch.TextQuery{
			Pattern: p.Pattern, IncludePattern: p.IncludePattern, ExcludePattern: p.ExcludePattern, CaseSensitive: p.CaseSensitive,
		})}
	}
	var p kiroFindFilesParams
	if err := parseRequest(msg, &p); err != nil {
		slog.Warn("search: undecodable find_files request; answering empty", "error", err)
		return emptyKiroSearchBody(msg.Method)
	}
	return kiroFindFilesBody{Files: s.Files(ctx, roots, kirosearch.FilesQuery{
		Pattern: p.Pattern, Exclude: p.Exclude, MaxResults: p.MaxResults,
	})}
}

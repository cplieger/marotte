package command

// The tab commands validate their payload and hand the operation to the membership coordinator. The
// client-minted op_id is echoed on the event for correlation; it is not an idempotency key.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/tabs"
)

// keyVersion is the response field carrying the collection version a
// mutation committed — one spelling across every tab response, since a
// client keys its gap detection on this field.
const keyVersion = "version"

// errTooManyOrderIDs is reorder_tabs' 413: a list longer than the decode
// bound is refused before it reaches the store.
var errTooManyOrderIDs = errors.New("order names more ids than the store can hold")

// CmdOpenTab opens a tab for something that already exists; it never mints a chat. `created` is
// load-bearing: an already-open (kind, ref) emits no event, so the client resolves on this
// response.
func CmdOpenTab(ctx context.Context, mem *Membership, cmd *marotte.ClientCommand) (any, error) {
	var p marotte.OpenTabCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	if !p.Kind.Valid() || !ValidIdent(p.OpID) || !validTabID(p.Parent) {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	// A chat ref is validated as a chat id here rather than in the store,
	// which treats a ref as opaque text on purpose.
	if p.Kind == marotte.TabKindChat && !ids.ValidChatID(p.Ref) {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	opened, err := mem.OpenTab(ctx, marotte.OpenTab{
		Kind:   p.Kind,
		Ref:    p.Ref,
		Parent: p.Parent,
		Owns:   p.Owns,
	}, p.OpID)
	if err != nil {
		return nil, err
	}
	return responseWith(map[string]any{
		"subject":  opened.Subject,
		"created":  opened.Created,
		keyVersion: opened.Version,
	}), nil
}

// CmdCloseTab closes a tab and its children (one mutation, hence a list). For a chat tab it runs
// the teardown; the record survives. An id that is not open closes nothing and is not an error.
func CmdCloseTab(ctx context.Context, mem *Membership, cmd *marotte.ClientCommand) (any, error) {
	var p marotte.CloseTabCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	if !validTabID(p.ID) || p.ID == "" || !ValidIdent(p.OpID) {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	closed, version, err := mem.CloseTab(ctx, p.ID, p.OpID)
	if err != nil {
		return nil, err
	}
	return responseWith(map[string]any{
		"closed":   subjectIDs(closed),
		keyVersion: version,
	}), nil
}

// CmdReorderTabs replaces the order with the arrangement a drag committed. The exact-set check is
// the whole precondition (no base version, which would discard a valid drag after an unrelated
// pin). A mismatch is a 409: re-list, never re-send.
func CmdReorderTabs(ctx context.Context, mem *Membership, cmd *marotte.ClientCommand) (any, error) {
	var p marotte.ReorderTabsCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	if !ValidIdent(p.OpID) {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	if len(p.Order) > tabs.MaxTabs {
		return nil, StatusError(http.StatusRequestEntityTooLarge, errTooManyOrderIDs)
	}
	for _, id := range p.Order {
		if id == "" || !validTabID(id) {
			return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
		}
	}
	version, err := mem.ReorderTabs(ctx, p.Order, p.OpID)
	if err != nil {
		return nil, err
	}
	return responseWith(map[string]any{keyVersion: version}), nil
}

// CmdPinTab pins or unpins one tab, idempotent in both directions.
func CmdPinTab(ctx context.Context, mem *Membership, cmd *marotte.ClientCommand) (any, error) {
	var p marotte.PinTabCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	if !validTabID(p.ID) || p.ID == "" || !ValidIdent(p.OpID) {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	version, err := mem.SetPinned(ctx, p.ID, p.Pinned, p.OpID)
	if err != nil {
		return nil, err
	}
	return responseWith(map[string]any{keyVersion: version}), nil
}

// CmdReparentTab hangs an open tab under an open chat tab. Idempotent when
// the parent is unchanged. The response carries the subject as it now reads,
// because an unchanged parent emits no event for the client to adopt from.
func CmdReparentTab(ctx context.Context, mem *Membership, cmd *marotte.ClientCommand) (any, error) {
	var p marotte.ReparentTabCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	if !validTabID(p.ID) || p.ID == "" || !validTabID(p.Parent) || p.Parent == "" || !ValidIdent(p.OpID) {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	subject, version, err := mem.Reparent(ctx, p.ID, p.Parent, p.OpID)
	if err != nil {
		return nil, err
	}
	return responseWith(map[string]any{
		"subject":  subject,
		keyVersion: version,
	}), nil
}

// validTabID reports whether s is safe to use as a tab id: hex from the
// store's own minting, or empty where the field is optional.
func validTabID(s string) bool {
	return s == "" || ids.ValidIdent(s)
}

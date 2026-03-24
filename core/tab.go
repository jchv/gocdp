package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jchv/gocdp/cdp"
)

// TabOn is a type-safe helper to register a Tab-scoped event handler.
//
// Because Go does not allow type parameters on methods, this is a top-level
// function rather than a method on Tab.
//
// Usage:
//
//	hid := core.TabOn(tab, func(ev *cdp.PageLoadEventFiredEvent) {
//	    fmt.Println("page loaded at", ev.Timestamp)
//	})
//	defer tab.RemoveHandler(hid)
func TabOn[E any, PE interface {
	*E
	Event
}](t *Tab, cb func(event PE)) HandlerID {
	var zero E
	name := PE(&zero).CDPEventName()

	return t.Conn.AddHandler(name, func(sessionID string, params json.RawMessage) {
		if sessionID != t.SessionID {
			return
		}
		var ev E
		if err := json.Unmarshal(params, &ev); err != nil {
			slog.Error("failed to decode CDP event", slog.String("event", name), slog.Any("err", err))
			return
		}
		cb(&ev)
	})
}

// Tab represents a single browser tab (page target) attached via a CDP session.
type Tab struct {
	TargetID  string
	SessionID string
	Conn      *Connection

	// Track which CDP domains have been enabled for this session so we don't
	// re-enable them on every call.
	enabledMu      sync.Mutex
	enabledDomains map[string]bool
}

// ensureDomain enables a CDP domain exactly once per tab lifetime.
func (t *Tab) ensureDomain(ctx context.Context, name string, cmd Command) error {
	t.enabledMu.Lock()
	if t.enabledDomains == nil {
		t.enabledDomains = make(map[string]bool)
	}
	already := t.enabledDomains[name]
	t.enabledMu.Unlock()

	if already {
		return nil
	}

	if err := t.Conn.Send(ctx, cmd, t.SessionID, nil); err != nil {
		return err
	}

	t.enabledMu.Lock()
	t.enabledDomains[name] = true
	t.enabledMu.Unlock()
	return nil
}

// ctxSleep sleeps for the given duration but returns early if the context is
// cancelled.
func ctxSleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// Navigate navigates to the specified URL and waits for the page load event.
func (t *Tab) Navigate(ctx context.Context, url string) error {
	// Enable necessary domains (idempotent – only sent once per tab).
	if err := t.ensureDomain(ctx, "DOM", cdp.DOMEnable()); err != nil {
		return fmt.Errorf("dom enable failed: %w", err)
	}
	if err := t.ensureDomain(ctx, "Page", cdp.PageEnable()); err != nil {
		return fmt.Errorf("page enable failed: %w", err)
	}

	// Register a one-shot handler for Page.loadEventFired *before* we send
	// the navigate command so we can't miss the event.
	loadCh := make(chan struct{}, 1)
	hid := TabOn[cdp.PageLoadEventFiredEvent](t, func(_ *cdp.PageLoadEventFiredEvent) {
		select {
		case loadCh <- struct{}{}:
		default:
		}
	})
	defer t.Conn.RemoveHandler(hid)

	var ret cdp.PageNavigateReturns
	err := t.Conn.Send(ctx, cdp.PageNavigate(url), t.SessionID, &ret)
	if err != nil {
		return fmt.Errorf("navigate failed: %w", err)
	}
	if ret.ErrorText != nil && *ret.ErrorText != "" {
		return fmt.Errorf("navigate error: %s", *ret.ErrorText)
	}

	// Wait for the load event, respecting the caller's context timeout.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-loadCh:
		return nil
	}
}

// GetDocument fetches the root DOM node
func (t *Tab) GetDocument(ctx context.Context) (*cdp.DOMNode, error) {
	var ret cdp.DOMGetDocumentReturns
	err := t.Conn.Send(ctx, cdp.DOMGetDocument().WithDepth(-1).WithPierce(true), t.SessionID, &ret)
	if err != nil {
		return nil, fmt.Errorf("get document: %w", err)
	}
	return &ret.Root, nil
}

// findNodeInTree recursively searches for a node ID in the tree
func findNodeInTree(root *cdp.DOMNode, id cdp.DOMNodeId) *cdp.DOMNode {
	if root.NodeId == id {
		return root
	}
	for i := range root.Children {
		if match := findNodeInTree(&root.Children[i], id); match != nil {
			return match
		}
	}
	return nil
}

// Select queries a single element by CSS selector
func (t *Tab) Select(ctx context.Context, selector string) (*Element, error) {
	doc, err := t.GetDocument(ctx)
	if err != nil {
		return nil, err
	}

	var ret cdp.DOMQuerySelectorReturns
	err = t.Conn.Send(ctx, cdp.DOMQuerySelector(doc.NodeId, selector), t.SessionID, &ret)
	if err != nil {
		return nil, fmt.Errorf("query selector: %w", err)
	}
	if ret.NodeId == 0 {
		return nil, fmt.Errorf("element not found: %s", selector)
	}

	node := findNodeInTree(doc, ret.NodeId)
	if node == nil {
		return nil, fmt.Errorf("could not resolve node matching id")
	}

	return &Element{Tab: t, Node: node}, nil
}

// Find searches the DOM for the given text/XPath and returns the first match.
func (t *Tab) Find(ctx context.Context, text string) (*Element, error) {
	doc, err := t.GetDocument(ctx)
	if err != nil {
		return nil, err
	}

	var ret cdp.DOMPerformSearchReturns
	err = t.Conn.Send(ctx, cdp.DOMPerformSearch(text), t.SessionID, &ret)
	if err != nil {
		return nil, fmt.Errorf("perform search: %w", err)
	}

	// Always discard the search session when we're done.
	defer func() {
		_ = t.Conn.Send(ctx, cdp.DOMDiscardSearchResults(ret.SearchId), t.SessionID, nil)
	}()

	if ret.ResultCount == 0 {
		return nil, fmt.Errorf("text not found: %q", text)
	}

	var searchRet cdp.DOMGetSearchResultsReturns
	err = t.Conn.Send(ctx, cdp.DOMGetSearchResults(ret.SearchId, 0, 1), t.SessionID, &searchRet)
	if err != nil {
		return nil, fmt.Errorf("get search results: %w", err)
	}
	if len(searchRet.NodeIds) == 0 {
		return nil, fmt.Errorf("text not found: %q", text)
	}

	node := findNodeInTree(doc, searchRet.NodeIds[0])
	if node == nil {
		return nil, fmt.Errorf("could not resolve search node in tree")
	}

	return &Element{Tab: t, Node: node}, nil
}

// SelectAll queries multiple elements by CSS selector
func (t *Tab) SelectAll(ctx context.Context, selector string) ([]*Element, error) {
	doc, err := t.GetDocument(ctx)
	if err != nil {
		return nil, err
	}

	var ret cdp.DOMQuerySelectorAllReturns
	err = t.Conn.Send(ctx, cdp.DOMQuerySelectorAll(doc.NodeId, selector), t.SessionID, &ret)
	if err != nil {
		return nil, fmt.Errorf("query selector all: %w", err)
	}

	var elements []*Element
	for _, id := range ret.NodeIds {
		if node := findNodeInTree(doc, id); node != nil {
			elements = append(elements, &Element{Tab: t, Node: node})
		}
	}
	return elements, nil
}

// MouseClick dispatches a left mouse press and release at the given coordinates.
func (t *Tab) MouseClick(ctx context.Context, x, y float64) error {
	err := t.Conn.Send(ctx, cdp.InputDispatchMouseEvent("mousePressed", x, y).
		WithButton("left").WithClickCount(1), t.SessionID, nil)
	if err != nil {
		return err
	}

	if err := ctxSleep(ctx, 50*time.Millisecond); err != nil {
		return err
	}

	return t.Conn.Send(ctx, cdp.InputDispatchMouseEvent("mouseReleased", x, y).
		WithButton("left").WithClickCount(1), t.SessionID, nil)
}

// MouseDrag performs a mouse drag from (startX, startY) to (endX, endY),
// interpolating the movement over the given number of steps.
func (t *Tab) MouseDrag(ctx context.Context, startX, startY, endX, endY float64, steps int) error {
	if steps < 1 {
		steps = 1
	}

	// Mouse Pressed
	err := t.Conn.Send(ctx, cdp.InputDispatchMouseEvent("mousePressed", startX, startY).
		WithButton("left").WithClickCount(1), t.SessionID, nil)
	if err != nil {
		return err
	}

	// Move in steps
	stepX := (endX - startX) / float64(steps)
	stepY := (endY - startY) / float64(steps)

	for i := 1; i <= steps; i++ {
		curX := startX + stepX*float64(i)
		curY := startY + stepY*float64(i)
		if err := t.Conn.Send(ctx, cdp.InputDispatchMouseEvent("mouseMoved", curX, curY).WithButton("left"), t.SessionID, nil); err != nil {
			return err
		}
		if err := ctxSleep(ctx, 10*time.Millisecond); err != nil {
			return err
		}
	}

	// Mouse Released
	return t.Conn.Send(ctx, cdp.InputDispatchMouseEvent("mouseReleased", endX, endY).
		WithButton("left").WithClickCount(1), t.SessionID, nil)
}

// Evaluate executes a JavaScript expression in the tab and returns the result.
func (t *Tab) Evaluate(ctx context.Context, expression string) (*cdp.RuntimeRemoteObject, error) {
	// Enable Runtime domain once per tab.
	if err := t.ensureDomain(ctx, "Runtime", cdp.RuntimeEnable()); err != nil {
		return nil, fmt.Errorf("runtime enable failed: %w", err)
	}

	var ret cdp.RuntimeEvaluateReturns
	err := t.Conn.Send(ctx, cdp.RuntimeEvaluate(expression).WithAwaitPromise(true).WithReturnByValue(true), t.SessionID, &ret)
	if err != nil {
		return nil, err
	}
	if ret.ExceptionDetails != nil {
		return nil, fmt.Errorf("js exception: %v", ret.ExceptionDetails.Exception.Description)
	}
	return &ret.Result, nil
}

// ScrollDown scrolls the page downward by y pixels.
func (t *Tab) ScrollDown(ctx context.Context, y int) error {
	_, err := t.Evaluate(ctx, fmt.Sprintf("window.scrollBy(0, %d)", y))
	return err
}

// ScrollUp scrolls the page upward by y pixels.
func (t *Tab) ScrollUp(ctx context.Context, y int) error {
	_, err := t.Evaluate(ctx, fmt.Sprintf("window.scrollBy(0, -%d)", y))
	return err
}

// Back navigates the tab to the previous history entry.
func (t *Tab) Back(ctx context.Context) error {
	_, err := t.Evaluate(ctx, "window.history.back()")
	return err
}

// Activate brings this tab to the foreground in the browser window.
func (t *Tab) Activate(ctx context.Context) error {
	return t.Conn.Send(ctx, cdp.TargetActivateTarget(cdp.TargetTargetID(t.TargetID)), "", nil)
}

// Close closes this tab's target in the browser.
func (t *Tab) Close(ctx context.Context) error {
	return t.Conn.Send(ctx, cdp.TargetCloseTarget(cdp.TargetTargetID(t.TargetID)), "", nil)
}

// AddHandler registers a CDP event handler scoped to this tab's session.
// It returns a HandlerID that can be used with RemoveHandler to unregister it.
func (t *Tab) AddHandler(eventName string, cb func(json.RawMessage)) HandlerID {
	return t.Conn.AddHandler(eventName, func(sessionID string, params json.RawMessage) {
		if sessionID == t.SessionID {
			cb(params)
		}
	})
}

// RemoveHandler unregisters a previously added handler by its HandlerID.
func (t *Tab) RemoveHandler(id HandlerID) {
	t.Conn.RemoveHandler(id)
}

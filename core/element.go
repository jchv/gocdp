package core

import (
	"context"
	"fmt"
	"time"

	"github.com/jchv/gocdp/cdp"
)

// Element wraps a DOM node and provides high-level interaction methods.
type Element struct {
	Tab  *Tab
	Node *cdp.DOMNode
}

// Position represents a 2D coordinate on the page.
type Position struct {
	X float64
	Y float64
}

// GetPosition retrieves the center coordinate of the element
func (e *Element) GetPosition(ctx context.Context) (Position, error) {
	var ret cdp.DOMGetContentQuadsReturns
	err := e.Tab.Conn.Send(ctx, cdp.DOMGetContentQuads().WithBackendNodeId(e.Node.BackendNodeId), e.Tab.SessionID, &ret)
	if err != nil {
		return Position{}, fmt.Errorf("get content quads: %w", err)
	}

	if len(ret.Quads) == 0 {
		return Position{}, fmt.Errorf("no quads found for element")
	}

	quad := ret.Quads[0]
	if len(quad) < 8 {
		return Position{}, fmt.Errorf("invalid quad length")
	}

	// quad = [x1, y1, x2, y2, x3, y3, x4, y4]
	// Center is bounding box center
	minX := quad[0]
	maxX := quad[0]
	minY := quad[1]
	maxY := quad[1]

	for i := 2; i < 8; i += 2 {
		x := quad[i]
		y := quad[i+1]
		if x < minX {
			minX = x
		}
		if x > maxX {
			maxX = x
		}
		if y < minY {
			minY = y
		}
		if y > maxY {
			maxY = y
		}
	}

	centerX := minX + (maxX-minX)/2
	centerY := minY + (maxY-minY)/2

	return Position{X: centerX, Y: centerY}, nil
}

// Click triggers a mouse click on the element
func (e *Element) Click(ctx context.Context) error {
	pos, err := e.GetPosition(ctx)
	if err != nil {
		return err
	}
	return e.Tab.MouseClick(ctx, pos.X, pos.Y)
}

// MouseDrag drags this element to the destination element
func (e *Element) MouseDrag(ctx context.Context, dest *Element, steps int) error {
	startPos, err := e.GetPosition(ctx)
	if err != nil {
		return fmt.Errorf("get start pos: %w", err)
	}

	endPos, err := dest.GetPosition(ctx)
	if err != nil {
		return fmt.Errorf("get end pos: %w", err)
	}

	return e.Tab.MouseDrag(ctx, startPos.X, startPos.Y, endPos.X, endPos.Y, steps)
}

// SendKeys focuses the element and types the given text one character at a time.
func (e *Element) SendKeys(ctx context.Context, text string) error {
	err := e.Tab.Conn.Send(ctx, cdp.DOMFocus().WithBackendNodeId(e.Node.BackendNodeId), e.Tab.SessionID, nil)
	if err != nil {
		return fmt.Errorf("focus error: %w", err)
	}

	for _, char := range text {
		err = e.Tab.Conn.Send(ctx, cdp.InputDispatchKeyEvent("char").WithText(string(char)), e.Tab.SessionID, nil)
		if err != nil {
			return err
		}
		if err := ctxSleep(ctx, 10*time.Millisecond); err != nil {
			return err
		}
	}
	return nil
}

// Apply resolves the element to a JS object and calls jsFunction with it.
func (e *Element) Apply(ctx context.Context, jsFunction string) (*cdp.RuntimeRemoteObject, error) {
	var resolveRet cdp.DOMResolveNodeReturns
	err := e.Tab.Conn.Send(ctx, cdp.DOMResolveNode().WithBackendNodeId(e.Node.BackendNodeId), e.Tab.SessionID, &resolveRet)
	if err != nil {
		return nil, fmt.Errorf("resolve node: %w", err)
	}

	if resolveRet.Object.ObjectId == nil {
		return nil, fmt.Errorf("resolved node has no object ID")
	}

	var callRet cdp.RuntimeCallFunctionOnReturns
	err = e.Tab.Conn.Send(ctx, cdp.RuntimeCallFunctionOn(jsFunction).
		WithObjectId(*resolveRet.Object.ObjectId).
		WithArguments([]cdp.RuntimeCallArgument{
			{ObjectId: resolveRet.Object.ObjectId},
		}).
		WithAwaitPromise(true).
		WithReturnByValue(true).
		WithUserGesture(true), e.Tab.SessionID, &callRet)
	if err != nil {
		return nil, fmt.Errorf("call function on: %w", err)
	}

	if callRet.ExceptionDetails != nil {
		desc := "unknown"
		if callRet.ExceptionDetails.Exception.Description != nil {
			desc = *callRet.ExceptionDetails.Exception.Description
		}
		return nil, fmt.Errorf("js exception: %s", desc)
	}

	return &callRet.Result, nil
}

// ClearInput sets the element's value property to an empty string.
func (e *Element) ClearInput(ctx context.Context) error {
	_, err := e.Apply(ctx, `function(element) { element.value = "" }`)
	return err
}

// Focus gives keyboard focus to the element.
func (e *Element) Focus(ctx context.Context) error {
	_, err := e.Apply(ctx, `function(element) { element.focus() }`)
	return err
}

// ScrollIntoView scrolls the element into the visible area if not already visible.
func (e *Element) ScrollIntoView(ctx context.Context) error {
	return e.Tab.Conn.Send(ctx, cdp.DOMScrollIntoViewIfNeeded().WithBackendNodeId(e.Node.BackendNodeId), e.Tab.SessionID, nil)
}

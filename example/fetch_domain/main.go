// Port of the fetch_domain.py example from nodriver.

package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"time"

	"github.com/jchv/gocdp/cdp"
	"github.com/jchv/gocdp/core"
)

func main() {
	opts := []core.Option{core.WithFlags(flag.CommandLine)}
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	browser, err := core.Start(ctx, opts...)
	if err != nil {
		slog.Error("error starting browser", slog.Any("err", err))
		os.Exit(1)
	}
	defer func() { _ = browser.Close() }()

	for range 5 {
		_, err := browser.NewTab(ctx, "https://www.google.com")
		if err != nil {
			slog.Error("error creating tab", slog.Any("err", err))
			os.Exit(1)
		}
	}

	for _, tab := range browser.Tabs {
		core.TabOn(tab, func(ev *cdp.FetchRequestPausedEvent) {
			slog.Info("RequestPaused handler", slog.String("url", ev.Request.Url))
			_ = tab.Conn.Send(ctx, cdp.FetchContinueRequest(ev.RequestId), tab.SessionID, nil)
		})
		_ = tab.Conn.Send(ctx, cdp.FetchEnable(), tab.SessionID, nil)
	}

	time.Sleep(2 * time.Second)

	for _, tab := range browser.Tabs {
		_ = tab.Activate(ctx)
		time.Sleep(500 * time.Millisecond)
	}

	for i := len(browser.Tabs) - 1; i >= 0; i-- {
		tab := browser.Tabs[i]
		_ = tab.Activate(ctx)
		_ = tab.Close(ctx)
		time.Sleep(500 * time.Millisecond)
	}

	time.Sleep(1 * time.Second)
}

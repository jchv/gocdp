// Port of the network_response_body.py example from nodriver.

package main

import (
	"context"
	"encoding/base64"
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
		slog.Error("Error starting browser", slog.Any("err", err))
		os.Exit(1)
	}
	defer func() { _ = browser.Close() }()

	if len(browser.Tabs) == 0 {
		slog.Error("No tabs found")
		os.Exit(1)
	}
	tab := browser.Tabs[0]

	core.TabOn(tab, func(ev *cdp.NetworkResponseReceivedEvent) {
		var ret cdp.NetworkGetResponseBodyReturns
		err := tab.Conn.Send(ctx, cdp.NetworkGetResponseBody(ev.RequestId), tab.SessionID, &ret)
		if err == nil {
			var body []byte
			if ret.Base64Encoded {
				body, _ = base64.StdEncoding.DecodeString(ret.Body)
			} else {
				body = []byte(ret.Body)
			}
			if len(body) > 100 {
				body = body[:100]
			}
			slog.Info("Response body", slog.String("url", ev.Response.Url), slog.String("body", string(body)))
		}
	})

	_ = tab.Conn.Send(ctx, cdp.NetworkEnable(), tab.SessionID, nil)

	for range 2 {
		_ = tab.Navigate(ctx, "https://google.com")
		time.Sleep(5 * time.Second)
	}
}

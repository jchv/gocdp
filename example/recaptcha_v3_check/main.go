package main

import (
	"context"
	"flag"
	"log/slog"
	"math/rand"
	"os"
	"time"

	"github.com/jchv/gocdp/core"
)

func main() {
	opts := []core.Option{core.WithFlags(flag.CommandLine)}
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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

	slog.Info("Navigating to recaptcha v3 test page...")
	if err := tab.Navigate(ctx, "https://2captcha.com/demo/recaptcha-v3"); err != nil {
		slog.Error("Error navigating", slog.Any("err", err))
		os.Exit(1)
	}

	// Wait a slightly random amount of time.
	time.Sleep(time.Duration(rand.Intn(1000))*time.Millisecond + 3*time.Second)

	_ = tab.ScrollDown(ctx, rand.Intn(100))

	time.Sleep(1 * time.Second)

	btn, err := tab.Select(ctx, "[data-action='demo_action']")
	if err != nil {
		slog.Error("Error selecting button", slog.Any("err", err))
		os.Exit(1)
	}
	_ = btn.Click(ctx)

	slog.Info("Done! Waiting 2 seconds.")
	time.Sleep(2 * time.Second)

	results, err := tab.Select(ctx, "code")
	if err != nil {
		slog.Error("Error selecting results", slog.Any("err", err))
		os.Exit(1)
	}

	text, err := results.Apply(ctx, "function(element) { return element.innerText; }")
	if err != nil {
		slog.Error("Error applying function", slog.Any("err", err))
		os.Exit(1)
	}
	slog.Info("Results:", slog.Any("value", text.Value))
}

package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"time"

	"github.com/jchv/gocdp/core"
)

func main() {
	sslKeyLogFile, err := os.CreateTemp("", "sslkeylog-*.txt")
	if err != nil {
		slog.Error("Error creating SSL key log file", slog.Any("err", err))
		os.Exit(1)
	}
	sslKeyLogFile.Close()

	slog.Info("Created SSL key log file", slog.String("name", sslKeyLogFile.Name()))

	opts := []core.Option{
		core.WithFlags(flag.CommandLine),
		core.WithEnv([]string{
			"SSLKEYLOGFILE=" + sslKeyLogFile.Name(),
		}),
	}
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

	_ = tab.Navigate(ctx, "https://google.com")
	time.Sleep(5 * time.Second)

	if err := browser.Close(); err != nil {
		slog.Error("Error closing browser", slog.Any("err", err))
		os.Exit(1)
	}
}

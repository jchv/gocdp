// Port of the mouse_drag_boxes.py example from nodriver.

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

	slog.Info("Navigating to mouse.html...")
	if err := tab.Navigate(ctx, "https://nowsecure.nl/mouse.html?boxes=50"); err != nil {
		slog.Error("error navigating", slog.Any("err", err))
		os.Exit(1)
	}

	time.Sleep(1 * time.Second)

	slog.Info("Selecting boxes...")
	boxes, err := tab.SelectAll(ctx, ".box")
	if err != nil {
		slog.Error("Error selecting boxes", slog.Any("err", err))
		os.Exit(1)
	}
	if len(boxes) == 0 {
		slog.Error("No boxes found!")
		os.Exit(1)
	}
	slog.Info("Found boxes", slog.Int("count", len(boxes)))

	area, err := tab.Select(ctx, ".area-a")
	if err != nil {
		slog.Error("Error selecting area", slog.Any("err", err))
		os.Exit(1)
	}

	for i, box := range boxes {
		slog.Info("Dragging box", slog.Int("index", i+1))
		if err := box.MouseDrag(ctx, area, 20); err != nil {
			slog.Error("Error dragging box", slog.Any("err", err))
			os.Exit(1)
		}
	}
	slog.Info("Done! Waiting 2 seconds.")
	time.Sleep(2 * time.Second)
}

package core

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/jchv/gocdp/cdp"
)

// Browser manages the lifecycle of the browser process and the CDP connection
type Browser struct {
	cmd              *exec.Cmd
	Conn             *Connection
	WebSocketAddress string
	Tabs             []*Tab
	Config           *Config
	tempProfileDir   string
	mu               sync.Mutex
}

// FindChromeExecutable automatically locates a version of Chrome or Chromium.
// This is the exact same logic as nodriver, basically.
func FindChromeExecutable() (string, error) {
	var candidates []string

	if runtime.GOOS == "windows" {
		for _, env := range []string{"PROGRAMFILES", "PROGRAMFILES(X86)", "LOCALAPPDATA", "PROGRAMW6432"} {
			if path := os.Getenv(env); path != "" {
				for _, subitem := range []string{
					"Google/Chrome/Application/chrome.exe",
					"Google/Chrome Beta/Application/chrome.exe",
					"Google/Chrome Canary/Application/chrome.exe",
				} {
					candidates = append(candidates, filepath.Join(path, subitem))
				}
			}
		}
	} else {
		for p := range strings.SplitSeq(os.Getenv("PATH"), string(os.PathListSeparator)) {
			for _, subitem := range []string{
				"google-chrome",
				"chromium",
				"chromium-browser",
				"chrome",
				"google-chrome-stable",
			} {
				candidates = append(candidates, filepath.Join(p, subitem))
			}
		}

		if runtime.GOOS == "darwin" {
			candidates = append(candidates,
				"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
				"/Applications/Chromium.app/Contents/MacOS/Chromium",
			)
		}
	}

	for _, cand := range candidates {
		if stat, err := os.Stat(cand); err == nil && !stat.IsDir() {
			if runtime.GOOS == "windows" {
				return cand, nil
			} else if stat.Mode()&0111 != 0 {
				return cand, nil
			}
		}
	}

	return "", fmt.Errorf("chrome executable not found")
}

// Start launches a new browser instance and establishes a CDP connection
func Start(ctx context.Context, opts ...Option) (*Browser, error) {
	config := &Config{}
	config.SetDefaults()
	for _, opt := range opts {
		if err := opt.Apply(config); err != nil {
			return nil, err
		}
	}

	var cmd *exec.Cmd
	var wsURL string
	var tmpDir string

	if config.Port != 0 {
		host := config.Host
		if host == "" {
			host = "127.0.0.1"
		}

		reqURL := fmt.Sprintf("http://%s:%d/json/version", host, config.Port)
		resp, err := http.Get(reqURL)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to existing browser: %w", err)
		}
		defer func() {
			_ = resp.Body.Close()
		}()

		var version struct {
			WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&version); err != nil {
			return nil, fmt.Errorf("failed to parse /json/version: %w", err)
		}
		wsURL = version.WebSocketDebuggerURL
		if wsURL == "" {
			return nil, fmt.Errorf("no websocket url returned from browser")
		}
	} else {
		execPath := config.BrowserExecutablePath
		var err error
		if execPath == "" {
			execPath, err = FindChromeExecutable()
			if err != nil {
				return nil, err
			}
		}

		args := []string{
			"--remote-debugging-port=0", // pick free port
		}

		if config.Headless {
			args = append(args, "--headless=new")
		}
		if !config.Sandbox {
			args = append(args, "--no-sandbox")
		}

		if config.UserDataDir != "" {
			args = append(args, "--user-data-dir="+config.UserDataDir)
		} else if config.TempProfile {
			tmpDir, err = os.MkdirTemp("", "gocdp_*")
			if err != nil {
				return nil, fmt.Errorf("failed to create temp profile dir: %w", err)
			}
			args = append(args, "--user-data-dir="+tmpDir)
		}

		if config.Lang != "" {
			args = append(args, "--lang="+config.Lang)
		}
		if len(config.BrowserArgs) > 0 {
			args = append(args, config.BrowserArgs...)
		}

		if len(config.BrowserWrapperArgs) > 0 {
			wrapperArgs := []string{}
			wrapperArgs = append(wrapperArgs, config.BrowserWrapperArgs[1:]...)
			wrapperArgs = append(wrapperArgs, execPath)
			wrapperArgs = append(wrapperArgs, args...)
			cmd = exec.CommandContext(ctx, config.BrowserWrapperArgs[0], wrapperArgs...)
		} else {
			cmd = exec.CommandContext(ctx, execPath, args...)
		}

		stderr, err := cmd.StderrPipe()
		if err != nil {
			return nil, err
		}

		if config.Env != nil {
			cmd.Env = config.Env
		}

		if err := cmd.Start(); err != nil {
			return nil, err
		}

		// Read stderr to find the websocket URL
		scanner := bufio.NewScanner(stderr)
		wsChan := make(chan string, 1)

		go func() {
			for scanner.Scan() {
				line := scanner.Text()
				if line != "" {
					slog.Info("[chrome]", slog.String("line", line))
				}
				if after, ok := strings.CutPrefix(line, "DevTools listening on "); ok {
					wsChan <- after
					break
				}
			}
			// Continue reading indefinitely so buffer doesn't fill
			for scanner.Scan() {
			}
		}()

		select {
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			if tmpDir != "" {
				_ = os.RemoveAll(tmpDir)
			}
			return nil, ctx.Err()
		case url := <-wsChan:
			wsURL = url
		}

		if wsURL == "" {
			_ = cmd.Process.Kill()
			if tmpDir != "" {
				_ = os.RemoveAll(tmpDir)
			}
			return nil, fmt.Errorf("failed to get websocket url")
		}
	}

	b := &Browser{
		cmd:              cmd,
		Config:           config,
		WebSocketAddress: wsURL,
		tempProfileDir:   tmpDir,
	}

	conn, err := NewConnection(wsURL)
	if err != nil {
		_ = b.Close()
		return nil, err
	}
	b.Conn = conn

	// Enable target discovery
	err = conn.Send(ctx, cdp.TargetSetDiscoverTargets(true), "", nil)
	if err != nil {
		_ = b.Close()
		return nil, fmt.Errorf("set discover targets: %w", err)
	}

	// Wait briefly for target initialization
	var targetsRet cdp.TargetGetTargetsReturns
	err = conn.Send(ctx, cdp.TargetGetTargets(), "", &targetsRet)
	if err != nil {
		_ = b.Close()
		return nil, fmt.Errorf("get targets: %w", err)
	}

	var firstTarget string
	for _, t := range targetsRet.TargetInfos {
		if t.Type == "page" {
			firstTarget = string(t.TargetId)
			break
		}
	}

	if firstTarget == "" {
		_ = b.Close()
		return nil, fmt.Errorf("no page target found initially")
	}

	var attachRet cdp.TargetAttachToTargetReturns
	err = conn.Send(ctx, cdp.TargetAttachToTarget(cdp.TargetTargetID(firstTarget)).WithFlatten(true), "", &attachRet)
	if err != nil {
		_ = b.Close()
		return nil, fmt.Errorf("attach to target: %w", err)
	}

	b.Tabs = append(b.Tabs, &Tab{
		TargetID:  firstTarget,
		SessionID: string(attachRet.SessionId),
		Conn:      conn,
	})

	return b, nil
}

// NewTab opens a new tab with the given URL.
func (b *Browser) NewTab(ctx context.Context, url string) (*Tab, error) {
	var ret cdp.TargetCreateTargetReturns
	err := b.Conn.Send(ctx, cdp.TargetCreateTarget(url), "", &ret)
	if err != nil {
		return nil, fmt.Errorf("create target: %w", err)
	}

	var attachRet cdp.TargetAttachToTargetReturns
	err = b.Conn.Send(ctx, cdp.TargetAttachToTarget(ret.TargetId).WithFlatten(true), "", &attachRet)
	if err != nil {
		return nil, fmt.Errorf("attach: %w", err)
	}

	tab := &Tab{
		TargetID:  string(ret.TargetId),
		SessionID: string(attachRet.SessionId),
		Conn:      b.Conn,
	}

	b.mu.Lock()
	b.Tabs = append(b.Tabs, tab)
	b.mu.Unlock()

	return tab, nil
}

// Close shuts down the browser. It first attempts a graceful CDP-level close,
// then falls back to killing the process.
func (b *Browser) Close() error {
	var finalErr error

	// Attempt graceful CDP shutdown before killing the process.
	if b.Conn != nil {
		gracefulCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := b.Conn.Send(gracefulCtx, cdp.BrowserClose(), "", nil)
		cancel()

		if err == nil && b.cmd != nil && b.cmd.Process != nil {
			// Give the process a moment to exit on its own after Browser.close.
			done := make(chan error, 1)
			go func() { done <- b.cmd.Wait() }()

			select {
			case <-done:
				// Process exited gracefully; skip Kill below.
				b.cmd = nil
			case <-time.After(5 * time.Second):
				// Timed out waiting; will fall through to Kill.
			}
		}

		if err := b.Conn.Close(); err != nil {
			finalErr = err
		}
	}

	if b.cmd != nil && b.cmd.Process != nil {
		if err := b.cmd.Process.Kill(); err != nil && finalErr == nil {
			finalErr = err
		}
		_ = b.cmd.Wait() // Wait for process to terminate before removing temporary profile
	}

	if b.tempProfileDir != "" {
		_ = os.RemoveAll(b.tempProfileDir)
	}

	// Clear tab references so stale objects aren't held.
	b.mu.Lock()
	b.Tabs = nil
	b.mu.Unlock()

	return finalErr
}

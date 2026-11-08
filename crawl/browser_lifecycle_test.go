package crawl

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mxschmitt/playwright-go"
)

type fakeBrowser struct {
	playwright.Browser
	connected atomic.Bool
	closes    atomic.Int32
}

func (b *fakeBrowser) IsConnected() bool { return b.connected.Load() }
func (b *fakeBrowser) Close(options ...playwright.BrowserCloseOptions) error {
	b.connected.Store(false)
	b.closes.Add(1)
	return nil
}

func TestBrowserLifecycleRetriesErrorsAndDisconnectedInstances(t *testing.T) {
	Shutdown()
	t.Cleanup(Shutdown)
	var attempts atomic.Int32
	first, second := &fakeBrowser{}, &fakeBrowser{}
	initialize := func() (*playwright.Playwright, playwright.Browser, *browserProxy, error) {
		switch attempts.Add(1) {
		case 1:
			return nil, nil, nil, errors.New("launch failed")
		case 2:
			first.connected.Store(true)
			return nil, first, nil, nil
		default:
			second.connected.Store(true)
			return nil, second, nil, nil
		}
	}
	if err := initBrowserWith(context.Background(), initialize); err == nil {
		t.Fatal("failed launch was accepted")
	}
	if err := initBrowserWith(context.Background(), initialize); err != nil {
		t.Fatal("failed initialization was sticky:", err)
	}
	first.connected.Store(false)
	if err := initBrowserWith(context.Background(), initialize); err != nil || attempts.Load() != 3 || first.closes.Load() != 1 {
		t.Fatalf("disconnected browser was not recycled: attempts=%d closes=%d err=%v", attempts.Load(), first.closes.Load(), err)
	}
}

func TestBrowserInitializationCancellationAndShutdown(t *testing.T) {
	Shutdown()
	t.Cleanup(Shutdown)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started, release := make(chan struct{}), make(chan struct{})
	browser := &fakeBrowser{}
	result := make(chan error, 1)
	go func() {
		result <- initBrowserWith(ctx, func() (*playwright.Playwright, playwright.Browser, *browserProxy, error) {
			close(started)
			<-release
			browser.connected.Store(true)
			return nil, browser, nil, nil
		})
	}()
	<-started
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("browser initialization blocked turn cancellation")
	}
	Shutdown()
	close(release)
	await(t, func() bool { return browser.closes.Load() == 1 })
	mu.Lock()
	resurrected := sharedBrowser != nil
	mu.Unlock()
	if resurrected {
		t.Fatal("initializer resurrected browser after shutdown")
	}
}

func TestBrowserLaunchKeepsSecurityAndStealthKeepsDOMEvents(t *testing.T) {
	opts := browserLaunchOptions("/browser", "http://127.0.0.1:12345", true)
	if opts.ChromiumSandbox == nil || !*opts.ChromiumSandbox || opts.HandleSIGINT == nil || *opts.HandleSIGINT {
		t.Fatal("sandbox or turn interruption settings are unsafe")
	}
	for _, arg := range opts.Args {
		if arg == "--no-sandbox" || arg == "--disable-web-security" || strings.Contains(arg, "IsolateOrigins") || strings.Contains(arg, "site-per-process") {
			t.Fatal("unsafe browser flag:", arg)
		}
	}
	for _, patch := range []string{"Element.prototype.addEventListener", "Object.assign({},", "console.log ="} {
		if strings.Contains(chromeStealthJS, patch) {
			t.Fatal("stealth still breaks events or silences logs:", patch)
		}
	}
}

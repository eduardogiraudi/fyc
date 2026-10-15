//go:build linux

package tools

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/godbus/dbus/v5"
)

// Minimal XDG Desktop Portal client built on D-Bus (pure Go via
// github.com/godbus/dbus/v5, no cgo, no external binaries).
//
// The portal "request/response" pattern works like this:
//
//  1. a request method returns an object path: the Request object for that call;
//  2. the result is delivered later as the Response(u, a{sv}) signal emitted on
//     that Request path;
//  3. the path is predictable when the caller passes a unique handle_token:
//
//     /org/freedesktop/portal/desktop/request/<SENDER>/<handle_token>
//
//     where <SENDER> is the caller unique bus name with the leading ':' removed
//     and every '.' replaced by '_' (":1.179" -> "1_179").
//
// To avoid the classic race (subscribing only after the method reply, when the
// signal may already have been emitted) this client subscribes once to
// Request::Response signals and dispatches them to per-request channels keyed by
// the pre-computed Request path. Session::Closed signals are dispatched to
// registered watchers so a session that the compositor tears down is recreated
// on the next action.

const (
	portalBusName    = "org.freedesktop.portal.Desktop"
	portalObjectPath = dbus.ObjectPath("/org/freedesktop/portal/desktop")

	portalRequestIface       = "org.freedesktop.portal.Request"
	portalSessionIface       = "org.freedesktop.portal.Session"
	portalResponseMember     = "Response"
	portalSessionCloseMethod = portalSessionIface + ".Close"
)

var (
	portalClientOnce sync.Once
	portalClientVal  *portalClient
	portalClientErr  error

	portalTokenSeq atomic.Uint64

	// pid is captured once so generated tokens stay unique across processes.
	pid = os.Getpid()

	// portalEmptyOptions is an empty a{sv} used by the fire-and-forget Notify* calls.
	portalEmptyOptions = map[string]dbus.Variant{}
)

// portalClient wraps a private D-Bus session connection dedicated to portals.
type portalClient struct {
	conn   *dbus.Conn
	unique string

	sigCh chan *dbus.Signal

	mu       sync.Mutex
	waiters  map[dbus.ObjectPath]chan *dbus.Signal
	watchers map[dbus.ObjectPath]func()
}

// getPortalClient returns the (lazily created) shared portal client. The error
// is cached: if the portal is unavailable the first time it stays unavailable.
func getPortalClient() (*portalClient, error) {
	portalClientOnce.Do(func() {
		portalClientVal, portalClientErr = newPortalClient()
	})
	return portalClientVal, portalClientErr
}

func newPortalClient() (*portalClient, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("cannot connect to the D-Bus session bus (is a desktop session running?): %w", err)
	}

	var hasOwner bool
	if err := conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, portalBusName).Store(&hasOwner); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("cannot query the D-Bus session bus: %w", err)
	}
	if !hasOwner {
		_ = conn.Close()
		return nil, fmt.Errorf("the XDG desktop portal (%s) is not available on the session bus", portalBusName)
	}

	unique := ""
	for _, name := range conn.Names() {
		if strings.HasPrefix(name, ":") {
			unique = name
			break
		}
	}
	if unique == "" {
		_ = conn.Close()
		return nil, fmt.Errorf("cannot determine the D-Bus unique connection name")
	}

	sigCh := make(chan *dbus.Signal, 64)
	if err := conn.AddMatchSignal(
		dbus.WithMatchSender(portalBusName),
		dbus.WithMatchInterface(portalRequestIface),
		dbus.WithMatchMember(portalResponseMember),
	); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("cannot subscribe to portal responses: %w", err)
	}
	if err := conn.AddMatchSignal(
		dbus.WithMatchSender(portalBusName),
		dbus.WithMatchInterface(portalSessionIface),
	); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("cannot subscribe to portal session signals: %w", err)
	}
	conn.Signal(sigCh)

	p := &portalClient{
		conn:     conn,
		unique:   unique,
		sigCh:    sigCh,
		waiters:  make(map[dbus.ObjectPath]chan *dbus.Signal),
		watchers: make(map[dbus.ObjectPath]func()),
	}
	go p.dispatch()
	return p, nil
}

func (p *portalClient) dispatch() {
	for sig := range p.sigCh {
		p.mu.Lock()
		if ch, ok := p.waiters[sig.Path]; ok {
			select {
			case ch <- sig:
			default:
			}
			p.mu.Unlock()
			continue
		}
		cb, ok := p.watchers[sig.Path]
		p.mu.Unlock()
		if ok {
			cb()
		}
	}
}

// call performs a plain D-Bus method call on the portal object.
func (p *portalClient) call(method string, args ...interface{}) error {
	obj := p.conn.Object(portalBusName, portalObjectPath)
	return obj.Call(method, 0, args...).Err
}

// notify performs a fire-and-forget portal call (the Notify* input methods).
// When the compositor granted the session without input control — e.g. GNOME's
// "Allow Remote Interaction" switch left off in the consent dialog, which still
// lets the screen be shared — every Notify* fails with a generic "not allowed"
// error; rewrite it into something actionable.
func (p *portalClient) notify(method string, args ...interface{}) error {
	err := p.call(method, args...)
	if err != nil && strings.Contains(err.Error(), "is not allowed to call Notify") {
		return fmt.Errorf("%w — the session was granted without input control: in the "+
			"Remote Desktop consent dialog enable \"Allow Remote Interaction\"; if the dialog "+
			"no longer appears, a remembered choice is being reused and must be reset", err)
	}
	return err
}

// request performs a portal request: it registers the waiter for the predicted
// Request path, invokes the method and waits for the Response signal (or ctx).
func (p *portalClient) request(ctx context.Context, token string, invoke func() error) (uint32, map[string]dbus.Variant, error) {
	path := dbus.ObjectPath(portalRequestPath(p.unique, token))

	ch := make(chan *dbus.Signal, 1)
	p.mu.Lock()
	p.waiters[path] = ch
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		if p.waiters[path] == ch {
			delete(p.waiters, path)
		}
		p.mu.Unlock()
	}()

	if err := invoke(); err != nil {
		return 0, nil, err
	}

	select {
	case sig := <-ch:
		if sig == nil || len(sig.Body) < 2 {
			return 0, nil, fmt.Errorf("malformed portal Response signal")
		}
		code, _ := sig.Body[0].(uint32)
		results, _ := sig.Body[1].(map[string]dbus.Variant)
		if results == nil {
			results = map[string]dbus.Variant{}
		}
		return code, results, nil
	case <-ctx.Done():
		return 0, nil, fmt.Errorf("the desktop portal did not answer in time: %w", ctx.Err())
	}
}

// watchSession registers a callback fired when the portal announces that the
// session object has been closed by the compositor.
func (p *portalClient) watchSession(path dbus.ObjectPath, cb func()) {
	p.mu.Lock()
	p.watchers[path] = cb
	p.mu.Unlock()
}

func (p *portalClient) unwatchSession(path dbus.ObjectPath) {
	p.mu.Lock()
	delete(p.watchers, path)
	p.mu.Unlock()
}

// closeSession asks the portal to tear down a session (best effort).
func (p *portalClient) closeSession(path dbus.ObjectPath) {
	p.unwatchSession(path)
	_ = p.call(portalSessionCloseMethod, path)
}

// ── helpers ──────────────────────────────────────────────────────────────────

// newPortalToken returns a fresh handle_token/session_handle_token. It must be a
// valid D-Bus object path element ([A-Za-z0-9_]).
func newPortalToken(prefix string) string {
	return fmt.Sprintf("%s_%d_%d", prefix, pid, portalTokenSeq.Add(1))
}

// portalSenderToken converts a unique bus name to the path element used by the
// portal: ":" is dropped and "." becomes "_" (":1.179" -> "1_179").
func portalSenderToken(unique string) string {
	return strings.ReplaceAll(strings.TrimPrefix(unique, ":"), ".", "_")
}

// portalRequestPath computes the Request object path for a handle_token.
func portalRequestPath(unique, token string) string {
	return "/org/freedesktop/portal/desktop/request/" + portalSenderToken(unique) + "/" + token
}

// portalOptions converts a plain map to the a{sv} wire type.
func portalOptions(opts map[string]interface{}) map[string]dbus.Variant {
	out := make(map[string]dbus.Variant, len(opts))
	for k, v := range opts {
		out[k] = dbus.MakeVariant(v)
	}
	return out
}

// variantString extracts a string from a vardict entry.
func variantString(v dbus.Variant) string {
	switch t := v.Value().(type) {
	case string:
		return t
	case dbus.ObjectPath:
		return string(t)
	}
	return ""
}

// variantPath extracts an object path from a vardict entry. The portals return
// session handles as strings (a historical quirk) but some backends send them
// as real object paths, so both are accepted.
func variantPath(v dbus.Variant) dbus.ObjectPath {
	switch t := v.Value().(type) {
	case string:
		return dbus.ObjectPath(t)
	case dbus.ObjectPath:
		return t
	}
	return ""
}

// variantUint32 extracts a uint32 from a vardict entry.
func variantUint32(v dbus.Variant) uint32 {
	u, _ := v.Value().(uint32)
	return u
}

// intTuple decodes a (ii) vardict entry (e.g. stream position/size).
func intTuple(v dbus.Variant) ([2]int, bool) {
	t, ok := v.Value().([]interface{})
	if !ok || len(t) < 2 {
		return [2]int{}, false
	}
	x, ok1 := t[0].(int32)
	y, ok2 := t[1].(int32)
	if !ok1 || !ok2 {
		return [2]int{}, false
	}
	return [2]int{int(x), int(y)}, true
}

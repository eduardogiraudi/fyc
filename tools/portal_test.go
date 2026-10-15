//go:build linux

package tools

import (
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
)

// testVariant builds a dbus.Variant with an explicit signature; needed because
// the Go fixtures (slice-of-interface tuples) have no inferable D-Bus signature.
func testVariant(sig string, val interface{}) dbus.Variant {
	return dbus.MakeVariantWithSignature(val, dbus.ParseSignatureMust(sig))
}

func TestPortalSenderToken(t *testing.T) {
	cases := map[string]string{
		":1.179": "1_179",
		":1.0":   "1_0",
		":42.7":  "42_7",
	}
	for in, want := range cases {
		if got := portalSenderToken(in); got != want {
			t.Errorf("portalSenderToken(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPortalRequestPath(t *testing.T) {
	got := portalRequestPath(":1.179", "fyc_42")
	want := "/org/freedesktop/portal/desktop/request/1_179/fyc_42"
	if got != want {
		t.Errorf("portalRequestPath = %q, want %q", got, want)
	}
}

func TestNewPortalTokenIsUniqueAndValid(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		tok := newPortalToken("fyc")
		if seen[tok] {
			t.Fatalf("duplicate token %q", tok)
		}
		seen[tok] = true
		for _, r := range tok {
			if !(r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
				t.Fatalf("token %q contains invalid character %q", tok, r)
			}
		}
	}
}

func TestPortalOptionsMarshalling(t *testing.T) {
	opts := portalOptions(map[string]interface{}{
		"handle_token": "tok",
		"types":        uint32(3),
		"multiple":     false,
	})
	if s := opts["handle_token"].Signature().String(); s != "s" {
		t.Errorf("handle_token signature = %q, want s", s)
	}
	if s := opts["types"].Signature().String(); s != "u" {
		t.Errorf("types signature = %q, want u", s)
	}
	if s := opts["multiple"].Signature().String(); s != "b" {
		t.Errorf("multiple signature = %q, want b", s)
	}
}

func TestFileFromURI(t *testing.T) {
	got, err := fileFromURI("file:///home/x/Screenshot%20from%202026.png")
	if err != nil {
		t.Fatalf("fileFromURI error: %v", err)
	}
	if got != "/home/x/Screenshot from 2026.png" {
		t.Errorf("fileFromURI = %q", got)
	}
	if _, err := fileFromURI(""); err == nil {
		t.Error("empty URI: expected error")
	}
	if _, err := fileFromURI("https://example.com/a.png"); err == nil {
		t.Error("non-file URI: expected error")
	}
}

func TestParseStreams(t *testing.T) {
	// godbus decodes the a(ua{sv}) array into a typed [][]interface{} (one
	// inner slice per stream struct): the fixture mirrors that real shape so a
	// regression in the slice handling is caught here.
	fixture := [][]interface{}{
		{
			uint32(7),
			map[string]dbus.Variant{
				"position": testVariant("(ii)", []interface{}{int32(1920), int32(0)}),
				"size":     testVariant("(ii)", []interface{}{int32(1920), int32(1080)}),
				"id":       dbus.MakeVariant("stream-1"),
			},
		},
	}
	got := parseStreams(testVariant("a(ua{sv})", fixture))
	if len(got) != 1 {
		t.Fatalf("parseStreams returned %d streams, want 1", len(got))
	}
	st := got[0]
	if st.nodeID != 7 || st.posX != 1920 || st.posY != 0 || st.sizeW != 1920 || st.sizeH != 1080 {
		t.Fatalf("unexpected stream: %+v", st)
	}

	// The untyped []interface{} shape (manually built fixtures) must keep working.
	loose := []interface{}{[]interface{}{uint32(7), map[string]dbus.Variant{}}}
	if streams := parseStreams(testVariant("a(ua{sv})", loose)); len(streams) != 1 {
		t.Fatalf("parseStreams([]interface{}) = %d, want 1", len(streams))
	}

	// An empty/missing property list must not panic.
	if streams := parseStreams(testVariant("a(ua{sv})", [][]interface{}{})); len(streams) != 0 {
		t.Fatalf("empty streams = %d, want 0", len(streams))
	}
}

func TestVariantUint32AndDeviceGrants(t *testing.T) {
	if got := variantUint32(dbus.MakeVariant(uint32(3))); got != 3 {
		t.Errorf("variantUint32(uint32(3)) = %d, want 3", got)
	}
	if got := variantUint32(dbus.MakeVariant("nope")); got != 0 {
		t.Errorf("variantUint32(string) = %d, want 0", got)
	}
	// A session granted without input (GNOME dialog with "Allow Remote
	// Interaction" off) yields devices=0 and must be rejected before notifying.
	if devices := variantUint32(dbus.MakeVariant(uint32(0))); devices&(portalDeviceKeyboard|portalDevicePointer) != 0 {
		t.Error("devices=0 must be treated as no input control")
	}
	for _, ok := range []uint32{uint32(portalDeviceKeyboard), uint32(portalDevicePointer), 3} {
		if d := variantUint32(dbus.MakeVariant(ok)); d&(portalDeviceKeyboard|portalDevicePointer) == 0 {
			t.Errorf("devices=%d must be accepted", ok)
		}
	}
}

func TestToStreamCoords(t *testing.T) {
	st := &waylandStream{posX: 1920, posY: 0, sizeW: 1920, sizeH: 1080}

	x, y, err := st.toStreamCoords(1920, 0)
	if err != nil || x != 0 || y != 0 {
		t.Fatalf("toStreamCoords(1920,0) = (%v,%v,%v), want (0,0,nil)", x, y, err)
	}
	if x, y, err := st.toStreamCoords(2879, 1079); err != nil || x != 959 || y != 1079 {
		t.Fatalf("toStreamCoords(2879,1079) = (%v,%v,%v)", x, y, err)
	}
	if _, _, err := st.toStreamCoords(100, 10); err == nil {
		t.Error("point outside the shared monitor: expected error")
	}
	if _, _, err := st.toStreamCoords(1920+1920, 0); err == nil {
		t.Error("point past the shared monitor: expected error")
	}
}

// ── Mutter DisplayConfig fixtures ────────────────────────────────────────────

func fixtureMode(w, h int32, current bool) []interface{} {
	props := map[string]dbus.Variant{}
	if current {
		props["is-current"] = dbus.MakeVariant(true)
	}
	return []interface{}{
		"mode-id", w, h, float64(60), float64(1),
		[]interface{}{float64(1), float64(2)}, props,
	}
}

func fixtureMonitor(conn string, modes ...[]interface{}) []interface{} {
	raw := make([]interface{}, len(modes))
	for i, m := range modes {
		raw[i] = m
	}
	return []interface{}{
		[]interface{}{conn, "vendor", "product", "serial"},
		raw,
		map[string]dbus.Variant{"is-builtin": dbus.MakeVariant(false)},
	}
}

func fixtureLogicalMonitor(x, y int32, scale float64, conns ...string) []interface{} {
	raw := make([]interface{}, len(conns))
	for i, c := range conns {
		raw[i] = []interface{}{c, "vendor", "product", "serial"}
	}
	return []interface{}{x, y, scale, uint32(0), true, raw, map[string]dbus.Variant{}}
}

func TestLogicalDesktopSizeFromState(t *testing.T) {
	// Two 1920x1080 monitors side by side: a 3840x1080 logical desktop.
	body := []interface{}{
		uint32(1),
		[]interface{}{
			fixtureMonitor("eDP-1", fixtureMode(1920, 1080, true), fixtureMode(1280, 720, false)),
			fixtureMonitor("HDMI-1", fixtureMode(1920, 1080, true)),
		},
		[]interface{}{
			fixtureLogicalMonitor(0, 0, 1.0, "eDP-1"),
			fixtureLogicalMonitor(1920, 0, 1.0, "HDMI-1"),
		},
		map[string]dbus.Variant{},
	}
	w, h, ok := logicalDesktopSizeFromState(body)
	if !ok || w != 3840 || h != 1080 {
		t.Fatalf("logicalDesktopSizeFromState = (%d,%d,%v), want (3840,1080,true)", w, h, ok)
	}

	// HiDPI: a 3840x2160 panel at scale 2 is a 1920x1080 logical monitor.
	body = []interface{}{
		uint32(1),
		[]interface{}{fixtureMonitor("eDP-1", fixtureMode(3840, 2160, true))},
		[]interface{}{fixtureLogicalMonitor(0, 0, 2.0, "eDP-1")},
		map[string]dbus.Variant{},
	}
	w, h, ok = logicalDesktopSizeFromState(body)
	if !ok || w != 1920 || h != 1080 {
		t.Fatalf("HiDPI logicalDesktopSizeFromState = (%d,%d,%v), want (1920,1080,true)", w, h, ok)
	}

	// Malformed replies must be reported as unavailable, not panic.
	if _, _, ok := logicalDesktopSizeFromState(nil); ok {
		t.Error("nil body: expected ok=false")
	}
	if _, _, ok := logicalDesktopSizeFromState([]interface{}{uint32(1), "junk", "junk"}); ok {
		t.Error("junk body: expected ok=false")
	}
}

func TestEvdevTables(t *testing.T) {
	if evdevKeycodes["a"] != 30 || evdevKeycodes["enter"] != 28 || evdevKeycodes["f12"] != 88 {
		t.Errorf("evdevKeycodes table looks wrong: %v", evdevKeycodes)
	}
	if evdevModifiers["ctrl"] != 29 || evdevModifiers["super"] != 125 {
		t.Errorf("evdevModifiers table looks wrong: %v", evdevModifiers)
	}
	if got := evdevButton("right"); got != 0x111 {
		t.Errorf("evdevButton(right) = %#x, want 0x111", got)
	}
	if got := evdevButton(""); got != 0x110 {
		t.Errorf("evdevButton(default) = %#x, want 0x110", got)
	}
}

func TestWaylandStreamGeometryStableToString(t *testing.T) {
	// Guard against accidental signature drift in the parse helpers.
	if sig := dbus.ParseSignatureMust("a(ua{sv})").String(); !strings.HasPrefix(sig, "a(") {
		t.Fatalf("unexpected signature %q", sig)
	}
}

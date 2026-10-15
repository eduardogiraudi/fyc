//go:build linux

package tools

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestPortalProbeNoStart exercises the portal request/response machinery
// against the live session bus without calling RemoteDesktop.Start (which would
// open a compositor dialog). Opt-in only.
func TestPortalProbeNoStart(t *testing.T) {
	if os.Getenv("FYC_PORTAL_TEST") != "1" {
		t.Skip("set FYC_PORTAL_TEST=1 to run the live portal probe (no dialog: Start is not called)")
	}
	cl, err := getPortalClient()
	if err != nil {
		t.Fatalf("getPortalClient: %v", err)
	}
	t.Logf("unique name: %s", cl.unique)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ct := newPortalToken("fycprobe")
	code, results, err := cl.request(ctx, ct, func() error {
		return cl.call(portalRemoteDesktopIface+".CreateSession", portalOptions(map[string]interface{}{
			"handle_token":         ct,
			"session_handle_token": newPortalToken("fycprobes"),
		}))
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	t.Logf("CreateSession code=%d results=%v", code, results)
	handle := variantPath(results["session_handle"])
	if handle == "" {
		t.Fatal("no session handle returned")
	}
	defer cl.closeSession(handle)

	ss := newPortalToken("fycprobess")
	code, _, err = cl.request(ctx, ss, func() error {
		return cl.call(portalScreenCastIface+".SelectSources", handle, portalOptions(map[string]interface{}{
			"handle_token": ss,
			"types":        uint32(portalSourceMonitor),
			"multiple":     false,
			"cursor_mode":  uint32(portalCursorModeEmbedded),
		}))
	})
	t.Logf("SelectSources code=%d err=%v", code, err)

	sd := newPortalToken("fycprobesd")
	code, _, err = cl.request(ctx, sd, func() error {
		return cl.call(portalRemoteDesktopIface+".SelectDevices", handle, portalOptions(map[string]interface{}{
			"handle_token": sd,
			"types":        uint32(portalDeviceKeyboard | portalDevicePointer),
			"persist_mode": uint32(portalPersistUntilRevoked),
		}))
	})
	t.Logf("SelectDevices code=%d err=%v", code, err)

	var devTypes uint32
	if err := cl.conn.Object(portalBusName, portalObjectPath).Call(
		"org.freedesktop.DBus.Properties.Get", 0, portalRemoteDesktopIface, "AvailableDeviceTypes").Store(&devTypes); err != nil {
		t.Logf("AvailableDeviceTypes: %v", err)
	} else {
		t.Logf("AvailableDeviceTypes=%d", devTypes)
	}
}

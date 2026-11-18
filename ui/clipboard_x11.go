//go:build linux

package ui

import (
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

// X11 has no clipboard API: the client that copies becomes the owner of the
// CLIPBOARD selection and serves the text to whoever pastes. Owning it here
// costs one lazily opened X connection and needs no external helper, which
// matters where nothing like xclip/wl-clipboard is installed. It works on every
// X11 desktop and on XWayland, where the compositor bridges the X11 selection
// to the Wayland clipboard. The owner only lives as long as this process, so
// nothing keeps the text after the CLI exits — there is no clipboard manager in
// the middle, exactly like a helper that dies with its client.
var (
	x11Once    sync.Once
	x11Sel     *x11Selection
	x11InitErr error
)

func init() {
	// xgb reports connection trouble straight to stderr, which in alt-screen
	// mode lands in the middle of the frame. The app surfaces its own errors.
	xgb.Logger = log.New(io.Discard, "", 0)
}

// errNoX11 reports that this machine gives us no X display to own a selection
// on, so the caller should keep looking for another way to copy.
var errNoX11 = errors.New("no X11 display")

// x11ClipboardSet hands text to a CLIPBOARD selection owned by this process.
func x11ClipboardSet(text string) error {
	x11Once.Do(func() { x11Sel, x11InitErr = newX11Selection() })
	if x11InitErr != nil {
		return x11InitErr
	}
	return x11Sel.set(text)
}

// x11Selection is the process-wide selection owner: one connection and one
// event loop answering every pasting client, plus the window that currently
// owns the selection.
type x11Selection struct {
	conn *xgb.Conn
	root xproto.Window
	// win owns the selection right now; prevWin lost it on the last copy and is
	// kept one generation, because a paste that was already in flight when the
	// ownership moved still has to be answered.
	win     xproto.Window
	prevWin xproto.Window
	// The atoms are interned once; the request handler pays no round trips.
	clipboard xproto.Atom
	targets   xproto.Atom
	utf8      xproto.Atom
	text      xproto.Atom
	compound  xproto.Atom
	incr      xproto.Atom
	// chunk is the largest payload one ChangeProperty request may carry: a
	// bigger selection is transferred incrementally (ICCCM INCR).
	chunk int

	mu       sync.Mutex
	payload  string
	transfer []*incrTransfer
}

// incrTransfer is one in-flight incremental transfer. The requestor deletes the
// property after reading each chunk, which is the signal to write the next one.
type incrTransfer struct {
	requestor xproto.Window
	property  xproto.Atom
	typ       xproto.Atom
	data      []byte
}

func newX11Selection() (*x11Selection, error) {
	if os.Getenv("DISPLAY") == "" {
		return nil, errNoX11
	}
	xwaylandAuth()
	conn, err := xgb.NewConn()
	if err != nil {
		return nil, err
	}
	setup := xproto.Setup(conn)
	screen := setup.DefaultScreen(conn)
	if screen == nil {
		conn.Close()
		return nil, errNoX11
	}
	s := &x11Selection{conn: conn, root: screen.Root}
	for _, a := range []struct {
		dst  *xproto.Atom
		name string
	}{
		{&s.clipboard, "CLIPBOARD"},
		{&s.targets, "TARGETS"},
		{&s.utf8, "UTF8_STRING"},
		{&s.text, "TEXT"},
		{&s.compound, "COMPOUND_TEXT"},
		{&s.incr, "INCR"},
	} {
		r, err := xproto.InternAtom(conn, false, uint16(len(a.name)), a.name).Reply()
		if err != nil {
			conn.Close()
			return nil, err
		}
		*a.dst = r.Atom
	}
	// MaximumRequestLength is expressed in 4-byte units; keep a margin for the
	// ChangeProperty header and the padding xgb adds.
	s.chunk = int(setup.MaximumRequestLength)*4 - 64
	go s.serve()
	return s, nil
}

// newOwnerWindow mints the window that will take the selection. An InputOnly
// 1x1 window is never mapped or drawn: it only exists to be the owner, which is
// why it needs no event mask of its own.
func (s *x11Selection) newOwnerWindow() (xproto.Window, error) {
	win, err := xproto.NewWindowId(s.conn)
	if err != nil {
		return 0, err
	}
	if err := xproto.CreateWindowChecked(s.conn, 0, win, s.root, 0, 0, 1, 1, 0,
		xproto.WindowClassInputOnly, 0, xproto.CwOverrideRedirect, []uint32{1}).Check(); err != nil {
		return 0, err
	}
	return win, nil
}

// xwaylandAuth points XAUTHORITY at the cookie mutter writes for XWayland when
// the variable is missing. Without it the connection is refused and the copy
// would quietly fall back to a terminal sequence the session probably drops.
func xwaylandAuth() {
	if os.Getenv("XAUTHORITY") != "" {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	if _, err := os.Stat(filepath.Join(home, ".Xauthority")); err == nil {
		return
	}
	matches, err := filepath.Glob(filepath.Join(home, ".mutter-Xwaylandauth.*"))
	if err != nil || len(matches) == 0 {
		return
	}
	os.Setenv("XAUTHORITY", matches[0])
}

// set publishes text as the new content of the selection.
//
// Every copy takes the ownership with a *fresh* window, the way a clipboard
// helper that is spawned per copy does: compositors and clipboard managers
// react to the owner changing, so updating the text of an ownership that never
// moved leaves them serving the first copy for the rest of the session.
func (s *x11Selection) set(text string) error {
	s.mu.Lock()
	s.payload = text
	s.mu.Unlock()

	win, err := s.newOwnerWindow()
	if err != nil {
		return err
	}
	// SetSelectionOwner is silently ignored when it is older than the current
	// owner's acquisition time and a manager may race us for the selection, so
	// the ownership is read back from the server instead of assumed.
	owned := false
	for attempt := 0; attempt < 3 && !owned; attempt++ {
		if err := xproto.SetSelectionOwnerChecked(s.conn, win, s.clipboard,
			xproto.TimeCurrentTime).Check(); err != nil {
			xproto.DestroyWindow(s.conn, win)
			return err
		}
		r, err := xproto.GetSelectionOwner(s.conn, s.clipboard).Reply()
		if err != nil {
			xproto.DestroyWindow(s.conn, win)
			return err
		}
		owned = r.Owner == win
		if !owned {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if !owned {
		xproto.DestroyWindow(s.conn, win)
		return errors.New("another client owns the clipboard")
	}

	s.mu.Lock()
	old, prev := s.prevWin, s.win
	s.win, s.prevWin = win, prev
	s.mu.Unlock()
	if old != 0 {
		xproto.DestroyWindow(s.conn, old)
	}
	s.conn.Sync()
	return nil
}

// serve answers paste requests until the connection goes away. Protocol errors
// (a requestor window that is already gone) are routine and must not stop the
// owner; only a dead connection does.
func (s *x11Selection) serve() {
	for {
		ev, err := s.conn.WaitForEvent()
		if err != nil {
			if _, protocol := err.(xgb.Error); protocol {
				continue
			}
			s.mu.Lock()
			s.transfer = nil
			s.mu.Unlock()
			return
		}
		switch e := ev.(type) {
		case xproto.SelectionRequestEvent:
			s.serveRequest(e)
		case xproto.SelectionClearEvent:
			// Someone else copied: stop any transfer aimed at the window that
			// just lost the selection.
			s.mu.Lock()
			s.dropTransfers(e.Owner)
			s.mu.Unlock()
		case xproto.PropertyNotifyEvent:
			s.serveChunk(e)
		}
	}
}

// serveRequest answers one paste. An unknown target, or a request aimed at a
// selection we no longer own, is refused with a SelectionNotify carrying no
// property, which is how a client learns there is nothing to paste.
func (s *x11Selection) serveRequest(req xproto.SelectionRequestEvent) {
	s.mu.Lock()
	payload := s.payload
	current, previous := s.win, s.prevWin
	s.mu.Unlock()

	property := req.Property
	if property == 0 {
		property = req.Target
	}
	// Requests aimed at the window that lost the selection on the last copy are
	// still ours to answer: that paste may have started before the handover.
	ours := req.Owner == 0 || req.Owner == current || req.Owner == previous
	if req.Selection != s.clipboard || !ours {
		s.notify(req, 0)
		return
	}
	switch req.Target {
	case s.targets:
		// TARGETS is a list of atoms, so the values are 32-bit, not text.
		atoms := []xproto.Atom{s.targets, s.utf8, s.text, s.compound, xproto.AtomString}
		data := make([]byte, 0, 4*len(atoms))
		for _, a := range atoms {
			data = append(data, byte(a), byte(a>>8), byte(a>>16), byte(a>>24))
		}
		xproto.ChangeProperty(s.conn, xproto.PropModeReplace, req.Requestor, property,
			xproto.AtomAtom, 32, uint32(len(atoms)), data)
		s.notify(req, property)
	case s.utf8, s.text, s.compound, xproto.AtomString:
		data := []byte(payload)
		if len(data) > s.chunk {
			s.startIncr(req, property, data)
		} else {
			xproto.ChangeProperty(s.conn, xproto.PropModeReplace, req.Requestor, property,
				req.Target, 8, uint32(len(data)), data)
		}
		s.notify(req, property)
	default:
		s.notify(req, 0)
	}
	s.conn.Sync()
}

// startIncr announces a transfer too large for one request: the property holds
// the total size and the requestor pulls the chunks by deleting it.
func (s *x11Selection) startIncr(req xproto.SelectionRequestEvent, property xproto.Atom, data []byte) {
	// PropertyNotify on the requestor's window is what carries the pull.
	xproto.ChangeWindowAttributes(s.conn, req.Requestor, xproto.CwEventMask,
		[]uint32{xproto.EventMaskPropertyChange})
	size := uint32(len(data))
	xproto.ChangeProperty(s.conn, xproto.PropModeReplace, req.Requestor, property,
		s.incr, 32, 1, []byte{byte(size), byte(size >> 8), byte(size >> 16), byte(size >> 24)})
	s.mu.Lock()
	s.transfer = append(s.transfer, &incrTransfer{
		requestor: req.Requestor,
		property:  property,
		typ:       req.Target,
		data:      data,
	})
	s.mu.Unlock()
}

// serveChunk writes the next slice of an incremental transfer. A zero-length
// write ends it, as ICCCM requires.
func (s *x11Selection) serveChunk(ev xproto.PropertyNotifyEvent) {
	if ev.State != xproto.PropertyDelete {
		return
	}
	s.mu.Lock()
	var t *incrTransfer
	done := false
	for i, cand := range s.transfer {
		if cand.requestor != ev.Window || cand.property != ev.Atom {
			continue
		}
		t = cand
		if len(cand.data) == 0 {
			s.transfer = append(s.transfer[:i], s.transfer[i+1:]...)
			done = true
		}
		break
	}
	s.mu.Unlock()
	if t == nil {
		return
	}
	chunk := t.data
	if len(chunk) > s.chunk {
		chunk = chunk[:s.chunk]
	}
	xproto.ChangeProperty(s.conn, xproto.PropModeReplace, t.requestor, t.property, t.typ,
		8, uint32(len(chunk)), chunk)
	if done {
		return
	}
	s.mu.Lock()
	t.data = t.data[len(chunk):]
	s.mu.Unlock()
	s.conn.Sync()
}

// dropTransfers forgets the incremental transfers that belonged to win.
func (s *x11Selection) dropTransfers(win xproto.Window) {
	kept := s.transfer[:0]
	for _, t := range s.transfer {
		if t.requestor != win {
			kept = append(kept, t)
		}
	}
	s.transfer = kept
}

// notify tells the requestor the transfer is complete (property 0: refused).
func (s *x11Selection) notify(req xproto.SelectionRequestEvent, property xproto.Atom) {
	xproto.SendEvent(s.conn, false, req.Requestor, 0, string(xproto.SelectionNotifyEvent{
		Time:      req.Time,
		Requestor: req.Requestor,
		Selection: req.Selection,
		Target:    req.Target,
		Property:  property,
	}.Bytes()))
}

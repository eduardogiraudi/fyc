package agui

import (
	"crypto/rand"
	"encoding/hex"
)

// NewID returns a short random identifier prefixed with prefix, e.g.
// "msg_3f9a…". It is used for message, tool-call, thread and run ids.
func NewID(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is not expected; fall back to a fixed marker so
		// the caller still gets a well-formed (if non-unique) id.
		return prefix + "_0000000000000000"
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}

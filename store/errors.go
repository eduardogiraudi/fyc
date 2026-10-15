package store

import (
	"errors"
	"fmt"
	"os"
	"sync"
)

// Sentinel errors returned (wrapped) by the store package so callers can react
// with errors.Is instead of matching on message strings.
var (
	// ErrCorrupt reports a file that exists but cannot be parsed as the
	// expected JSON document.
	ErrCorrupt = errors.New("store: corrupt file")
	// ErrSchemaTooNew reports a document written by a newer version of the
	// application (schema beyond the target the caller understands). Loading it
	// would silently drop fields, so it is refused.
	ErrSchemaTooNew = errors.New("store: schema newer than supported")
	// ErrNoMigration reports a missing step in the migration chain.
	ErrNoMigration = errors.New("store: missing migration")
	// ErrLocked is returned by TryLock when the lock is held elsewhere.
	ErrLocked = errors.New("store: file is locked")
)

var (
	logMu   sync.Mutex
	logFunc = defaultLog
)

func defaultLog(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

// SetLogger redirects the package's non-fatal notices (for example a successful
// recovery from a ".bak" copy) to fn. Passing nil restores the default sink
// (stderr). Fatal conditions are never logged here: they are returned.
func SetLogger(fn func(format string, args ...any)) {
	logMu.Lock()
	defer logMu.Unlock()
	if fn == nil {
		logFunc = defaultLog
		return
	}
	logFunc = fn
}

func logf(format string, args ...any) {
	logMu.Lock()
	fn := logFunc
	logMu.Unlock()
	fn(format, args...)
}

// Notice reports a non-fatal problem (for example the reason a file was
// discarded and rebuilt from defaults) through the package logger. Callers that
// need to react to the condition still return the underlying error.
func Notice(format string, args ...any) {
	logf(format, args...)
}

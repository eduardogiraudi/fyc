package logger

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fuckyourcli/store"
)

const (
	eventFinished = "request_finished"
	eventFailed   = "request_failed"

	usageSchema = 1
)

// Totals is a sum of provider usage. Input is the whole prompt, cached part
// included (see eventInput), so Input and CacheRead are comparable across
// providers. There is deliberately no cost: prices differ per provider and
// endpoint and cannot be known reliably.
type Totals struct {
	Requests   int `json:"requests"`
	Failed     int `json:"failed,omitempty"`
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cache_read,omitempty"`
	CacheWrite int `json:"cache_write,omitempty"`
}

func (t *Totals) add(o Totals) {
	t.Requests += o.Requests
	t.Failed += o.Failed
	t.Input += o.Input
	t.Output += o.Output
	t.CacheRead += o.CacheRead
	t.CacheWrite += o.CacheWrite
}

func (t *Totals) addEvent(e Event) {
	if e.Type == eventFailed {
		t.Failed++
	} else {
		t.Requests++
	}
	t.Input += eventInput(e)
	t.Output += e.CompletionTokens
	t.CacheRead += e.CachedTokens
	t.CacheWrite += e.CacheWriteTokens
}

// eventInput returns the whole prompt of a request. Records written before
// context_tokens existed only carry the provider's own prompt count, which for
// Anthropic is the uncached tail: the cached parts are added back.
func eventInput(e Event) int {
	if e.ContextTokens > 0 {
		return e.ContextTokens
	}
	if e.Provider == "anthropic" {
		return e.PromptTokens + e.CachedTokens + e.CacheWriteTokens
	}
	return e.PromptTokens
}

// ledger keeps per-day totals outside the conversation logs, which retention
// prunes after MaxLogAge, so lifetime and monthly figures survive it.
type ledger struct {
	Schema int               `json:"schema"`
	Days   map[string]Totals `json:"days"`
}

func usagePath() string { return filepath.Join(store.CacheDir(), "usage.json") }

// Days are local calendar days: "this month" means the user's month.
func dayKey(t time.Time) string { return t.Local().Format("2006-01-02") }

func (l *ledger) addEvent(e Event) {
	if l.Days == nil {
		l.Days = map[string]Totals{}
	}
	day := dayKey(e.Timestamp)
	t := l.Days[day]
	t.addEvent(e)
	l.Days[day] = t
}

// scanEvents feeds fn the finished and failed requests of one events.jsonl.
// Unparsable lines (a torn last record after a crash) are skipped.
func scanEvents(path string, fn func(Event)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var e Event
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		if e.Type == eventFinished || e.Type == eventFailed {
			fn(e)
		}
	}
	return sc.Err()
}

// rebuildLedger recounts every conversation still on disk. Best effort: an
// unreadable conversation is left out rather than failing the whole count.
func rebuildLedger(root string) ledger {
	l := ledger{Schema: usageSchema, Days: map[string]Totals{}}
	entries, err := os.ReadDir(root)
	if err != nil {
		return l
	}
	for _, d := range entries {
		if !d.IsDir() || !validID(d.Name()) {
			continue
		}
		_ = scanEvents(filepath.Join(root, d.Name(), "events.jsonl"), l.addEvent)
	}
	return l
}

// recordUsage is called with the root lock held, right after the event was
// appended to its conversation. A missing or corrupt ledger is rebuilt from the
// logs, which already contain this event, so it must not be added again.
func recordUsage(e Event) error {
	path := usagePath()
	var l ledger
	err := store.ReadJSON(path, &l)
	corrupt := errors.Is(err, store.ErrCorrupt)
	switch {
	case err == nil:
		l.addEvent(e)
	case os.IsNotExist(err) || corrupt:
		l = rebuildLedger(getLogDir())
	default:
		return err
	}
	l.Schema = usageSchema
	return store.WriteJSON(path, l, store.FilePerm, corrupt)
}

// Usage holds the lifetime and current-month totals.
type Usage struct {
	// Since is the first recorded day (YYYY-MM-DD), empty when nothing was.
	Since string
	All   Totals
	Month Totals
}

// LoadUsage returns the lifetime totals and those of the calendar month that
// contains now. Without a ledger yet (nothing was requested since it was
// introduced) the figures come from the conversation logs still on disk.
func LoadUsage(now time.Time) (Usage, error) {
	var l ledger
	err := store.ReadJSON(usagePath(), &l)
	switch {
	case err == nil:
	case os.IsNotExist(err) || errors.Is(err, store.ErrCorrupt):
		l = rebuildLedger(getLogDir())
	default:
		return Usage{}, err
	}
	prefix := now.Local().Format("2006-01")
	var u Usage
	for day, t := range l.Days {
		u.All.add(t)
		if strings.HasPrefix(day, prefix) {
			u.Month.add(t)
		}
		if u.Since == "" || day < u.Since {
			u.Since = day
		}
	}
	return u, nil
}

// SessionUsage sums one conversation, including its sub-agents and compaction
// requests and everything recorded before a resume, and splits it by role.
func SessionUsage(id string) (Totals, map[string]Totals, error) {
	var total Totals
	roles := map[string]Totals{}
	if !validID(id) {
		return total, roles, errors.New("logs: invalid session id")
	}
	err := scanEvents(filepath.Join(getLogDir(), id, "events.jsonl"), func(e Event) {
		total.addEvent(e)
		r := roles[e.Role]
		r.addEvent(e)
		roles[e.Role] = r
	})
	if err != nil && !os.IsNotExist(err) {
		return total, roles, err
	}
	return total, roles, nil
}

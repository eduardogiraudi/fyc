// Package main — AG-UI backend: the `serve` command and the Runner that maps
// HTTP runs onto the existing chat session / streaming loop.
//
// The design keeps the agent core untouched: a run simply installs the AG-UI
// sink on the thread's ChatSession and calls streamChat, exactly like the TUI
// installs ui.TerminalOutputSink.
package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"fuckyourcli/agui"
	"fuckyourcli/config"
	"fuckyourcli/crawl"
	"fuckyourcli/store"
	"fuckyourcli/tools"
	"fuckyourcli/types"
)

// aguiRunner adapts the fuckyourcli chat loop to the agui.Runner interface.
//
// It bootstraps one shared base session (MCP servers, ignore rules, tool
// availability, system prompt) and derives one lightweight ChatSession per
// threadId from it. Runs on the same thread are serialized; runs on different
// threads can proceed in parallel (the tool layer still holds a global mutex).
type aguiRunner struct {
	base       *ChatSession
	cfg        config.Config
	logger     *log.Logger
	maxThreads int
	// store persists per-thread state (MemoryStore when --state-dir is empty).
	store agui.ThreadStore
	// broker owns the level-2 interrupts; its state travels with the thread.
	broker *agui.Broker

	mu       sync.Mutex
	sessions map[string]*aguiThread
}

// aguiThread is the per-thread state: a ChatSession plus its run lock.
type aguiThread struct {
	// id is the thread identifier (the key of the runner's session map).
	id      string
	mu      sync.Mutex
	session *ChatSession
	seeded  bool
}

func newAGUIRunner(cfg config.Config, logger *log.Logger, maxThreads int, store agui.ThreadStore, broker *agui.Broker) *aguiRunner {
	base := newSession()
	base.Headless = true
	if store == nil {
		store = agui.MemoryStore{}
	}
	return &aguiRunner{
		base:       base,
		cfg:        cfg,
		logger:     logger,
		maxThreads: maxThreads,
		store:      store,
		broker:     broker,
		sessions:   map[string]*aguiThread{},
	}
}

// newThreadSession builds a per-thread session that shares the base
// infrastructure. The system prompt is authoritative and inherited from the
// base; client-supplied system/developer messages are dropped upstream.
func (r *aguiRunner) newThreadSession() *ChatSession {
	base := r.base
	s := &ChatSession{
		MCPManager:     base.MCPManager,
		GlobalIgnore:   base.GlobalIgnore,
		Version:        base.Version,
		Commit:         base.Commit,
		RestrictedRoot: base.RestrictedRoot,
		AvailableTools: base.AvailableTools,
		Headless:       true,
	}
	s.LSPToolsAutoDisabled.Store(base.LSPToolsAutoDisabled.Load())
	s.GitToolsAutoDisabled.Store(base.GitToolsAutoDisabled.Load())
	s.gitInRepo.Store(base.gitInRepo.Load())
	if len(base.ChatHistory) > 0 && base.ChatHistory[0].Role == "system" {
		s.ChatHistory = []types.Message{base.ChatHistory[0]}
	}
	return s
}

func (r *aguiRunner) thread(id string) (*aguiThread, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if th, ok := r.sessions[id]; ok {
		return th, nil
	}
	if r.maxThreads > 0 && len(r.sessions) >= r.maxThreads {
		return nil, fmt.Errorf("maximum number of threads (%d) reached", r.maxThreads)
	}
	th := &aguiThread{id: id, session: r.newThreadSession()}
	// Client thread IDs are arbitrary strings, never filesystem path components.
	threadHash := sha256.Sum256([]byte(id))
	th.session.SessionID = fmt.Sprintf("agui-%s-%x", store.WorkspaceKey(r.base.RestrictedRoot), threadHash)
	if r.store != nil {
		if st, err := r.store.Load(id); err != nil {
			r.logf("thread %s: cannot load persisted state: %v", id, err)
		} else if st != nil {
			r.restoreThread(th, st)
		}
	}
	r.sessions[id] = th
	return th, nil
}

// PrepareThread materialises a thread (loading its persisted state, and with it
// the broker's open interrupts) before the server validates a resume.
func (r *aguiRunner) PrepareThread(threadID string) {
	if _, err := r.thread(threadID); err != nil {
		r.logf("thread %s: %v", threadID, err)
	}
}

// restoreThread rebuilds a thread from its persisted state: the authoritative
// system prompt is kept and the stored conversation is appended, while the
// broker regains its interrupts and "remember" grants.
func (r *aguiRunner) restoreThread(th *aguiThread, st *agui.ThreadState) {
	if len(st.Messages) > 0 {
		msgs := make([]types.Message, 0, len(st.Messages)+1)
		if len(th.session.ChatHistory) > 0 && th.session.ChatHistory[0].Role == "system" {
			msgs = append(msgs, th.session.ChatHistory[0])
		}
		for _, m := range st.Messages {
			if m.Role == "system" || m.Role == "developer" {
				continue
			}
			msgs = append(msgs, m)
		}
		th.session.ChatHistory = msgs
	}
	th.seeded = st.Seeded
	if r.broker != nil {
		r.broker.RestoreThread(th.id, st.Interrupts)
	}
}

// persistThread writes the thread state after a run. With the in-memory store
// (the default) it is a no-op, so behaviour is unchanged unless --state-dir was
// given.
func (r *aguiRunner) persistThread(th *aguiThread) {
	if r.store == nil {
		return
	}
	st := &agui.ThreadState{
		ThreadID:  th.id,
		UpdatedAt: time.Now().UTC(),
		Seeded:    th.seeded,
		Messages:  th.session.ChatHistory,
	}
	if r.broker != nil {
		st.Interrupts = r.broker.SnapshotThread(th.id)
	}
	if err := r.store.Save(th.id, st); err != nil {
		r.logf("thread %s: cannot persist state: %v", th.id, err)
	}
}

// logf logs through the runner logger when one is configured.
func (r *aguiRunner) logf(format string, args ...interface{}) {
	if r.logger != nil {
		r.logger.Printf(format, args...)
	}
}

// Run implements agui.Runner.
func (r *aguiRunner) Run(ctx context.Context, req agui.RunRequest, sink types.OutputSink) error {
	th, err := r.thread(req.ThreadID)
	if err != nil {
		return err
	}
	th.mu.Lock()
	defer th.mu.Unlock()
	// Persist the thread (conversation + open interrupts) when the run ends,
	// including when it is suspended by an interrupt.
	defer r.persistThread(th)

	s := th.session

	// Seed the history on the first run; afterwards only the latest user message
	// is appended, so fyc's in-memory history is never duplicated. A resumed run
	// (RunAgentInput.resume) carries no new user input: it only answers the
	// interrupts of the previous run.
	if !th.seeded {
		for _, m := range req.Messages {
			if m.Role == "system" {
				continue
			}
			s.ChatHistory = append(s.ChatHistory, m)
		}
		th.seeded = true
	} else if !req.Resume {
		if last := lastUserMessage(req.Messages); last != nil {
			s.ChatHistory = append(s.ChatHistory, *last)
		}
	}

	s.Sink = sink
	ctx = sessionLogContext(ctx, s)
	s.RunCtx = ctx
	defer func() {
		s.Sink = nil
		s.RunCtx = nil
	}()

	// Cancel the in-flight generation when the client disconnects (an interrupt
	// also cancels the run context, with a different cause).
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			finishGeneration(s)
		case <-done:
		}
	}()

	if req.Resume {
		// Level-2 HITL: this run continues the one that ended with an interrupt
		// outcome. The tool calls left pending are replayed here — their answers
		// are already in the broker — and only their TOOL_CALL_RESULT is emitted
		// (the protocol's audit rule: no second TOOL_CALL_START/ARGS/END).
		if pending := pendingToolCalls(s.ChatHistory); len(pending) > 0 {
			handleToolCallsWithSink(s, sink, pending, true, false)
			if interrupted := agui.RunInterrupted(ctx); interrupted != nil {
				interrupted.Snapshot = agui.SnapshotFromMessages(s.ChatHistory)
				return interrupted
			}
		}
	} else if pending := pendingToolCalls(s.ChatHistory); len(pending) > 0 {
		// An interrupt the client never resumed (expired, or the client started a
		// new turn) left tool calls without results: close them, otherwise the
		// history sent to the provider contains a tool call with no answer.
		closePendingToolCalls(s, pending)
	}

	streamChat(s)

	if interrupted := agui.RunInterrupted(ctx); interrupted != nil {
		// The run was suspended: the server turns this into RUN_FINISHED with the
		// interrupt outcome, after emitting the state the resume needs.
		interrupted.Snapshot = agui.SnapshotFromMessages(s.ChatHistory)
		return interrupted
	}
	return nil
}

// pendingToolCalls returns the tool calls of the last assistant message that have
// no recorded tool result yet: exactly the calls a resumed run must replay.
func pendingToolCalls(history []types.Message) []types.ToolCall {
	assistant := -1
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "assistant" && len(history[i].ToolCalls) > 0 {
			assistant = i
			break
		}
	}
	if assistant < 0 {
		return nil
	}

	calls := history[assistant].ToolCalls
	results := 0
	for i := assistant + 1; i < len(history); i++ {
		if history[i].Role != "tool" {
			break
		}
		results++
	}
	if results >= len(calls) {
		return nil
	}

	pending := make([]types.ToolCall, len(calls)-results)
	copy(pending, calls[results:])
	return pending
}

// closePendingToolCalls records an error result for every tool call left without
// one, so the conversation stays valid for the provider.
func closePendingToolCalls(s *ChatSession, pending []types.ToolCall) {
	for _, tc := range pending {
		msg := types.Message{
			Role:    "tool",
			Name:    tc.Function.Name,
			Content: "error: the tool call was interrupted and never approved (the interrupt expired without a resume)",
		}
		if tc.ID != "" {
			msg.ToolCallID = tc.ID
		}
		s.ChatHistory = append(s.ChatHistory, msg)
	}
}

// lastUserMessage returns the most recent user message, or nil.
func lastUserMessage(msgs []types.Message) *types.Message {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return &msgs[i]
		}
	}
	return nil
}

// runServe implements `fuckyourcli serve`.
func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:8787", "listen address")
	token := fs.String("token", "", "bearer token required for every request (or FYC_AGUI_TOKEN)")
	permissions := fs.String("permissions", "deny", "headless approval policy: auto | deny")
	cors := fs.String("cors", "", "allowed CORS origin (default: none)")
	maxThreads := fs.Int("max-threads", 64, "maximum number of in-memory threads")
	hitl := fs.Bool("hitl", true, "ask the client to answer tool approvals/form inputs (level 1 control plane, or level 2 interrupts with --interrupts); --hitl=false restores the deterministic deny/auto policy")
	interrupts := fs.Bool("interrupts", false, "level-2 HITL: answer approvals with standard AG-UI interrupts (RUN_FINISHED with outcome:{type:\"interrupt\"} + RunAgentInput.resume) instead of the level-1 round trip")
	interactionTimeout := fs.Duration("interaction-timeout", 5*time.Minute, "how long a parked interaction waits for the client answer")
	stateDir := fs.String("state-dir", "", "persist AG-UI threads (and their open interrupts) under this directory; empty keeps them in memory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	tok := strings.TrimSpace(*token)
	if tok == "" {
		tok = strings.TrimSpace(os.Getenv("FYC_AGUI_TOKEN"))
	}
	if tok == "" {
		return errors.New("a bearer token is required: pass --token or set FYC_AGUI_TOKEN")
	}
	if *interrupts && !*hitl {
		return errors.New("--interrupts requires --hitl=true: the level-2 interrupt lifecycle is a human-in-the-loop mode")
	}

	policy := "deny"
	if strings.EqualFold(strings.TrimSpace(*permissions), "auto") {
		policy = "auto"
	}

	// Install the interactive prompt router before any tool can run, so a
	// non-TTY process never blocks on stdin/stdout.
	//
	//   - level 1 (default): the tool goroutine parks and the client answers on
	//     the run's SSE stream through the fyc control plane;
	//   - level 2 (--interrupts): the run is suspended and ends with
	//     RUN_FINISHED{outcome:{type:"interrupt"}}; the client answers by starting
	//     a new run with RunAgentInput.resume;
	//   - --hitl=false or a fallback (no run, timeout, disconnect): the
	//     deterministic auto/deny policy.
	broker := agui.NewBroker(*interactionTimeout)
	broker.SetInterruptMode(*interrupts)
	tools.SetInteractor(newHITLInteractor(broker, policy, *hitl))

	// "Allow always" answers must never leak into the shared configuration: in
	// server mode they are remembered per thread by the broker only.
	tools.SetPermissionPersistence(false)
	cfg := config.Get()
	logger := log.New(os.Stderr, "[agui] ", log.LstdFlags)
	var threadStore agui.ThreadStore = agui.MemoryStore{}
	if dir := strings.TrimSpace(*stateDir); dir != "" {
		threadStore = agui.NewFileStore(dir)
	}
	runner := newAGUIRunner(cfg, logger, *maxThreads, threadStore, broker)

	defer crawl.Shutdown()
	defer tools.CloseLSPManagers()
	defer runner.base.MCPManager.Shutdown(context.Background())

	exposed := runner.base.getToolsForAI()
	hitlMode := "off"
	if *hitl {
		hitlMode = "l1"
	}
	if *interrupts {
		hitlMode = "l2"
	}

	fmt.Printf("AG-UI backend on http://%s (permissions=%s, hitl=%s, interaction-timeout=%s)\n", *addr, policy, hitlMode, (*interactionTimeout).Round(time.Second))
	fmt.Printf("workspace: %s\n", runner.base.RestrictedRoot)
	fmt.Printf("provider: %s • model: %s\n", config.GetCurrentProviderInfo(), cfg.Model)
	fmt.Printf("tools exposed to the model: %d\n", len(exposed))
	if dir := strings.TrimSpace(*stateDir); dir != "" {
		fmt.Printf("state dir: %s (threads persisted)\n", dir)
	} else {
		fmt.Printf("state: in memory only (pass --state-dir to persist threads)\n")
	}
	logger.Printf("listening on %s (permissions=%s, hitl=%s, max-threads=%d)", *addr, policy, hitlMode, *maxThreads)

	return agui.Serve(agui.Options{
		Addr:               *addr,
		Token:              tok,
		CORS:               *cors,
		Permissions:        policy,
		MaxThreads:         *maxThreads,
		Interactions:       broker,
		InteractionTimeout: *interactionTimeout,
		Logger:             logger,
	}, runner)
}

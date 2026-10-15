package agui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"fuckyourcli/types"
)

// maxRequestBytes caps the size of a RunAgentInput body.
const maxRequestBytes = 8 << 20 // 8 MiB

// Options configures the AG-UI HTTP server.
type Options struct {
	// Addr is the listen address (default 127.0.0.1:8787).
	Addr string
	// Token is the mandatory bearer token (see FYC_AGUI_TOKEN).
	Token string
	// CORS is the allowed origin; empty disables CORS, "*" allows any.
	CORS string
	// Permissions is the headless approval policy ("auto" or "deny"). It is
	// consumed by package main to configure tools.NewHeadlessInteractor.
	Permissions string
	// MaxThreads is the maximum number of concurrent in-memory threads.
	MaxThreads int
	// Interactions is the level-1 human-in-the-loop broker. When nil the server
	// creates one using InteractionTimeout as the per-interaction deadline.
	Interactions *Broker
	// InteractionTimeout is the answer budget for each parked interaction
	// (level 1) and the TTL of each interrupt (level 2, default 5m).
	InteractionTimeout time.Duration
	// Logger receives server-level messages; nil silences them.
	Logger *log.Logger
}

func (o Options) logger() *log.Logger { return o.Logger }

// RunRequest is one agent run handed to a Runner.
type RunRequest struct {
	ThreadID string
	RunID    string
	// Messages is the full AG-UI conversation history converted to the internal
	Messages []types.Message
	// Resume is true when the run carries RunAgentInput.resume, i.e. it continues
	// a run that ended with an interrupt outcome. The Runner then replays the
	// pending tool calls instead of treating the history as new input.
	Resume bool
}

// Runner executes one agent run, streaming output into sink.
type Runner interface {
	Run(ctx context.Context, req RunRequest, sink types.OutputSink) error
}

// ThreadPreparer is implemented by runners that keep per-thread state. The
// server calls PrepareThread before validating a resume, so a store-backed
// runner can load the thread (and its open interrupts) into memory first.
type ThreadPreparer interface {
	PrepareThread(threadID string)
}

// Server is the AG-UI HTTP server.
type Server struct {
	opts   Options
	runner Runner
	broker *Broker
}

// NewServer builds a Server.
func NewServer(opts Options, runner Runner) *Server {
	if opts.Addr == "" {
		opts.Addr = "127.0.0.1:8787"
	}
	if opts.InteractionTimeout <= 0 {
		opts.InteractionTimeout = 5 * time.Minute
	}
	if opts.Interactions == nil {
		opts.Interactions = NewBroker(opts.InteractionTimeout)
	}
	return &Server{opts: opts, runner: runner, broker: opts.Interactions}
}

// Broker exposes the level-1 human-in-the-loop broker.
func (s *Server) Broker() *Broker { return s.broker }

// Handler returns the HTTP handler (useful for tests).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("POST /runs/{runId}/interactions/{interactionId}", s.handleInteraction)
	mux.HandleFunc("/", s.handleRun)
	return mux
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	s.applyCORS(w, r)
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ok\n")
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	corsApplied := s.applyCORS(w, r)
	if r.Method == http.MethodOptions {
		if corsApplied {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes))
	if err != nil {
		http.Error(w, "cannot read request body", http.StatusBadRequest)
		return
	}
	var input RunAgentInput
	if err := json.Unmarshal(body, &input); err != nil {
		http.Error(w, "invalid RunAgentInput: "+err.Error(), http.StatusBadRequest)
		return
	}
	if input.ThreadID == "" {
		input.ThreadID = NewID("thread")
	}
	if input.RunID == "" {
		input.RunID = NewID("run")
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	sink := NewSink(w, flusher.Flush, input.ThreadID, input.RunID)
	sink.Emit(NewRunStarted(input.ThreadID, input.RunID))

	// Level-2 HITL: a thread with unresolved interrupts only accepts a run that
	// resumes them (contract rule 4), and a resume must address every open
	// interrupt (rule 3) of this thread (rule 2). Anything else is a RUN_ERROR
	// (rule 8).
	if s.broker.InterruptMode() {
		// A store-backed runner materialises the thread (and its persisted open
		// interrupts) before the resume is validated: after a restart the broker
		// is empty until the thread is loaded.
		if tp, ok := s.runner.(ThreadPreparer); ok {
			tp.PrepareThread(input.ThreadID)
		}
		if err := s.prepareResume(input); err != nil {
			s.logf("run %s (thread %s) rejected: %v", input.RunID, input.ThreadID, err)
			sink.Emit(NewRunError(err.Error(), ""))
			return
		}
	}

	req := RunRequest{
		ThreadID: input.ThreadID,
		RunID:    input.RunID,
		Messages: MessagesFromInput(input.Messages),
		Resume:   len(input.Resume) > 0,
	}

	// Attach the run to the context so tool interactions can reach this run's
	// SSE stream (L1), be canceled when the client disconnects, and suspend the
	// run with a standard interrupt (L2).
	runCtx := WithRun(r.Context(), sink, input.ThreadID, input.RunID)
	runErr := s.runner.Run(runCtx, req, sink)
	sink.Finalize()

	if runErr != nil {
		// A run suspended by a level-2 interrupt ends with the interrupt outcome
		// (never RUN_ERROR): the state needed to resume is emitted first, as the
		// protocol requires.
		var interrupted *InterruptError
		if errors.As(runErr, &interrupted) {
			if len(interrupted.Snapshot) > 0 {
				sink.Emit(NewMessagesSnapshot(interrupted.Snapshot))
			}
			sink.Emit(NewRunFinishedInterrupt(input.ThreadID, input.RunID, sink.Metadata(), interrupted.Interrupts))
			return
		}
		if !errors.Is(runErr, context.Canceled) {
			s.logf("run %s (thread %s) failed: %v", input.RunID, input.ThreadID, runErr)
		}
		sink.Emit(NewRunError(runErr.Error(), ""))
		return
	}
	sink.Emit(NewRunFinished(input.ThreadID, input.RunID, sink.Metadata()))
}

// handleInteraction receives the client answer to a parked interaction (level-1
// human-in-the-loop): POST /runs/{runId}/interactions/{interactionId} with an
// InteractionAnswer body. The parked tool goroutine resumes immediately.
func (s *Server) handleInteraction(w http.ResponseWriter, r *http.Request) {
	corsApplied := s.applyCORS(w, r)
	if r.Method == http.MethodOptions {
		if corsApplied {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes))
	if err != nil {
		http.Error(w, "cannot read request body", http.StatusBadRequest)
		return
	}
	var answer InteractionAnswer
	if len(body) > 0 {
		if err := json.Unmarshal(body, &answer); err != nil {
			http.Error(w, "invalid InteractionAnswer: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	runID := r.PathValue("runId")
	interactionID := r.PathValue("interactionId")
	if s.broker == nil {
		http.Error(w, "interactions are not enabled", http.StatusNotFound)
		return
	}
	if err := s.broker.Resolve(runID, interactionID, answer); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if s.opts.logger() != nil {
		s.opts.logger().Printf("interaction %s (run %s) resolved", interactionID, runID)
	}
	w.WriteHeader(http.StatusNoContent)
}

// prepareResume validates RunAgentInput.resume against the open interrupts of
// the thread and applies the answers to the broker, so the resumed run finds
// them ready when it replays the pending tool calls.
func (s *Server) prepareResume(input RunAgentInput) error {
	open := s.broker.OpenInterrupts(input.ThreadID)
	if len(open) == 0 {
		// No open interrupt: the resume may still reference interrupts this
		// thread already resolved (idempotent replay), which the broker accepts.
		return s.broker.Resume(input.ThreadID, input.Resume)
	}
	if len(input.Resume) == 0 {
		return fmt.Errorf("thread %s has %d unresolved interrupt(s): this run must carry resume", input.ThreadID, len(open))
	}
	if err := s.broker.Resume(input.ThreadID, input.Resume); err != nil {
		return err
	}
	if left := s.broker.OpenInterrupts(input.ThreadID); len(left) > 0 {
		return fmt.Errorf("resume must address every open interrupt of thread %s (%d left)", input.ThreadID, len(left))
	}
	return nil
}

// logf writes a server-level message when a logger is configured.
func (s *Server) logf(format string, args ...interface{}) {
	if logger := s.opts.logger(); logger != nil {
		logger.Printf(format, args...)
	}
}

// Serve starts the HTTP server and blocks until an interrupt/terminate signal
// arrives or the server fails. It then shuts down gracefully.
func Serve(opts Options, runner Runner) error {
	srv := NewServer(opts, runner)

	httpServer := &http.Server{
		Addr:    srv.opts.Addr,
		Handler: srv.Handler(),
		// No read/write timeouts: SSE responses are long-lived. The read header
		// timeout still protects against slowloris attacks.
		ReadHeaderTimeout: 15 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-errCh:
		srv.broker.Close()
		return fmt.Errorf("ag-ui server: %w", err)
	case <-ctx.Done():
	}

	// Deny every interaction still parked, then shut the HTTP server down.
	srv.broker.Close()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
	return nil
}

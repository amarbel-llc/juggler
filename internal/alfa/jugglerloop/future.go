package jugglerloop

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Futures (FDR 0022, first slice). With Config.Futures, a tool-server call that
// outruns Config.CallWait is not abandoned: the loop keeps the work running,
// answers the model with a handle ({"state":"waiting","future":"f-1"}), and the
// model can wait again with future_await or stop it with future_cancel. This
// removes the double-execution hazard of abandon-and-retry (juggler#2). A tool
// named in Config.AbandonTools keeps the abandon behaviour: its expired call is
// cancelled and answered as an isError result.
//
// Only this slice: the wait is one fixed budget (CallWait). Idle/ceiling
// deadlines driven by progress, the host broker and event delivery of a late
// completion are later slices of the record.

const (
	// DefaultCallWait is how long the loop waits on one tool call before
	// answering "waiting" (or abandoning, for an AbandonTools tool): the 30 s
	// per-call budget juggler#2 introduced.
	DefaultCallWait = 30 * time.Second

	// FutureAwaitTool and FutureCancelTool are the tools the loop offers when
	// Config.Futures is set. They act only on futures this run started.
	FutureAwaitTool  = "future_await"
	FutureCancelTool = "future_cancel"

	// futureCancelGrace bounds how long the loop waits for a cancelled call to
	// return before it reports the cancel as still in progress.
	futureCancelGrace = 2 * time.Second
)

// FutureAwaitSpec and FutureCancelSpec are the injected future tools.
var (
	FutureAwaitSpec = ToolSpec{
		Name:        FutureAwaitTool,
		Description: "Wait for a tool call that is still running (state \"waiting\") and return its result. If it is still running after the wait you get the waiting state again.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"future":{"type":"string","description":"The future id from a waiting result."},"timeout_ms":{"type":"integer","description":"How long to wait, in milliseconds. Defaults to the loop's call wait."}},"required":["future"]}`),
	}
	FutureCancelSpec = ToolSpec{
		Name:        FutureCancelTool,
		Description: "Stop a tool call that is still running (state \"waiting\"). Cancelling is best effort: the tool server may already have done the work.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"future":{"type":"string","description":"The future id from a waiting result."}},"required":["future"]}`),
	}
)

// future is one tool call the loop owns. content, isError and err are written
// by the call's goroutine before done is closed and read only after.
type future struct {
	id       string
	call     *ToolCall
	entryIdx int // index of its LedgerCall
	cancel   context.CancelFunc
	started  time.Time
	done     chan struct{}

	content   json.RawMessage
	isError   bool
	err       error
	cancelled bool // the loop (or the run ending) asked it to stop
	settled   bool // its ledger entry reflects the final outcome
}

func (f *future) finished() bool {
	select {
	case <-f.done:
		return true
	default:
		return false
	}
}

// executeAsFuture runs a tool call as a future: if it finishes within
// CallWait it is settled exactly like a synchronous call, otherwise it is
// parked and the model gets a handle.
func (r *run) executeAsFuture(ctx context.Context, callTurn Turn, entry LedgerCall) (EndReason, bool) {
	call := callTurn.Body.ToolCall
	fctx, cancel := context.WithCancel(ctx)
	r.futureSeq++
	f := &future{
		id:      fmt.Sprintf("f-%d", r.futureSeq),
		call:    call,
		cancel:  cancel,
		started: r.cfg.Now(),
		done:    make(chan struct{}),
	}
	go func() {
		f.content, f.isError, f.err = r.cfg.Exec.Call(fctx, call.Name, call.Args)
		close(f.done)
	}()

	timer := time.NewTimer(r.cfg.CallWait)
	defer timer.Stop()
	select {
	case <-f.done:
		cancel()
		return r.settle(ctx, callTurn, entry, f.content, f.isError, f.err)
	case <-timer.C:
		return r.park(callTurn, entry, f)
	case <-ctx.Done():
		f.cancelled = true
		cancel()
		select {
		case <-f.done:
		case <-time.After(futureCancelGrace):
		}
		return r.settle(ctx, callTurn, entry, nil, false, ctx.Err())
	}
}

// park records a still-running call: a ledger entry that finishFutures or a
// later await settles in place, and a tool_result carrying the handle.
func (r *run) park(callTurn Turn, entry LedgerCall, f *future) (EndReason, bool) {
	entry.Future = f.id
	entry.Error = fmt.Sprintf("still running after %s", r.cfg.CallWait)
	f.entryIdx = len(r.ledger.Calls)
	r.ledger.Calls = append(r.ledger.Calls, entry)
	r.futures = append(r.futures, f)

	content := waitingContent(f, r.cfg.Now())
	// OK: the model's call succeeded in starting; the ledger entry stays not-ok
	// until the work finishes.
	t := r.append(ToolResultBody(ToolResult{CallID: callTurn.Body.ToolCall.CallID, OK: true, Content: content}), callTurn.ID)
	r.traffic(entry, callTurn.Body.ToolCall.Args, len(content), false, t.At)
	return "", false
}

func waitingContent(f *future, now time.Time) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"state":      "waiting",
		"future":     f.id,
		"elapsed_ms": now.Sub(f.started).Milliseconds(),
		"note":       "still running; call future_await to wait again or future_cancel to stop it",
	})
	return b
}

func (r *run) futureByID(id string) *future {
	for _, f := range r.futures {
		if f.id == id {
			return f
		}
	}
	return nil
}

// settleFuture folds a finished future's outcome into its ledger entry,
// once. It reports whether the entry is now a successful call.
func (r *run) settleFuture(f *future) bool {
	if f.settled || !f.finished() {
		return false
	}
	f.settled = true
	e := &r.ledger.Calls[f.entryIdx]
	switch {
	case f.cancelled:
		e.OK, e.Error = false, "cancelled"
	case f.err != nil:
		e.OK, e.Error = false, f.err.Error()
	case f.isError:
		e.OK, e.Error = false, toolResultText(ToolResult{Content: f.content})
	default:
		e.OK, e.Error = true, ""
		if KindProducesArtifacts(r.kinds[f.call.Name]) {
			if uris := r.cfg.URIExtractor(f.call.Name, f.content); uris != nil {
				e.URIs = uris
			}
		}
	}
	return e.OK
}

// harvest settles every parked future that finished while the model was
// elsewhere, so its artifacts reach the ledger without an await. A success
// may satisfy StopWhen, ending the run.
func (r *run) harvest() (EndReason, bool) {
	for _, f := range r.futures {
		if r.settleFuture(f) && r.stopNow() {
			return EndEvaluatorPass, true
		}
	}
	return "", false
}

// finishFutures runs when the loop ends: still-running futures are cancelled
// (bounded wait) and every entry is settled, so the ledger is final. A call
// that did not stop in time keeps an explicit "still running" error.
func (r *run) finishFutures() {
	for _, f := range r.futures {
		if f.settled {
			continue
		}
		if !f.finished() {
			f.cancelled = true
			f.cancel()
			select {
			case <-f.done:
			case <-time.After(futureCancelGrace):
			}
		}
		if f.finished() {
			r.settleFuture(f)
		} else {
			e := &r.ledger.Calls[f.entryIdx]
			e.OK, e.Error = false, "still running when the run ended"
		}
	}
}

// executeFutureTool handles the loop's own future_await / future_cancel
// calls. Neither is a tool-server call, so neither is ledgered.
func (r *run) executeFutureTool(ctx context.Context, callTurn Turn) (EndReason, bool) {
	call := callTurn.Body.ToolCall
	var args struct {
		Future    string `json:"future"`
		TimeoutMS int64  `json:"timeout_ms"`
	}
	_ = json.Unmarshal(call.Args, &args)

	reply := func(ok bool, v any) (EndReason, bool) {
		content, _ := json.Marshal(v)
		r.append(ToolResultBody(ToolResult{CallID: call.CallID, OK: ok, Content: content}), callTurn.ID)
		return "", false
	}
	f := r.futureByID(args.Future)
	if f == nil {
		return reply(false, map[string]string{"error": "unknown future " + fmt.Sprintf("%q", args.Future) + ": only futures this run started can be awaited or cancelled"})
	}

	if call.Name == FutureCancelTool {
		if f.finished() {
			r.settleFuture(f)
			return reply(true, map[string]string{"state": "already_done", "note": "call future_await for its result"})
		}
		f.cancelled = true
		f.cancel()
		select {
		case <-f.done:
			r.settleFuture(f)
			return reply(true, map[string]string{"state": "cancelled"})
		case <-time.After(futureCancelGrace):
			return reply(true, map[string]string{"state": "cancelling", "note": "the tool has not stopped yet"})
		case <-ctx.Done():
			return reply(true, map[string]string{"state": "cancelling"})
		}
	}

	// future_await
	wait := r.cfg.CallWait
	if args.TimeoutMS > 0 {
		wait = time.Duration(args.TimeoutMS) * time.Millisecond
	}
	if !f.finished() {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-f.done:
		case <-timer.C:
			return reply(true, json.RawMessage(waitingContent(f, r.cfg.Now())))
		case <-ctx.Done():
			return reply(false, map[string]string{"error": "run ended while waiting"})
		}
	}
	ok := r.settleFuture(f)
	switch {
	case f.cancelled:
		return reply(true, map[string]string{"state": "cancelled"})
	case f.err != nil:
		// A transport fault: the same terminal outcome a synchronous call has.
		r.append(ToolResultBody(ToolResult{CallID: call.CallID, OK: false, Error: f.err.Error()}), callTurn.ID)
		if ctx.Err() != nil {
			return EndTimeout, true
		}
		return EndToolError, true
	}
	r.append(ToolResultBody(ToolResult{CallID: call.CallID, OK: !f.isError, Content: f.content}), callTurn.ID)
	if ok && r.stopNow() {
		return EndEvaluatorPass, true
	}
	return "", false
}

package jugglerloop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	rm "code.linenisgreat.com/juggler/internal/0/juggler"
)

const (
	DefaultMaxSteps  = 12
	DefaultWallClock = 2 * time.Minute
	DefaultMaxTokens = 4096

	// CannotCompleteTool is the tool the loop always offers the model.
	// Calling it ends the run and records its reason in the ledger.
	CannotCompleteTool = "cannot_complete"
)

// CannotCompleteSpec is the injected cannot_complete tool definition.
var CannotCompleteSpec = ToolSpec{
	Name:        CannotCompleteTool,
	Description: "Call this when you cannot complete the task. It ends the run; explain why in reason.",
	InputSchema: json.RawMessage(`{"type":"object","properties":{"reason":{"type":"string","description":"Why the task cannot be completed."}},"required":["reason"]}`),
}

// ToolExecutor runs one tool call. A returned err is a transport failure
// (an unrecovered tool error that ends the run); isError=true is a normal
// tool-level error whose content the model sees as a tool_result.
type ToolExecutor interface {
	Call(ctx context.Context, name string, args json.RawMessage) (content json.RawMessage, isError bool, err error)
}

// Config is one run's inputs. Codec, HTTPClient, MaxSteps, WallClock,
// MaxTokens, URIExtractor, NewTurnID and Now default when zero; Codec
// defaults to CodecFor(Resolved). Principal and Tools' executor are
// required.
type Config struct {
	Codec      Codec
	HTTPClient *http.Client
	Resolved   rm.ResolveModelResult
	Model      string

	SystemPrompt string
	// Task is sent to the model as the opening user message. It is the
	// brief's content, so it is not re-recorded as a transcript turn; the
	// first transcript turn's Parent is BriefTurnID.
	Task  string
	Tools []ToolSpec
	Exec  ToolExecutor

	// MaxSteps caps model round trips: one step is one request/response
	// with the provider, regardless of how many tool calls it carries. The
	// cap is checked before each request, so the tool calls of the final
	// permitted step still execute and the run then ends with step_cap.
	MaxSteps int
	// WallClock bounds the whole run, model requests and tool calls alike;
	// on expiry the in-flight call is cancelled and the run ends with
	// timeout. Cancelling the parent ctx ends the run the same way, but Run
	// then also returns the ctx's error so the caller can tell a holder's
	// cancel from the brief's own limit.
	WallClock time.Duration
	MaxTokens int

	// Principal is the sender of every turn the run appends.
	Principal   string
	BriefTurnID string
	// OnTurn, when set, is called synchronously with each turn as it is
	// appended, terminal turn included.
	OnTurn func(Turn)
	// OnTraffic, when set, is called synchronously once per tool call (in
	// the same places a LedgerCall is appended; clown#255), so a caller can
	// persist the call's full arguments somewhere the ledger itself does
	// not carry them.
	OnTraffic func(TrafficRecord)

	URIExtractor URIExtractor
	// StepMeta, when set, is called once after each model request (success or
	// failure) and returns the provider harness's account of that request,
	// or nil when it has none. The loop totals it into Ledger.Provider and
	// stamps it on the first turn of the reply (Turn.Provider; FDR 0021 §4).
	// It is provider-agnostic on purpose: the host wires it from whatever
	// wraps the HTTP client.
	StepMeta func() *ProviderMeta

	// ResultFormat opts the run in to a structured result (ResultFormatJSON):
	// when it ends end_turn, the final model message must be one JSON value,
	// which becomes Terminal.Result and Ledger.End.Result; otherwise
	// ResultError says why not. Empty means no structured result (the
	// default; nothing changes).
	ResultFormat string

	// Futures turns tool-server calls into loop-owned futures (FDR 0022): a
	// call that outruns CallWait is parked, not abandoned, and the model is
	// offered future_await and future_cancel. Off, calls are synchronous as
	// before.
	Futures bool
	// CallWait is how long the loop waits on one call before parking it
	// (abandoning it, for an AbandonTools tool); DefaultCallWait when zero.
	CallWait time.Duration
	// AbandonTools are tools whose call past CallWait is cancelled and
	// answered as an isError result, juggler#2's behaviour, instead of being
	// parked. Only meaningful with Futures.
	AbandonTools map[string]bool
	// StopWhen, when set, is called with the in-progress ledger (the final
	// ledger's shape, end reason empty) after each successful tool result; true
	// ends the run with evaluator_pass. An error is recorded as a ledger note
	// and the run goes on. The loop does not know what decides it (the brief's
	// evaluator, for `juggler run`).
	StopWhen  func(ledger json.RawMessage) (bool, error)
	NewTurnID func() string
	Now       func() time.Time
}

// Result is what a run recorded. The loop never decides success.
type Result struct {
	Transcript Transcript
	Ledger     Ledger
	End        EndReason
}

type run struct {
	cfg        Config
	transcript Transcript
	ledger     Ledger
	kinds      map[string]string
	// offered is the exact set of names sent to the model this run (cfg.Tools
	// plus the injected cannot_complete), the same set toolsWithCannotComplete
	// builds for the wire request: a call for any other name is refused
	// before Exec.Call (clown#261). Neither codec renames a tool between
	// advertising it and decoding a tool_use/tool_call back (verified by
	// reading openaicompat.go and anthropic.go: both send and parse t.Name
	// verbatim), so this is also exactly what the model was shown.
	offered    map[string]struct{}
	lastTurnID string
	start      time.Time

	futureSeq int
	futures   []*future

	// finalText is the text of the model reply that ended the run with
	// end_turn (the source of the structured result).
	finalText string
}

// Run drives the model's tool-use protocol until the model ends its turn
// (a reply with no tool calls), calls cannot_complete, StopWhen passes after
// a successful tool result, exhausts MaxSteps,
// the wall clock or ctx expires, or the executor reports a transport
// failure. The returned error is non-nil only for an invalid Config, a
// model endpoint failure (End = model_error), or a cancelled parent ctx;
// the Result is populated in every case but the first.
func Run(ctx context.Context, cfg Config) (Result, error) {
	cfg, err := cfg.withDefaults()
	if err != nil {
		return Result{}, err
	}
	start := cfg.Now()
	runCtx, cancel := context.WithTimeout(ctx, cfg.WallClock)
	defer cancel()

	r := &run{
		cfg:        cfg,
		transcript: Transcript{Schema: TranscriptSchema, Turns: []Turn{}},
		ledger:     newLedger(),
		kinds:      map[string]string{},
		offered:    map[string]struct{}{},
		lastTurnID: cfg.BriefTurnID,
		start:      start,
	}
	tools := toolsWithCannotComplete(cfg.Tools)
	if cfg.Futures {
		tools = withFutureTools(tools)
	}
	for _, t := range tools {
		if t.Kind != "" {
			r.kinds[t.Name] = t.Kind
		}
		r.offered[t.Name] = struct{}{}
	}

	end, runErr := r.loop(runCtx, tools)
	if end == EndTimeout && ctx.Err() != nil {
		runErr = ctx.Err()
	}
	r.finishFutures()
	result, resultErr := r.terminalResult(end)
	r.append(terminalBodyWithResult(end, result, resultErr), r.lastTurnID)
	r.ledger.End = LedgerEnd{Reason: end, Result: result, ResultError: resultErr}
	r.ledger.ElapsedMS = cfg.Now().Sub(start).Milliseconds()
	r.ledger.TranscriptDigest = TranscriptDigest(r.transcript)
	return Result{Transcript: r.transcript, Ledger: r.ledger, End: end}, runErr
}

func (c Config) withDefaults() (Config, error) {
	if c.Principal == "" {
		return c, errors.New("jugglerloop: Principal is required")
	}
	if c.Exec == nil {
		return c, errors.New("jugglerloop: Exec is required")
	}
	if c.Codec == nil {
		codec, err := CodecFor(c.Resolved)
		if err != nil {
			return c, fmt.Errorf("jugglerloop: %w", err)
		}
		c.Codec = codec
	}
	if c.HTTPClient == nil {
		c.HTTPClient = http.DefaultClient
	}
	if c.MaxSteps <= 0 {
		c.MaxSteps = DefaultMaxSteps
	}
	if c.CallWait <= 0 {
		c.CallWait = DefaultCallWait
	}
	if c.WallClock <= 0 {
		c.WallClock = DefaultWallClock
	}
	if c.MaxTokens <= 0 {
		c.MaxTokens = DefaultMaxTokens
	}
	if c.URIExtractor == nil {
		c.URIExtractor = ExtractURIsFromTopLevelFields
	}
	if c.NewTurnID == nil {
		c.NewTurnID = NewRandomTurnID
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c, nil
}

func toolsWithCannotComplete(tools []ToolSpec) []ToolSpec {
	out := make([]ToolSpec, 0, len(tools)+1)
	for _, t := range tools {
		if t.Name != CannotCompleteTool {
			out = append(out, t)
		}
	}
	return append(out, CannotCompleteSpec)
}

// ResultFormatJSON is the one structured-result format: the final model
// message is a single JSON value.
const ResultFormatJSON = "json"

// MaxResultBytes caps a structured result: the ledger is the evaluator's input
// and must not grow with a model's long final message.
const MaxResultBytes = 64 << 10

// terminalResult extracts the structured result when the run opted in and
// ended end_turn. It returns the compact JSON value, or a reason there is none.
// A run that did not opt in, or ended any other way, returns nothing.
func (r *run) terminalResult(end EndReason) (json.RawMessage, string) {
	if r.cfg.ResultFormat != ResultFormatJSON || end != EndTurn {
		return nil, ""
	}
	text := strings.TrimSpace(r.finalText)
	if text == "" {
		return nil, "the final message is empty"
	}
	if len(text) > MaxResultBytes {
		return nil, fmt.Sprintf("the final message is %d bytes, over the %d-byte result cap", len(text), MaxResultBytes)
	}
	dec := json.NewDecoder(strings.NewReader(text))
	var v json.RawMessage
	if err := dec.Decode(&v); err != nil {
		return nil, "the final message is not valid JSON: " + err.Error()
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, "the final message has text after its JSON value"
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, v); err != nil {
		return nil, "the final message is not valid JSON: " + err.Error()
	}
	return json.RawMessage(compact.Bytes()), ""
}

// withFutureTools adds the future tools, dropping any same-named tool the
// brief listed.
func withFutureTools(tools []ToolSpec) []ToolSpec {
	out := make([]ToolSpec, 0, len(tools)+2)
	for _, t := range tools {
		if t.Name != FutureAwaitTool && t.Name != FutureCancelTool {
			out = append(out, t)
		}
	}
	return append(out, FutureAwaitSpec, FutureCancelSpec)
}

func (r *run) append(body Body, parent string) Turn {
	return r.appendWithMeta(body, parent, nil)
}

func (r *run) appendWithMeta(body Body, parent string, meta *ProviderMeta) Turn {
	t := Turn{
		ID:       r.cfg.NewTurnID(),
		Sender:   r.cfg.Principal,
		Parent:   parent,
		At:       r.cfg.Now(),
		Body:     body,
		Provider: meta,
	}
	r.transcript.Turns = append(r.transcript.Turns, t)
	r.lastTurnID = t.ID
	if r.cfg.OnTurn != nil {
		r.cfg.OnTurn(t)
	}
	return t
}

// wireTurns is the conversation sent to the model: the brief's task as the
// opening user turn (sender "" never equals Principal) followed by the
// transcript.
func (r *run) wireTurns() []Turn {
	task := Turn{ID: r.cfg.BriefTurnID, Body: TextBody(r.cfg.Task)}
	return append([]Turn{task}, r.transcript.Turns...)
}

func (r *run) loop(ctx context.Context, tools []ToolSpec) (EndReason, error) {
	for {
		if ctx.Err() != nil {
			return EndTimeout, nil
		}
		if r.ledger.Steps >= r.cfg.MaxSteps {
			return EndStepCap, nil
		}
		if end, done := r.harvest(); done {
			return end, nil
		}

		r.ledger.Steps++
		reply, err := RoundTrip(ctx, r.cfg.HTTPClient, r.cfg.Codec, r.cfg.Resolved, r.cfg.Model, ModelRequest{
			System:    r.cfg.SystemPrompt,
			Agent:     r.cfg.Principal,
			Turns:     r.wireTurns(),
			Tools:     tools,
			MaxTokens: r.cfg.MaxTokens,
		})
		meta := r.stepMeta()
		if err != nil {
			if ctx.Err() != nil {
				return EndTimeout, nil
			}
			return EndModelError, err
		}

		var calls []Turn
		for i, t := range reply.Turns {
			var turnMeta *ProviderMeta
			if i == 0 {
				turnMeta = meta
			}
			appended := r.appendWithMeta(t.Body, r.lastTurnID, turnMeta)
			if appended.Body.Type == BodyToolCall {
				calls = append(calls, appended)
			}
		}
		if len(calls) == 0 {
			if reply.Stop == StopMaxTokens {
				// Cut off by the provider's token limit: not a finished answer.
				return EndMaxTokens, nil
			}
			var texts []string
			for _, t := range reply.Turns {
				if t.Body.Type == BodyText {
					texts = append(texts, t.Body.Text)
				}
			}
			r.finalText = strings.Join(texts, "\n")
			return EndTurn, nil
		}

		for _, callTurn := range calls {
			if end, done := r.execute(ctx, callTurn); done {
				return end, nil
			}
		}
	}
}

// stepMeta asks Config.StepMeta for the last model request's account and
// totals it into the ledger; nil when no harness reports one.
func (r *run) stepMeta() *ProviderMeta {
	if r.cfg.StepMeta == nil {
		return nil
	}
	m := r.cfg.StepMeta()
	if m == nil {
		return nil
	}
	if r.ledger.Provider == nil {
		r.ledger.Provider = &LedgerProvider{}
	}
	p := r.ledger.Provider
	if p.Name == "" {
		p.Name = m.Name
	}
	p.Requests++
	p.Attempts += m.Attempts
	p.PromptTokens += m.PromptTokens
	p.CompletionTokens += m.CompletionTokens
	p.CostUSD += m.CostUSD
	return m
}

// validateToolArgs reports why args cannot be a tool call's arguments: they
// must be a JSON object. The openai-compat codec keeps unparseable argument
// text as a JSON string, so a string is re-parsed to surface the real parse
// error.
func validateToolArgs(args json.RawMessage) error {
	var obj map[string]json.RawMessage
	err := json.Unmarshal(args, &obj)
	if err == nil {
		return nil
	}
	var text string
	if json.Unmarshal(args, &text) == nil {
		if innerErr := json.Unmarshal([]byte(text), &obj); innerErr != nil {
			return innerErr
		}
	}
	return errors.New("arguments must be a JSON object")
}

// execute runs one tool_call turn and appends its tool_result. done reports
// that the run ends here, with end as the reason.
func (r *run) execute(ctx context.Context, callTurn Turn) (end EndReason, done bool) {
	call := callTurn.Body.ToolCall
	if call.Name == CannotCompleteTool {
		var args struct {
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal(call.Args, &args)
		r.ledger.CannotComplete = &CannotComplete{Reason: args.Reason}
		return EndCannotComplete, true
	}
	if r.cfg.Futures && (call.Name == FutureAwaitTool || call.Name == FutureCancelTool) {
		return r.executeFutureTool(ctx, callTurn)
	}

	entry := LedgerCall{Tool: call.Name, URIs: []string{}, ArgsDigest: ArgsDigest(call.Args)}
	if kind, ok := r.kinds[call.Name]; ok {
		entry.Kind = &kind
	}

	if _, ok := r.offered[call.Name]; !ok {
		// A tool the brief's allowlist never offered: a hallucinated or
		// prompt-injected name. The model can recover (its next reply can
		// call something it was actually offered), so the executor is never
		// reached and the run goes on, exactly like an invalid-arguments
		// call below (clown#261).
		content, _ := json.Marshal(map[string]string{"error": "tool not offered to the model"})
		result := ToolResult{CallID: call.CallID, OK: false, Content: content}
		entry.Error = toolResultText(result)
		r.ledger.Calls = append(r.ledger.Calls, entry)
		t := r.append(ToolResultBody(result), callTurn.ID)
		r.traffic(entry, call.Args, len(content), false, t.At)
		return "", false
	}

	if argsErr := validateToolArgs(call.Args); argsErr != nil {
		// The model can recover from unusable arguments (e.g. JSON cut off by
		// a token limit), so the executor is not called and the run goes on.
		content, _ := json.Marshal(map[string]string{"error": "invalid tool arguments: " + argsErr.Error()})
		result := ToolResult{CallID: call.CallID, OK: false, Content: content}
		entry.Error = toolResultText(result)
		r.ledger.Calls = append(r.ledger.Calls, entry)
		t := r.append(ToolResultBody(result), callTurn.ID)
		r.traffic(entry, call.Args, len(content), false, t.At)
		return "", false
	}

	if r.cfg.Futures {
		if !r.cfg.AbandonTools[call.Name] {
			return r.executeAsFuture(ctx, callTurn, entry)
		}
		content, isError, err := r.callAbandoning(ctx, call)
		return r.settle(ctx, callTurn, entry, content, isError, err)
	}
	content, isError, err := r.cfg.Exec.Call(ctx, call.Name, call.Args)
	return r.settle(ctx, callTurn, entry, content, isError, err)
}

// callAbandoning is the AbandonTools path: the call gets CallWait, and when
// that elapses (the run itself still live) it is cancelled and answered as an
// isError result the model can act on, as juggler#2 does inside the client.
func (r *run) callAbandoning(ctx context.Context, call *ToolCall) (json.RawMessage, bool, error) {
	callCtx, cancel := context.WithTimeout(ctx, r.cfg.CallWait)
	defer cancel()
	content, isError, err := r.cfg.Exec.Call(callCtx, call.Name, call.Args)
	if err != nil && ctx.Err() == nil && errors.Is(callCtx.Err(), context.DeadlineExceeded) {
		text, _ := json.Marshal(fmt.Sprintf("tool call %q exceeded the %s call wait and was abandoned; it may still be running upstream", call.Name, r.cfg.CallWait))
		return text, true, nil
	}
	return content, isError, err
}

// settle records one finished tool-server call: the ledger entry, the
// tool_result turn and the traffic record, and decides whether the run ends
// here. A returned err is a transport fault (terminal), isError a tool-level
// failure the model sees.
func (r *run) settle(ctx context.Context, callTurn Turn, entry LedgerCall, content json.RawMessage, isError bool, err error) (end EndReason, done bool) {
	call := callTurn.Body.ToolCall
	if err != nil {
		entry.Error = err.Error()
		r.ledger.Calls = append(r.ledger.Calls, entry)
		t := r.append(ToolResultBody(ToolResult{CallID: call.CallID, OK: false, Error: err.Error()}), callTurn.ID)
		r.traffic(entry, call.Args, 0, false, t.At)
		if ctx.Err() != nil {
			return EndTimeout, true
		}
		return EndToolError, true
	}

	result := ToolResult{CallID: call.CallID, OK: !isError, Content: content}
	if isError {
		entry.Error = toolResultText(result)
	} else {
		entry.OK = true
		if KindProducesArtifacts(r.kinds[call.Name]) {
			if uris := r.cfg.URIExtractor(call.Name, content); uris != nil {
				entry.URIs = uris
			}
		}
	}
	r.ledger.Calls = append(r.ledger.Calls, entry)
	t := r.append(ToolResultBody(result), callTurn.ID)
	r.traffic(entry, call.Args, len(content), entry.OK, t.At)
	if entry.OK && r.stopNow() {
		return EndEvaluatorPass, true
	}
	return "", false
}

// traffic calls Config.OnTraffic, when set, with one TrafficRecord for a
// completed tool call (clown#255): every call gets one, regardless of which
// of execute's three outcomes it took.
func (r *run) traffic(entry LedgerCall, args json.RawMessage, resultSize int, ok bool, ts time.Time) {
	if r.cfg.OnTraffic == nil {
		return
	}
	r.cfg.OnTraffic(TrafficRecord{
		Step:       r.ledger.Steps,
		Tool:       entry.Tool,
		Args:       args,
		ArgsDigest: entry.ArgsDigest,
		ResultSize: resultSize,
		OK:         ok,
		TS:         ts,
	})
}

// stopNow runs Config.StopWhen over the in-progress ledger; an error is a
// note, never a stop.
func (r *run) stopNow() bool {
	if r.cfg.StopWhen == nil {
		return false
	}
	snapshot := r.ledger
	snapshot.ElapsedMS = r.cfg.Now().Sub(r.start).Milliseconds()
	doc, err := json.Marshal(snapshot)
	if err == nil {
		var stop bool
		if stop, err = r.cfg.StopWhen(doc); err == nil {
			return stop
		}
	}
	r.ledger.Notes = append(r.ledger.Notes, fmt.Sprintf("stop_on_pass check after call %d errored: %v", len(r.ledger.Calls), err))
	return false
}

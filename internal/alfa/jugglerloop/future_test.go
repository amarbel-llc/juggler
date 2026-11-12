package jugglerloop

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// blockingExec answers a call only when release is closed; it stops with the
// call's context otherwise. It counts how many times each tool was started.
type blockingExec struct {
	release chan struct{}
	result  string

	mu        sync.Mutex
	started   int
	cancelled int
}

func newBlockingExec(result string) *blockingExec {
	return &blockingExec{release: make(chan struct{}), result: result}
}

func (b *blockingExec) Call(ctx context.Context, _ string, _ json.RawMessage) (json.RawMessage, bool, error) {
	b.mu.Lock()
	b.started++
	b.mu.Unlock()
	select {
	case <-b.release:
		return json.RawMessage(b.result), false, nil
	case <-ctx.Done():
		b.mu.Lock()
		b.cancelled++
		b.mu.Unlock()
		return nil, false, ctx.Err()
	}
}

func (b *blockingExec) counts() (started, cancelled int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.started, b.cancelled
}

func toolCallReply(id, name, args string) string {
	return fmt.Sprintf(`{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":%q,"type":"function","function":{"name":%q,"arguments":%q}}]},"finish_reason":"tool_calls"}]}`, id, name, args)
}

const issueResult = `{"url":"https://code.example/clown/issues/301"}`

func futureConfig(url string, exec ToolExecutor) Config {
	cfg := baseConfig(openAIResolved(url), exec)
	cfg.Futures = true
	cfg.CallWait = 20 * time.Millisecond
	return cfg
}

func toolResultTexts(res Result) []string {
	var out []string
	for _, t := range res.Transcript.Turns {
		if t.Body.Type == BodyToolResult {
			out = append(out, toolResultText(*t.Body.ToolResult))
		}
	}
	return out
}

// Futures on, a call that answers in time: indistinguishable from a
// synchronous call in the ledger (no future id), and the future tools are
// offered to the model.
func TestFutures_FastCallIsAnOrdinaryCall(t *testing.T) {
	model, srv := newScriptedModel(t, "/v1/chat/completions", openAICreateIssueReply, openAIEndTurnReply)
	res, err := Run(context.Background(), futureConfig(srv.URL, createIssueExecutor()))
	if err != nil {
		t.Fatal(err)
	}
	if res.End != EndTurn || len(res.Ledger.Calls) != 1 || !res.Ledger.Calls[0].OK || res.Ledger.Calls[0].Future != "" {
		t.Fatalf("end = %s, calls = %+v", res.End, res.Ledger.Calls)
	}
	var names []string
	for _, tool := range model.request(0)["tools"].([]any) {
		names = append(names, tool.(map[string]any)["function"].(map[string]any)["name"].(string))
	}
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, FutureAwaitTool) || !strings.Contains(joined, FutureCancelTool) {
		t.Errorf("offered tools = %v, want the future tools", names)
	}
}

// The point of futures: the call outruns CallWait, the model gets a handle,
// awaits it, and the work ran exactly once.
func TestFutures_WaitingThenAwaitRunsTheWorkOnce(t *testing.T) {
	exec := newBlockingExec(issueResult)
	_, srv := newScriptedModel(t, "/v1/chat/completions",
		openAICreateIssueReply,
		toolCallReply("call_2", FutureAwaitTool, `{"future":"f-1","timeout_ms":5000}`),
		openAIEndTurnReply)
	time.AfterFunc(120*time.Millisecond, func() { close(exec.release) })

	res, err := Run(context.Background(), futureConfig(srv.URL, exec))
	if err != nil {
		t.Fatal(err)
	}
	if res.End != EndTurn {
		t.Fatalf("end = %s, want end_turn", res.End)
	}
	if started, _ := exec.counts(); started != 1 {
		t.Errorf("the tool was started %d times, want exactly 1", started)
	}
	if len(res.Ledger.Calls) != 1 {
		t.Fatalf("calls = %+v, want one entry settled in place", res.Ledger.Calls)
	}
	c := res.Ledger.Calls[0]
	if c.Future != "f-1" || !c.OK || c.Error != "" || len(c.URIs) != 1 || c.URIs[0] != "https://code.example/clown/issues/301" {
		t.Errorf("entry = %+v, want settled ok with the issue URI", c)
	}
	texts := toolResultTexts(res)
	if len(texts) != 2 || !strings.Contains(texts[0], `"state":"waiting"`) || !strings.Contains(texts[0], `"future":"f-1"`) || !strings.Contains(texts[1], "issues/301") {
		t.Errorf("tool results = %q, want [waiting handle, the real result]", texts)
	}
}

func TestFutures_CancelStopsTheWork(t *testing.T) {
	exec := newBlockingExec(issueResult)
	_, srv := newScriptedModel(t, "/v1/chat/completions",
		openAICreateIssueReply,
		toolCallReply("call_2", FutureCancelTool, `{"future":"f-1"}`),
		openAIEndTurnReply)
	res, err := Run(context.Background(), futureConfig(srv.URL, exec))
	if err != nil {
		t.Fatal(err)
	}
	if res.End != EndTurn {
		t.Fatalf("end = %s", res.End)
	}
	if started, cancelled := exec.counts(); started != 1 || cancelled != 1 {
		t.Errorf("started = %d, cancelled = %d, want 1 and 1", started, cancelled)
	}
	if c := res.Ledger.Calls[0]; c.OK || c.Error != "cancelled" {
		t.Errorf("entry = %+v, want cancelled", c)
	}
	if texts := toolResultTexts(res); len(texts) != 2 || !strings.Contains(texts[1], `"cancelled"`) {
		t.Errorf("tool results = %q", texts)
	}
}

// The model never awaits: the run ends and the parked call is cancelled and
// recorded, not left running or silently lost.
func TestFutures_ParkedCallIsSettledWhenTheRunEnds(t *testing.T) {
	exec := newBlockingExec(issueResult)
	_, srv := newScriptedModel(t, "/v1/chat/completions", openAICreateIssueReply, openAIEndTurnReply)
	res, err := Run(context.Background(), futureConfig(srv.URL, exec))
	if err != nil {
		t.Fatal(err)
	}
	if started, cancelled := exec.counts(); started != 1 || cancelled != 1 {
		t.Errorf("started = %d, cancelled = %d, want the call cancelled at run end", started, cancelled)
	}
	if c := res.Ledger.Calls[0]; c.OK || c.Error != "cancelled" || c.Future != "f-1" {
		t.Errorf("entry = %+v, want cancelled f-1", c)
	}
}

// A parked call that finishes while the model is elsewhere reaches the
// ledger (artifacts included) without an await.
func TestFutures_FinishedParkedCallIsHarvested(t *testing.T) {
	exec := newBlockingExec(issueResult)
	// Step 2's reply is slow to arrive relative to the release, so the call is
	// done by the time the loop starts step 2; step 2 just ends the turn.
	_, srv := newScriptedModel(t, "/v1/chat/completions", openAICreateIssueReply, openAIEndTurnReply)
	time.AfterFunc(60*time.Millisecond, func() { close(exec.release) })
	cfg := futureConfig(srv.URL, exec)
	cfg.CallWait = 20 * time.Millisecond
	var once sync.Once
	cfg.OnTurn = func(t Turn) {
		if t.Body.Type == BodyToolResult {
			once.Do(func() { time.Sleep(150 * time.Millisecond) })
		}
	}
	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if c := res.Ledger.Calls[0]; !c.OK || len(c.URIs) != 1 {
		t.Errorf("entry = %+v, want the finished call settled ok with its URI", c)
	}
}

// The per-tool flag keeps juggler#2's behaviour: cancelled and answered as an
// error result, no handle.
func TestFutures_AbandonToolIsCancelledAndAnsweredAsAnError(t *testing.T) {
	exec := newBlockingExec(issueResult)
	_, srv := newScriptedModel(t, "/v1/chat/completions", openAICreateIssueReply, openAIEndTurnReply)
	cfg := futureConfig(srv.URL, exec)
	cfg.AbandonTools = map[string]bool{"create_issue": true}
	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.End != EndTurn {
		t.Fatalf("end = %s, want the run to continue to end_turn", res.End)
	}
	if started, cancelled := exec.counts(); started != 1 || cancelled != 1 {
		t.Errorf("started = %d, cancelled = %d", started, cancelled)
	}
	c := res.Ledger.Calls[0]
	if c.OK || c.Future != "" || !strings.Contains(c.Error, "abandoned") {
		t.Errorf("entry = %+v, want an abandoned error with no future", c)
	}
}

func TestFutures_UnknownFutureIsAnErrorResult(t *testing.T) {
	_, srv := newScriptedModel(t, "/v1/chat/completions",
		toolCallReply("call_1", FutureAwaitTool, `{"future":"f-9"}`), openAIEndTurnReply)
	res, err := Run(context.Background(), futureConfig(srv.URL, createIssueExecutor()))
	if err != nil {
		t.Fatal(err)
	}
	if res.End != EndTurn || len(res.Ledger.Calls) != 0 {
		t.Fatalf("end = %s, calls = %+v, want a recovered run with no tool-server call", res.End, res.Ledger.Calls)
	}
	if texts := toolResultTexts(res); len(texts) != 1 || !strings.Contains(texts[0], "unknown future") {
		t.Errorf("tool results = %q", texts)
	}
}

// Futures off: the future tools are not offered and a future_await name is
// refused like any unoffered tool.
func TestFutures_OffByDefault(t *testing.T) {
	_, srv := newScriptedModel(t, "/v1/chat/completions",
		toolCallReply("call_1", FutureAwaitTool, `{"future":"f-1"}`), openAIEndTurnReply)
	cfg := baseConfig(openAIResolved(srv.URL), createIssueExecutor())
	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if texts := toolResultTexts(res); len(texts) != 1 || !strings.Contains(texts[0], "not offered") {
		t.Errorf("tool results = %q, want the not-offered refusal", texts)
	}
}

package jugglerprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	rm "code.linenisgreat.com/juggler/internal/0/juggler"
	"code.linenisgreat.com/juggler/internal/alfa/jugglerloop"
)

// countingTransport answers from canned responses and counts sends.
type countingTransport struct {
	responses []*http.Response
	sends     int
	headers   []http.Header
}

func (c *countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c.headers = append(c.headers, req.Header.Clone())
	i := min(c.sends, len(c.responses)-1)
	c.sends++
	r := c.responses[i]
	return &http.Response{StatusCode: r.StatusCode, Header: r.Header.Clone(), Body: io.NopCloser(strings.NewReader("body")), Request: req}, nil
}

func resp(status int, hdr ...string) *http.Response {
	h := http.Header{}
	for i := 0; i+1 < len(hdr); i += 2 {
		h.Set(hdr[i], hdr[i+1])
	}
	return &http.Response{StatusCode: status, Header: h}
}

func request(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://x.invalid/", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func noSleep(ds *[]time.Duration) func(context.Context, time.Duration) error {
	return func(_ context.Context, d time.Duration) error { *ds = append(*ds, d); return nil }
}

func TestVerifyFailureSendsNothing(t *testing.T) {
	next := &countingTransport{responses: []*http.Response{resp(200)}}
	tr := &Transport{Provider: Fake{ProviderName: "tf", VerifyErr: errors.New("measurement mismatch")}, Next: next}
	for range 2 {
		if _, err := tr.RoundTrip(request(t)); err == nil || !strings.Contains(err.Error(), "measurement mismatch") {
			t.Fatalf("err = %v, want the verification error", err)
		}
	}
	if next.sends != 0 {
		t.Errorf("sends = %d, want 0: a failed verification must transmit nothing", next.sends)
	}
}

func TestRetryHonoursRetryAfterAndPrepares(t *testing.T) {
	next := &countingTransport{responses: []*http.Response{resp(429, "Retry-After", "2"), resp(200)}}
	var slept []time.Duration
	tr := &Transport{Provider: Fake{Headers: map[string]string{"X-Title": "juggler"}}, Next: next, Sleep: noSleep(&slept)}
	got, err := tr.RoundTrip(request(t))
	if err != nil || got.StatusCode != 200 {
		t.Fatalf("resp = %v, err = %v", got, err)
	}
	if len(slept) != 1 || slept[0] != 2*time.Second {
		t.Errorf("slept = %v, want [2s]", slept)
	}
	if s := tr.Stats(); s.Requests != 1 || s.Attempts != 2 {
		t.Errorf("stats = %+v, want 1 request / 2 attempts", s)
	}
	for i, h := range next.headers {
		if h.Get("X-Title") != "juggler" {
			t.Errorf("attempt %d lacks the Prepare header", i+1)
		}
	}
}

func TestNoRetryOnClientErrorAndBoundedOtherwise(t *testing.T) {
	next := &countingTransport{responses: []*http.Response{resp(400)}}
	tr := &Transport{Provider: Fake{}, Next: next, Sleep: noSleep(new([]time.Duration))}
	if got, err := tr.RoundTrip(request(t)); err != nil || got.StatusCode != 400 || next.sends != 1 {
		t.Errorf("400: resp=%v err=%v sends=%d, want one send and the 400 returned", got, err, next.sends)
	}

	next = &countingTransport{responses: []*http.Response{resp(503)}}
	var slept []time.Duration
	tr = &Transport{Provider: Fake{BaseBackoff: time.Second}, Next: next, Sleep: noSleep(&slept), MaxAttempts: 3}
	got, err := tr.RoundTrip(request(t))
	if err != nil || got.StatusCode != 503 || next.sends != 3 {
		t.Errorf("503: resp=%v err=%v sends=%d, want 3 sends then the 503", got, err, next.sends)
	}
	if len(slept) != 2 || slept[0] != time.Second || slept[1] != 2*time.Second {
		t.Errorf("backoff = %v, want [1s 2s]", slept)
	}
}

func TestRetrySleepStopsOnContextCancel(t *testing.T) {
	next := &countingTransport{responses: []*http.Response{resp(503)}}
	ctx, cancel := context.WithCancel(context.Background())
	tr := &Transport{Provider: Fake{BaseBackoff: time.Hour}, Next: next}
	cancel()
	req := request(t).WithContext(ctx)
	if _, err := tr.RoundTrip(req); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// fakeExec answers every tool with a fixed result.
type fakeExec struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeExec) Call(_ context.Context, name string, _ json.RawMessage) (json.RawMessage, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name)
	return json.RawMessage(`"ok"`), false, nil
}

// The working fake path end to end: the unchanged loop, a scripted model, the
// provider harness retrying a 429 and totalling usage, no network.
func TestLoopRunsOnTheScriptedProviderPath(t *testing.T) {
	tpl, err := ParseScriptTemplate([]byte(`
name = "e2e"
[[step]]
text = "listing"
tool = "list_repos"
args = '{}'
failures = 1
status = 429
retry_after = 1
prompt_tokens = 10
completion_tokens = 5
[[step]]
text = "done"
prompt_tokens = 20
completion_tokens = 3
`))
	if err != nil {
		t.Fatal(err)
	}
	var slept []time.Duration
	transport := &Transport{Provider: Fake{}, Next: &ScriptedTransport{}, Sleep: noSleep(&slept)}
	exec := &fakeExec{}
	res, err := jugglerloop.Run(context.Background(), jugglerloop.Config{
		Codec:       &ScriptedCodec{Template: tpl},
		HTTPClient:  &http.Client{Transport: transport},
		Resolved:    rm.ResolveModelResult{Kind: rm.ModelKindRemote, Style: rm.StyleOpenAICompat},
		Model:       "scripted",
		Task:        "file one issue",
		Tools:       []jugglerloop.ToolSpec{{Name: "list_repos", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		Exec:        exec,
		StepMeta:    transport.Step,
		Principal:   "agent",
		BriefTurnID: "brief",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.End != jugglerloop.EndTurn || res.Ledger.Steps != 2 {
		t.Errorf("end = %s, steps = %d, want end_turn after 2 model steps (the retry is not a step)", res.End, res.Ledger.Steps)
	}
	if len(exec.calls) != 1 || exec.calls[0] != "list_repos" {
		t.Errorf("tool calls = %v", exec.calls)
	}
	s := transport.Stats()
	if s.Requests != 2 || s.Attempts != 3 {
		t.Errorf("stats = %+v, want 2 requests / 3 attempts", s)
	}
	if s.Usage.PromptTokens != 30 || s.Usage.CompletionTokens != 8 {
		t.Errorf("usage = %+v, want 30/8 (the failed attempt reports none)", s.Usage)
	}
	if len(slept) != 1 || slept[0] != time.Second {
		t.Errorf("slept = %v, want [1s] from Retry-After", slept)
	}

	// The same account reaches the loop's ledger and turns through StepMeta.
	want := jugglerloop.LedgerProvider{Name: "fake", Requests: 2, Attempts: 3, PromptTokens: 30, CompletionTokens: 8}
	if res.Ledger.Provider == nil || *res.Ledger.Provider != want {
		t.Errorf("ledger provider = %+v, want %+v", res.Ledger.Provider, want)
	}
	var first *jugglerloop.ProviderMeta
	for _, turn := range res.Transcript.Turns {
		if turn.Provider != nil {
			first = turn.Provider
			break
		}
	}
	if first == nil || first.Attempts != 2 || first.PromptTokens != 10 {
		t.Errorf("first model reply turn provider = %+v, want 2 attempts, 10 prompt tokens", first)
	}
}

func TestScriptExhaustedIsAnError(t *testing.T) {
	c := &ScriptedCodec{Template: ScriptTemplate{Steps: []ScriptStep{{Text: "only"}}}}
	if _, err := c.DecodeResponse([]byte(`{"text":"only"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.EncodeRequest(rm.ResolveModelResult{}, "m", jugglerloop.ModelRequest{}); !errors.Is(err, ErrScriptExhausted) {
		t.Errorf("err = %v, want ErrScriptExhausted", err)
	}
}

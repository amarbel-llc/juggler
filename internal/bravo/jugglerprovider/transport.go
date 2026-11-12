package jugglerprovider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// DefaultMaxAttempts bounds the round trips Transport makes for one request.
const DefaultMaxAttempts = 3

// Stats is what a Transport observed.
type Stats struct {
	Requests int // requests accepted by RoundTrip
	Attempts int // round trips sent to Next (retries included)
	Usage    Usage
}

// Transport is the provider harness's policy layer (FDR 0021 §4) as an
// http.RoundTripper, so it plugs into the unchanged loop through
// jugglerloop.Config.HTTPClient: Verify once before the first prompt (failure
// sends nothing), Prepare each attempt, retry as the Provider directs up to
// MaxAttempts, and total the provider-reported Usage.
type Transport struct {
	Provider Provider
	// Next is the underlying transport; nil means http.DefaultTransport.
	Next http.RoundTripper
	// MaxAttempts per request; zero means DefaultMaxAttempts.
	MaxAttempts int
	// Sleep waits between attempts; nil means a context-aware timer.
	Sleep func(ctx context.Context, d time.Duration) error

	verifyOnce sync.Once
	verifyErr  error

	mu    sync.Mutex
	stats Stats
}

var _ http.RoundTripper = (*Transport)(nil)

// Stats returns a snapshot of what the transport observed.
func (t *Transport) Stats() Stats {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stats
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.verifyOnce.Do(func() { t.verifyErr = t.Provider.Verify(req.Context()) })
	if t.verifyErr != nil {
		return nil, fmt.Errorf("%s: verification failed, nothing sent: %w", t.Provider.Name(), t.verifyErr)
	}
	var payload []byte
	if req.Body != nil {
		var err error
		if payload, err = io.ReadAll(req.Body); err != nil {
			return nil, err
		}
		req.Body.Close()
	}
	t.mu.Lock()
	t.stats.Requests++
	t.mu.Unlock()

	max := t.MaxAttempts
	if max <= 0 {
		max = DefaultMaxAttempts
	}
	next := t.Next
	if next == nil {
		next = http.DefaultTransport
	}
	for attempt := 1; ; attempt++ {
		attemptReq := req.Clone(req.Context())
		attemptReq.Body = io.NopCloser(bytes.NewReader(payload))
		attemptReq.ContentLength = int64(len(payload))
		if err := t.Provider.Prepare(attemptReq); err != nil {
			return nil, fmt.Errorf("%s: preparing request: %w", t.Provider.Name(), err)
		}
		t.mu.Lock()
		t.stats.Attempts++
		t.mu.Unlock()

		resp, err := next.RoundTrip(attemptReq)
		var body []byte
		if err == nil {
			body, err = io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				resp = nil
			}
		}
		if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if u, ok := t.Provider.Usage(body); ok {
				t.mu.Lock()
				t.stats.Usage.PromptTokens += u.PromptTokens
				t.stats.Usage.CompletionTokens += u.CompletionTokens
				t.stats.Usage.CostUSD += u.CostUSD
				t.mu.Unlock()
			}
			resp.Body = io.NopCloser(bytes.NewReader(body))
			return resp, nil
		}

		after, again := t.Provider.Retry(attempt, resp, body, err)
		if !again || attempt >= max {
			if err != nil {
				return nil, err
			}
			resp.Body = io.NopCloser(bytes.NewReader(body))
			return resp, nil
		}
		if serr := t.sleep(req.Context(), after); serr != nil {
			return nil, errors.Join(serr, err)
		}
	}
}

func (t *Transport) sleep(ctx context.Context, d time.Duration) error {
	if t.Sleep != nil {
		return t.Sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

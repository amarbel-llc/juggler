// Package jugglerprovider is the provider harness of FDR 0021: an adapter
// between the agent loop and a model codec that supplies the per-provider
// behaviour a bare codec lacks (verification before the first prompt,
// provider-specific request fields, usage accounting, retry policy).
//
// STUBS ONLY. Every method here returns ErrNotImplemented or a zero value; no
// network call is made and no credential is read. The names and shapes are
// proposals from the design record and are expected to change before any
// behaviour lands.
package jugglerprovider

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// ErrNotImplemented is what every stub returns.
var ErrNotImplemented = errors.New("jugglerprovider: not implemented (FDR 0021 stub)")

// Usage is what a provider reported for one response.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	// CostUSD is the provider-reported cost when it reports one, else 0.
	CostUSD float64 `json:"cost_usd,omitempty"`
}

// Provider is the adapter the loop's round trip goes through.
type Provider interface {
	Name() string
	// Verify runs once per run, before the first prompt is sent. A failure
	// ends the run as model_error and nothing is transmitted (fail closed).
	Verify(ctx context.Context) error
	// Prepare may add provider-specific headers or body fields to an encoded
	// request.
	Prepare(req *http.Request) error
	// Usage extracts token counts and cost from a response body.
	Usage(body []byte) (Usage, bool)
	// Retry classifies a failed round trip (attempt counts from 1): retry
	// after the returned delay (again == true), or stop. resp is nil when the
	// round trip itself failed (err != nil); otherwise body is its body and
	// resp.Header carries e.g. Retry-After.
	Retry(attempt int, resp *http.Response, body []byte, err error) (after time.Duration, again bool)
}

// Registry keys of the providers FDR 0021 names.
const (
	NameOpenRouter = "openrouter"
	NameTinfoil    = "tinfoil"
)

// ForName returns the provider registered under name; an empty name means no
// provider (today's plain codec path) and returns (nil, nil).
func ForName(name string) (Provider, error) {
	switch name {
	case "":
		return nil, nil
	case NameOpenRouter:
		return OpenRouter{}, nil
	case NameTinfoil:
		return Tinfoil{}, nil
	}
	return nil, errors.New("jugglerprovider: unknown provider " + name)
}

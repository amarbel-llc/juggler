package jugglerprovider

import (
	"context"
	"net/http"
	"time"
)

// OpenRouter is the OpenRouter adapter (FDR 0021 §2): attribution headers and
// routing preference on Prepare, usage and cost from the response, backoff on
// 429/5xx honouring Retry-After. Stub.
type OpenRouter struct {
	// Referer and Title are the optional attribution headers.
	Referer string
	Title   string
}

var _ Provider = OpenRouter{}

func (OpenRouter) Name() string                                        { return NameOpenRouter }
func (OpenRouter) Verify(context.Context) error                        { return ErrNotImplemented }
func (OpenRouter) Prepare(*http.Request) error                         { return ErrNotImplemented }
func (OpenRouter) Usage([]byte) (Usage, bool)                          { return Usage{}, false }
func (OpenRouter) Retry(int, int, []byte, error) (time.Duration, bool) { return 0, false }

package jugglerprovider

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

// Fake is a Provider with the harness's default policy and no network, for
// the working fake path (FDR 0021): it retries transport errors, 429 and 5xx
// with exponential backoff capped at MaxBackoff, honouring Retry-After, and
// reads usage from a reply's top-level "usage" object.
type Fake struct {
	ProviderName string
	// VerifyErr, when set, is what Verify returns (a failed attestation).
	VerifyErr error
	// Headers are added to every request by Prepare.
	Headers map[string]string
	// BaseBackoff and MaxBackoff default to 1 s and 20 s.
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
}

var _ Provider = Fake{}

func (f Fake) Name() string {
	if f.ProviderName == "" {
		return "fake"
	}
	return f.ProviderName
}

func (f Fake) Verify(context.Context) error { return f.VerifyErr }

func (f Fake) Prepare(req *http.Request) error {
	for k, v := range f.Headers {
		req.Header.Set(k, v)
	}
	return nil
}

func (Fake) Usage(body []byte) (Usage, bool) {
	var w struct {
		Usage *Usage `json:"usage"`
	}
	if json.Unmarshal(body, &w) != nil || w.Usage == nil {
		return Usage{}, false
	}
	return *w.Usage, true
}

func (f Fake) Retry(attempt int, resp *http.Response, _ []byte, err error) (time.Duration, bool) {
	if err == nil && resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500 {
		return 0, false
	}
	maxBackoff := f.MaxBackoff
	if maxBackoff <= 0 {
		maxBackoff = 20 * time.Second
	}
	if resp != nil {
		if secs, perr := strconv.Atoi(resp.Header.Get("Retry-After")); perr == nil && secs >= 0 {
			return min(time.Duration(secs)*time.Second, maxBackoff), true
		}
	}
	base := f.BaseBackoff
	if base <= 0 {
		base = time.Second
	}
	d := base << (attempt - 1)
	if d <= 0 || d > maxBackoff {
		d = maxBackoff
	}
	return d, true
}

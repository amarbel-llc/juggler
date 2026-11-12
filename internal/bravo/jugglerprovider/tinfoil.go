package jugglerprovider

import (
	"context"
	"net/http"
	"time"
)

// Attestor is the seam for Tinfoil's enclave attestation (FDR 0021 §3): check
// the enclave's evidence against a pinned measurement and bind the result to
// the TLS session the prompt will use. Only the interface is defined; the
// evidence format and the channel binding are open questions in the record.
type Attestor interface {
	Attest(ctx context.Context, endpoint, pinnedMeasurement string) error
}

// Tinfoil is the Tinfoil adapter (FDR 0021 §3). Verify must fail closed: an
// unpinned measurement or a failed attestation refuses the run before any
// prompt is sent. Stub.
type Tinfoil struct {
	Endpoint    string
	Measurement string
	Attestor    Attestor
}

var _ Provider = Tinfoil{}

func (Tinfoil) Name() string                                        { return NameTinfoil }
func (Tinfoil) Verify(context.Context) error                        { return ErrNotImplemented }
func (Tinfoil) Prepare(*http.Request) error                         { return nil }
func (Tinfoil) Usage([]byte) (Usage, bool)                          { return Usage{}, false }
func (Tinfoil) Retry(int, int, []byte, error) (time.Duration, bool) { return 0, false }

package jugglerloop

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// The ledger names the run's turn log: the digest recomputed from the
// returned transcript matches the one in the ledger.
func TestTranscriptDigest_LedgerNamesTheTurnLog(t *testing.T) {
	_, srv := newScriptedModel(t, "/v1/chat/completions", openAICreateIssueReply, openAIEndTurnReply)
	res, err := Run(context.Background(), baseConfig(openAIResolved(srv.URL), createIssueExecutor()))
	if err != nil {
		t.Fatal(err)
	}
	d := res.Ledger.TranscriptDigest
	if !strings.HasPrefix(d, "sha256:") || len(d) != len("sha256:")+64 {
		t.Fatalf("digest = %q, want sha256:<64 hex>", d)
	}
	if got := TranscriptDigest(res.Transcript); got != d {
		t.Errorf("recomputed %s != ledger %s", got, d)
	}
	// The ledger digest is the digest of the persisted bytes the result carries.
	if got := DigestBytes(res.TranscriptJSON); got != d {
		t.Errorf("digest of TranscriptJSON %s != ledger %s", got, d)
	}
	if got := TranscriptDigest(res.Transcript); got != d {
		t.Error("the digest must be deterministic")
	}
	// It covers the terminal turn, and any change to a turn changes it.
	altered := res.Transcript
	altered.Turns = append([]Turn(nil), res.Transcript.Turns...)
	altered.Turns[0].Body.Text += " "
	if TranscriptDigest(altered) == d {
		t.Error("changing a turn must change the digest")
	}
	short := res.Transcript
	short.Turns = res.Transcript.Turns[:len(res.Transcript.Turns)-1]
	if TranscriptDigest(short) == d {
		t.Error("the terminal turn must be covered")
	}
}

// It is additive: a ledger built by hand (as the existing goldens do) has no
// digest and serialises as before.
func TestTranscriptDigest_AbsentIsOmitted(t *testing.T) {
	got, _ := json.Marshal(newLedger())
	if strings.Contains(string(got), "transcript_digest") {
		t.Errorf("empty ledger = %s", got)
	}
}

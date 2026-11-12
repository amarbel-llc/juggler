package jugglerloop

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	rm "code.linenisgreat.com/juggler/internal/0/juggler"
)

func openAIResolved(url string) rm.ResolveModelResult {
	return rm.ResolveModelResult{Kind: rm.ModelKindRemote, Style: rm.StyleOpenAICompat, URL: url + "/v1", Token: "sk-test"}
}

// StepMeta is totalled into the ledger and stamped on the FIRST turn of each
// model reply only (FDR 0021 §4).
func TestRun_StepMetaIsTotalledAndStampedOnFirstReplyTurn(t *testing.T) {
	_, srv := newScriptedModel(t, "/v1/chat/completions", openAICreateIssueReply, openAIEndTurnReply)
	cfg := baseConfig(openAIResolved(srv.URL), createIssueExecutor())
	metas := []*ProviderMeta{
		{Name: "fake", Attempts: 2, PromptTokens: 10, CompletionTokens: 5},
		{Name: "fake", Attempts: 1, PromptTokens: 20, CompletionTokens: 3, CostUSD: 0.5},
	}
	n := 0
	cfg.StepMeta = func() *ProviderMeta { m := metas[n]; n++; return m }

	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := &LedgerProvider{Name: "fake", Requests: 2, Attempts: 3, PromptTokens: 30, CompletionTokens: 8, CostUSD: 0.5}
	if res.Ledger.Provider == nil || *res.Ledger.Provider != *want {
		t.Errorf("ledger provider = %+v, want %+v", res.Ledger.Provider, want)
	}
	var stamped []*ProviderMeta
	for _, turn := range res.Transcript.Turns {
		if turn.Provider != nil {
			stamped = append(stamped, turn.Provider)
		}
	}
	if len(stamped) != 2 || stamped[0].Attempts != 2 || stamped[1].PromptTokens != 20 {
		t.Errorf("stamped turns = %+v, want one per model reply, in order", stamped)
	}
	// Tool results and terminal turns never carry it.
	for _, turn := range res.Transcript.Turns {
		if (turn.Body.Type == BodyToolResult || turn.Body.Type == BodyTerminal) && turn.Provider != nil {
			t.Errorf("%s turn carries provider meta", turn.Body.Type)
		}
	}
}

// Without the hook the serialised ledger and turns are exactly as before.
func TestRun_WithoutStepMetaNothingIsAdded(t *testing.T) {
	_, srv := newScriptedModel(t, "/v1/chat/completions", openAIEndTurnReply)
	res, err := Run(context.Background(), baseConfig(openAIResolved(srv.URL), createIssueExecutor()))
	if err != nil {
		t.Fatal(err)
	}
	led, _ := json.Marshal(res.Ledger)
	tr, _ := json.Marshal(res.Transcript)
	if strings.Contains(string(led), `"provider"`) || strings.Contains(string(tr), `"provider"`) {
		t.Errorf("provider fields leaked without StepMeta:\n%s\n%s", led, tr)
	}
}

// A request that fails still reports what it cost, so a model_error run is
// not invisible in the ledger.
func TestRun_StepMetaIsRecordedForAFailedRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	cfg := baseConfig(openAIResolved(srv.URL), createIssueExecutor())
	cfg.StepMeta = func() *ProviderMeta { return &ProviderMeta{Name: "fake", Attempts: 3} }

	res, err := Run(context.Background(), cfg)
	if err == nil || res.End != EndModelError {
		t.Fatalf("end = %q, err = %v, want model_error", res.End, err)
	}
	if p := res.Ledger.Provider; p == nil || p.Requests != 1 || p.Attempts != 3 {
		t.Errorf("ledger provider = %+v, want 1 request / 3 attempts", p)
	}
}

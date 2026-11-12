package jugglerloop

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func finalReply(content string) string {
	return fmt.Sprintf(`{"choices":[{"message":{"role":"assistant","content":%q},"finish_reason":"stop"}]}`, content)
}

func runWithFinal(t *testing.T, format, content string) Result {
	t.Helper()
	_, srv := newScriptedModel(t, "/v1/chat/completions", finalReply(content))
	cfg := baseConfig(openAIResolved(srv.URL), createIssueExecutor())
	cfg.ResultFormat = format
	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func terminalOf(res Result) *Terminal {
	return res.Transcript.Turns[len(res.Transcript.Turns)-1].Body.Terminal
}

// Opted in, a valid final JSON value is carried, compacted, on the terminal
// turn and the ledger's end alike.
func TestResult_JSONFinalMessageIsCarried(t *testing.T) {
	res := runWithFinal(t, ResultFormatJSON, "  {\n \"findings\": [1, 2],\n \"ok\": true }\n")
	const want = `{"findings":[1,2],"ok":true}`
	if got := terminalOf(res); string(got.Result) != want || got.ResultError != "" {
		t.Errorf("terminal = %+v, want result %s", got, want)
	}
	if string(res.Ledger.End.Result) != want || res.Ledger.End.ResultError != "" {
		t.Errorf("ledger end = %+v, want result %s", res.Ledger.End, want)
	}
	if res.End != EndTurn {
		t.Errorf("end = %s", res.End)
	}
}

func TestResult_UnusableFinalMessageSaysWhy(t *testing.T) {
	for name, content := range map[string]string{
		"not json":      "I filed the issue.",
		"trailing text": `{"a":1} and some prose`,
		"two values":    `{"a":1}{"b":2}`,
		"empty":         "   ",
		"oversized":     `"` + strings.Repeat("x", MaxResultBytes) + `"`,
	} {
		res := runWithFinal(t, ResultFormatJSON, content)
		term := terminalOf(res)
		if len(term.Result) != 0 || term.ResultError == "" || res.Ledger.End.ResultError != term.ResultError || len(res.Ledger.End.Result) != 0 {
			t.Errorf("%s: terminal = %+v, ledger end = %+v, want an error and no result", name, term, res.Ledger.End)
		}
		if res.End != EndTurn {
			t.Errorf("%s: end = %s, an unusable result must not change how the run ended", name, res.End)
		}
	}
}

// Not opted in: nothing is added, anywhere, even for a valid JSON message.
func TestResult_OffByDefaultSerialisesAsBefore(t *testing.T) {
	res := runWithFinal(t, "", `{"findings":[]}`)
	led, _ := json.Marshal(res.Ledger)
	tr, _ := json.Marshal(res.Transcript)
	if strings.Contains(string(led), `"result`) || strings.Contains(string(tr), `"result`) {
		t.Errorf("result fields appeared without an opt-in:\n%s\n%s", led, tr)
	}
	if got, _ := json.Marshal(terminalOf(res)); string(got) != `{"reason":"end_turn"}` {
		t.Errorf("terminal = %s", got)
	}
}

// Only an end_turn run has a result: cannot_complete and the like carry none,
// and no error either (there was no final message to read).
func TestResult_OnlyEndTurnRunsHaveOne(t *testing.T) {
	_, srv := newScriptedModel(t, "/v1/chat/completions", openAICannotCompleteReply)
	cfg := baseConfig(openAIResolved(srv.URL), createIssueExecutor())
	cfg.ResultFormat = ResultFormatJSON
	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if term := terminalOf(res); res.End != EndCannotComplete || len(term.Result) != 0 || term.ResultError != "" {
		t.Errorf("end = %s, terminal = %+v, want no result and no error", res.End, term)
	}
}

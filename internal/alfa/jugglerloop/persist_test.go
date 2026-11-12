package jugglerloop

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func turnOf(id string, body Body) Turn {
	return Turn{ID: id, Sender: "agent-1", Parent: "p", At: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Body: body}
}

func transcriptOf(turns ...Turn) Transcript {
	return Transcript{Schema: TranscriptSchema, Turns: turns}
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// With nothing over a cap the persisted form IS encoding/json's output, so the
// redefined digest equals the one first merged for such a transcript.
func TestPersist_UntruncatedEqualsPlainJSON(t *testing.T) {
	tr := transcriptOf(
		turnOf("t1", TextBody("filing")),
		turnOf("t2", ToolCallBody(ToolCall{Name: "create_issue", Args: json.RawMessage(`{"title":"x"}`), CallID: "c1"})),
		turnOf("t3", ToolResultBody(ToolResult{CallID: "c1", OK: true, Content: json.RawMessage(`{"url":"u"}`)})),
		turnOf("t4", TerminalBody(EndTurn)),
	)
	plain, _ := json.Marshal(tr)
	if got := PersistTranscript(tr); string(got) != string(plain) {
		t.Errorf("persisted != plain JSON\n got %s\nwant %s", got, plain)
	}
	if got, want := TranscriptDigest(tr), DigestBytes(plain); got != want {
		t.Errorf("digest = %s, want %s", got, want)
	}
}

func TestPersist_OversizedBodiesBecomeMarkersWithTheOriginalsHash(t *testing.T) {
	bigText := strings.Repeat("a", MaxPersistedBodyBytes+1)
	bigResult := `"` + strings.Repeat("b", MaxPersistedBodyBytes) + `"` // 64 KiB + 2 quotes
	bigArgs := `{"x":"` + strings.Repeat("c", MaxPersistedBodyBytes) + `"}`
	tr := transcriptOf(
		turnOf("t1", TextBody(bigText)),
		turnOf("t2", ToolCallBody(ToolCall{Name: "n", Args: json.RawMessage(bigArgs), CallID: "c1"})),
		turnOf("t3", ToolResultBody(ToolResult{CallID: "c1", OK: true, Content: json.RawMessage(bigResult)})),
		turnOf("t4", TextBody("small")),
	)
	doc := PersistTranscript(tr)
	if len(doc) > 4*1024 {
		t.Errorf("document is %d bytes; the oversized bodies should have been replaced", len(doc))
	}
	var got struct {
		Turns []struct {
			ID   string
			Body struct {
				Text       json.RawMessage
				ToolCall   *struct{ Args json.RawMessage }    `json:"tool_call"`
				ToolResult *struct{ Content json.RawMessage } `json:"tool_result"`
			}
		}
	}
	if err := json.Unmarshal(doc, &got); err != nil {
		t.Fatalf("persisted form is not valid JSON: %v", err)
	}
	marker := func(orig string) string {
		return fmt.Sprintf(`{"truncated":true,"sha256":%q,"bytes":%d}`, sha256Hex([]byte(orig)), len(orig))
	}
	if string(got.Turns[0].Body.Text) != marker(bigText) {
		t.Errorf("text = %s, want %s", got.Turns[0].Body.Text, marker(bigText))
	}
	if string(got.Turns[1].Body.ToolCall.Args) != marker(bigArgs) {
		t.Errorf("args = %s", got.Turns[1].Body.ToolCall.Args)
	}
	if string(got.Turns[2].Body.ToolResult.Content) != marker(bigResult) {
		t.Errorf("content = %s", got.Turns[2].Body.ToolResult.Content)
	}
	if string(got.Turns[3].Body.Text) != `"small"` {
		t.Errorf("a small body must be kept: %s", got.Turns[3].Body.Text)
	}
	if len(got.Turns) != 4 || got.Turns[0].ID != "t1" || got.Turns[3].ID != "t4" {
		t.Errorf("the turn sequence must be complete: %+v", got.Turns)
	}
}

// Past the total cap every further body becomes a marker, small ones too; the
// turn skeletons and the terminal turn are kept, and the document stays bounded.
func TestPersist_TotalCapMarkersTheRestAndKeepsTheSequence(t *testing.T) {
	body := json.RawMessage(`"` + strings.Repeat("r", MaxPersistedBodyBytes-2) + `"`) // just under the per-body cap
	var turns []Turn
	n := MaxPersistedTranscriptBytes/MaxPersistedBodyBytes + 20
	for i := 0; i < n; i++ {
		turns = append(turns, turnOf(fmt.Sprintf("t%d", i), ToolResultBody(ToolResult{CallID: "c", OK: true, Content: body})))
	}
	turns = append(turns, turnOf("small", TextBody("short")), turnOf("end", TerminalBody(EndTurn)))
	doc := PersistTranscript(transcriptOf(turns...))

	if len(doc) > MaxPersistedTranscriptBytes+2*MaxPersistedBodyBytes {
		t.Errorf("document is %d bytes, want it bounded near %d", len(doc), MaxPersistedTranscriptBytes)
	}
	var got struct{ Turns []struct{ ID string } }
	if err := json.Unmarshal(doc, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Turns) != len(turns) {
		t.Fatalf("persisted %d turns, want all %d", len(got.Turns), len(turns))
	}
	s := string(doc)
	if !strings.Contains(s, `"truncated":true`) {
		t.Error("expected markers past the total cap")
	}
	// The small text after the cap is a marker too (force), the terminal is intact.
	if !strings.Contains(s, fmt.Sprintf(`"text":{"truncated":true,"sha256":%q,"bytes":5}`, sha256Hex([]byte("short")))) {
		t.Error("a small body past the total cap must also be a marker")
	}
	if !strings.Contains(s, `"terminal":{"reason":"end_turn"}`) {
		t.Error("the terminal turn must be kept whole")
	}
}

func TestPersist_DeterministicAndDigestNamesItsAlgorithm(t *testing.T) {
	tr := transcriptOf(turnOf("t1", TextBody(strings.Repeat("a", MaxPersistedBodyBytes+5))))
	if string(PersistTranscript(tr)) != string(PersistTranscript(tr)) {
		t.Error("PersistTranscript must be deterministic")
	}
	d := TranscriptDigest(tr)
	if !strings.HasPrefix(d, DigestAlgorithm+":") || len(d) != len(DigestAlgorithm)+1+64 {
		t.Errorf("digest = %q, want %s:<64 hex>", d, DigestAlgorithm)
	}
}

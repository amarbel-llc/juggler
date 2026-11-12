// Package jugglerloop is the tool-use loop behind `juggler run` (FDR 0019
// §1, §4, §8): a stanza-shaped transcript, the runtime-kept ledger, and two
// thin provider codecs (OpenAI-compatible chat completions and Anthropic
// Messages) that project the transcript onto the wire.
package jugglerloop

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"
)

// TranscriptSchema is the version carried by every Transcript.
const TranscriptSchema = 1

// Transcript is the run's turns written out. It is the canonical record;
// the codecs only project it.
type Transcript struct {
	Schema int    `json:"schema"`
	Turns  []Turn `json:"turns"`
}

// Turn is one stanza-shaped message: a sender principal, a provenance link
// to its parent turn, a signature slot (always empty in slice 0) and a
// typed body.
type Turn struct {
	ID        string    `json:"id"`
	Sender    string    `json:"sender"`
	Parent    string    `json:"parent"`
	Signature string    `json:"signature,omitempty"`
	At        time.Time `json:"at"`
	Body      Body      `json:"body"`
	// Provider is what the model provider's harness observed for the model
	// request that produced this turn (FDR 0021 §4). It is set only on the
	// FIRST turn of a model reply, so summing over a transcript counts each
	// request once, and only when Config.StepMeta is set. It is never sent
	// to a provider.
	Provider *ProviderMeta `json:"provider,omitempty"`
}

// ProviderMeta is the provider harness's account of one model request:
// which provider, how many HTTP attempts it took (retries included), and the
// usage the provider reported.
type ProviderMeta struct {
	Name             string  `json:"name,omitempty"`
	Attempts         int     `json:"attempts"`
	PromptTokens     int     `json:"prompt_tokens,omitempty"`
	CompletionTokens int     `json:"completion_tokens,omitempty"`
	CostUSD          float64 `json:"cost_usd,omitempty"`
}

// BodyType discriminates a Body.
type BodyType string

const (
	BodyText       BodyType = "text"
	BodyToolCall   BodyType = "tool_call"
	BodyToolResult BodyType = "tool_result"
	BodyTerminal   BodyType = "terminal"
)

// Body is a tagged union: Type names which one of the other fields is set.
type Body struct {
	Type       BodyType    `json:"type"`
	Text       string      `json:"text,omitempty"`
	ToolCall   *ToolCall   `json:"tool_call,omitempty"`
	ToolResult *ToolResult `json:"tool_result,omitempty"`
	Terminal   *Terminal   `json:"terminal,omitempty"`
}

// ToolCall is a model's request to invoke a tool. CallID is the
// provider-assigned id that the matching ToolResult echoes.
type ToolCall struct {
	Name   string          `json:"name"`
	Args   json.RawMessage `json:"args"`
	CallID string          `json:"call_id"`
}

// ToolResult is the runtime's answer to one ToolCall. OK=false with Content
// set is a tool-level error the model sees; Error carries a transport
// failure's message.
type ToolResult struct {
	CallID  string          `json:"call_id"`
	OK      bool            `json:"ok"`
	Content json.RawMessage `json:"content,omitempty"`
	Error   string          `json:"error,omitempty"`
}

// Terminal records how the loop ended; it is always the transcript's last
// turn.
type Terminal struct {
	Reason EndReason `json:"reason"`
	// Result is the run's structured result: the one JSON value of the final
	// model message, set only when the run opted in (Config.ResultFormat) and
	// ended end_turn with a valid value. ResultError says why there is none
	// when it opted in and the final message was unusable. Both are absent for
	// a run that did not opt in, so such transcripts serialise as before.
	Result      json.RawMessage `json:"result,omitempty"`
	ResultError string          `json:"result_error,omitempty"`
}

func TextBody(text string) Body { return Body{Type: BodyText, Text: text} }

func ToolCallBody(c ToolCall) Body { return Body{Type: BodyToolCall, ToolCall: &c} }

func ToolResultBody(r ToolResult) Body { return Body{Type: BodyToolResult, ToolResult: &r} }

func TerminalBody(reason EndReason) Body {
	return Body{Type: BodyTerminal, Terminal: &Terminal{Reason: reason}}
}

// terminalBodyWithResult is TerminalBody plus the structured result (or why
// there is none).
func terminalBodyWithResult(reason EndReason, result json.RawMessage, resultErr string) Body {
	return Body{Type: BodyTerminal, Terminal: &Terminal{Reason: reason, Result: result, ResultError: resultErr}}
}

// NewRandomTurnID returns a 128-bit random hex id, the default turn id
// generator.
func NewRandomTurnID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

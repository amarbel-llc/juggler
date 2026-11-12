package jugglerprovider

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"text/template"

	"github.com/BurntSushi/toml"

	rm "code.linenisgreat.com/juggler/internal/0/juggler"
	"code.linenisgreat.com/juggler/internal/alfa/jugglerloop"
)

// ScriptStep is one canned model reply: text, a tool call, or both (text
// first). A step may first fail Failures times with Status (default 503), with
// an optional Retry-After, to exercise the harness's retry path; the step's
// real reply is then delivered.
type ScriptStep struct {
	Text     string `toml:"text"`
	ToolName string `toml:"tool"`
	ToolArgs string `toml:"args"` // JSON object

	Failures         int `toml:"failures"`
	Status           int `toml:"status"`
	RetryAfterSecs   int `toml:"retry_after"`
	PromptTokens     int `toml:"prompt_tokens"`
	CompletionTokens int `toml:"completion_tokens"`
}

// ScriptTemplate is a named, parameterised list of canned replies (FDR 0021
// §5): the deterministic test double for the harness, and the shape of the
// substrate's script fallback.
type ScriptTemplate struct {
	Name  string       `toml:"name"`
	Steps []ScriptStep `toml:"step"`
}

// ParseScriptTemplate reads a template from TOML: `name = "..."` and one
// `[[step]]` table per reply.
func ParseScriptTemplate(data []byte) (ScriptTemplate, error) {
	var t ScriptTemplate
	if err := toml.Unmarshal(data, &t); err != nil {
		return ScriptTemplate{}, fmt.Errorf("script template: %w", err)
	}
	if len(t.Steps) == 0 {
		return ScriptTemplate{}, errors.New("script template: no steps")
	}
	return t, nil
}

// Render substitutes params ({{.Key}}) into every step's Text and ToolArgs.
// A key the template names but params lacks is an error, and a rendered
// ToolArgs must be a JSON object.
func (t ScriptTemplate) Render(params map[string]string) (ScriptTemplate, error) {
	out := ScriptTemplate{Name: t.Name, Steps: make([]ScriptStep, len(t.Steps))}
	for i, s := range t.Steps {
		var err error
		if s.Text, err = renderField(params, s.Text); err != nil {
			return ScriptTemplate{}, fmt.Errorf("script template %q step %d text: %w", t.Name, i+1, err)
		}
		if s.ToolArgs, err = renderField(params, s.ToolArgs); err != nil {
			return ScriptTemplate{}, fmt.Errorf("script template %q step %d args: %w", t.Name, i+1, err)
		}
		if s.ToolName != "" {
			var obj map[string]json.RawMessage
			if s.ToolArgs == "" {
				s.ToolArgs = "{}"
			}
			if err := json.Unmarshal([]byte(s.ToolArgs), &obj); err != nil {
				return ScriptTemplate{}, fmt.Errorf("script template %q step %d: args are not a JSON object: %w", t.Name, i+1, err)
			}
		}
		out.Steps[i] = s
	}
	return out, nil
}

func renderField(params map[string]string, field string) (string, error) {
	if !strings.Contains(field, "{{") {
		return field, nil
	}
	tpl, err := template.New("").Option("missingkey=error").Parse(field)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, params); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// scriptWire is what ScriptedCodec puts in a request body and what
// ScriptedTransport answers with: the step's reply, or a failure to retry.
type scriptWire struct {
	Step       int             `json:"step"`
	Failures   int             `json:"failures,omitempty"`
	Status     int             `json:"status,omitempty"`
	RetryAfter int             `json:"retry_after,omitempty"`
	Text       string          `json:"text,omitempty"`
	Tool       string          `json:"tool,omitempty"`
	Args       json.RawMessage `json:"args,omitempty"`
	Usage      *Usage          `json:"usage,omitempty"`
}

// ErrScriptExhausted is returned when the model is asked for a step past the
// end of the script.
var ErrScriptExhausted = errors.New("jugglerprovider: script exhausted")

// ScriptedCodec plays a ScriptTemplate back as a jugglerloop.Codec, one step
// per accepted reply (a retried step is not skipped). Pair it with
// ScriptedTransport as the http.Client's transport: nothing leaves the
// process. Use a pointer; the codec keeps the script position.
type ScriptedCodec struct {
	Template ScriptTemplate

	mu   sync.Mutex
	next int // step awaiting an accepted reply
}

var _ jugglerloop.Codec = (*ScriptedCodec)(nil)

func (c *ScriptedCodec) EncodeRequest(_ rm.ResolveModelResult, _ string, _ jugglerloop.ModelRequest) (*http.Request, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.next >= len(c.Template.Steps) {
		return nil, ErrScriptExhausted
	}
	s := c.Template.Steps[c.next]
	w := scriptWire{Step: c.next, Text: s.Text, Tool: s.ToolName}
	if s.ToolName != "" {
		w.Args = json.RawMessage(s.ToolArgs)
		if len(w.Args) == 0 {
			w.Args = json.RawMessage(`{}`)
		}
	}
	if s.PromptTokens != 0 || s.CompletionTokens != 0 {
		w.Usage = &Usage{PromptTokens: s.PromptTokens, CompletionTokens: s.CompletionTokens}
	}
	if s.Failures > 0 {
		w.Failures, w.Status, w.RetryAfter = s.Failures, s.Status, s.RetryAfterSecs
		if w.Status == 0 {
			w.Status = http.StatusServiceUnavailable
		}
	}
	body, err := json.Marshal(w)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, "http://scripted.invalid/step", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("content-type", "application/json")
	return req, nil
}

func (c *ScriptedCodec) DecodeResponse(body []byte) (jugglerloop.ModelReply, error) {
	var w scriptWire
	if err := json.Unmarshal(body, &w); err != nil {
		return jugglerloop.ModelReply{}, fmt.Errorf("scripted reply: %w", err)
	}
	c.mu.Lock()
	idx := c.next
	c.next++
	c.mu.Unlock()

	var reply jugglerloop.ModelReply
	if w.Text != "" {
		reply.Turns = append(reply.Turns, jugglerloop.Turn{Body: jugglerloop.TextBody(w.Text)})
	}
	reply.Stop = jugglerloop.StopEndTurn
	if w.Tool != "" {
		reply.Turns = append(reply.Turns, jugglerloop.Turn{Body: jugglerloop.ToolCallBody(jugglerloop.ToolCall{
			Name: w.Tool, Args: w.Args, CallID: fmt.Sprintf("call-%d", idx+1),
		})})
		reply.Stop = jugglerloop.StopToolUse
	}
	return reply, nil
}

// ScriptedTransport answers ScriptedCodec's requests in-process: it echoes the
// request body as the 200 reply, except that a step with Failures first fails
// that many times (counted here, per step, across retried sends) with the
// step's status and Retry-After header. Use a pointer; the zero value is ready.
type ScriptedTransport struct {
	mu     sync.Mutex
	failed map[int]int // step -> failures already served
}

var _ http.RoundTripper = (*ScriptedTransport)(nil)

func (s *ScriptedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	var w scriptWire
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("scripted transport: %w", err)
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(raw)),
		Request:    req,
	}
	s.mu.Lock()
	fail := s.failed[w.Step] < w.Failures
	if fail {
		if s.failed == nil {
			s.failed = map[int]int{}
		}
		s.failed[w.Step]++
	}
	s.mu.Unlock()
	if fail {
		resp.StatusCode = w.Status
		resp.Body = io.NopCloser(strings.NewReader("scripted failure"))
		if w.RetryAfter > 0 {
			resp.Header.Set("Retry-After", fmt.Sprint(w.RetryAfter))
		}
	}
	resp.Status = fmt.Sprintf("%d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	return resp, nil
}

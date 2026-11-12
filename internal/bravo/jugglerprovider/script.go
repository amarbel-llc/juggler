package jugglerprovider

import (
	"net/http"

	rm "code.linenisgreat.com/juggler/internal/0/juggler"
	"code.linenisgreat.com/juggler/internal/alfa/jugglerloop"
)

// ScriptStep is one canned model reply: text, or a tool call.
type ScriptStep struct {
	Text     string
	ToolName string
	ToolArgs string // JSON object
}

// ScriptTemplate is a named, parameterised list of canned replies (FDR 0021
// §5): the deterministic test double for the harness, and the shape of the
// substrate's script fallback. Substitution of brief parameters into Text and
// ToolArgs is part of the design and not implemented here.
type ScriptTemplate struct {
	Name  string
	Steps []ScriptStep
}

// ScriptedCodec plays a ScriptTemplate back as a jugglerloop.Codec with no
// network. Stub.
type ScriptedCodec struct {
	Template ScriptTemplate
}

var _ jugglerloop.Codec = ScriptedCodec{}

func (ScriptedCodec) EncodeRequest(rm.ResolveModelResult, string, jugglerloop.ModelRequest) (*http.Request, error) {
	return nil, ErrNotImplemented
}

func (ScriptedCodec) DecodeResponse([]byte) (jugglerloop.ModelReply, error) {
	return jugglerloop.ModelReply{}, ErrNotImplemented
}

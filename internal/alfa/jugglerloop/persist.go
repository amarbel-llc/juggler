package jugglerloop

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// The persisted transcript (FDR 0023 open question 8). A run's transcript is
// written once, at the end, in the form PersistTranscript returns, and
// Ledger.TranscriptDigest is the digest of exactly those bytes, so anyone
// holding the file can hash it and compare.
const (
	// MaxPersistedBodyBytes caps one turn's text, tool_call args or tool_result
	// content in the persisted form, the same bound the room copy uses.
	MaxPersistedBodyBytes = 64 << 10

	// MaxPersistedTranscriptBytes caps the persisted document: once it has grown
	// past this, every further body is replaced by a marker. The turn skeletons
	// (id, sender, parent, time, body type) are always kept, so the sequence
	// stays complete.
	MaxPersistedTranscriptBytes = 8 << 20

	// DigestAlgorithm names the hash behind every digest this package makes. The
	// digest string carries it as a prefix ("sha256:<hex>"), so a reader never
	// has to guess and switching algorithms later is a change here only.
	DigestAlgorithm = "sha256"
)

// DigestBytes is the digest of b: DigestAlgorithm, a colon, the hex hash.
func DigestBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return DigestAlgorithm + ":" + hex.EncodeToString(sum[:])
}

// TranscriptDigest names a transcript: the digest of its persisted form. It
// identifies one run's turn log, timestamps and provider metadata included.
//
// This redefines the digest first merged in ef32dee, which hashed the
// in-memory JSON: for a transcript with nothing truncated the persisted form
// is that same JSON, so the value is unchanged; it differs only when a body
// was over the cap.
func TranscriptDigest(t Transcript) string { return DigestBytes(PersistTranscript(t)) }

// PersistTranscript is the transcript's stored form: encoding/json's output for
// the Transcript, except that a body past MaxPersistedBodyBytes (or any body
// once the document has passed MaxPersistedTranscriptBytes) is replaced by a
// {"truncated":true,"sha256":"<hex of the original>","bytes":N} marker. So the
// omitted bytes stay attested by their own hash. Deterministic.
func PersistTranscript(t Transcript) []byte {
	if t.Turns == nil {
		b, _ := json.Marshal(t)
		return b
	}
	var buf bytes.Buffer
	schema, _ := json.Marshal(t.Schema)
	buf.WriteString(`{"schema":`)
	buf.Write(schema)
	buf.WriteString(`,"turns":[`)
	over := false
	for i, turn := range t.Turns {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(persistTurn(turn, over))
		if buf.Len() > MaxPersistedTranscriptBytes {
			over = true
		}
	}
	buf.WriteString(`]}`)
	return buf.Bytes()
}

// persistTurn marshals one turn, replacing its body payload with a marker when
// force is set or the payload is over the per-body cap.
func persistTurn(turn Turn, force bool) []byte {
	cut := func(n int) bool { return n > 0 && (force || n > MaxPersistedBodyBytes) }
	switch turn.Body.Type {
	case BodyText:
		if cut(len(turn.Body.Text)) {
			// The outer fields shadow the embedded Turn.Body and Body.Text, as the
			// room stanza's truncation does, so every other field is unchanged.
			type truncatedText struct {
				Body
				Text json.RawMessage `json:"text"`
			}
			b, _ := json.Marshal(struct {
				Turn
				Body truncatedText `json:"body"`
			}{turn, truncatedText{turn.Body, truncationMarker([]byte(turn.Body.Text))}})
			return b
		}
	case BodyToolCall:
		if tc := turn.Body.ToolCall; tc != nil && cut(len(tc.Args)) {
			c := *tc
			c.Args = truncationMarker(tc.Args)
			turn.Body.ToolCall = &c
		}
	case BodyToolResult:
		if tr := turn.Body.ToolResult; tr != nil && cut(len(tr.Content)) {
			r := *tr
			r.Content = truncationMarker(tr.Content)
			turn.Body.ToolResult = &r
		}
	}
	b, _ := json.Marshal(turn)
	return b
}

// truncationMarker stands in for a body that was left out of the persisted
// transcript: the original's sha256 (always sha256, whatever DigestAlgorithm
// is, since it names the original's bytes, not the document) and its length.
func truncationMarker(original []byte) json.RawMessage {
	sum := sha256.Sum256(original)
	return json.RawMessage(fmt.Sprintf(`{"truncated":true,"sha256":%q,"bytes":%d}`, hex.EncodeToString(sum[:]), len(original)))
}

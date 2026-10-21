// Package jugglerrpc is the public face of juggler's daemon wire protocol: the
// socket path, the client, the resolve/list-models calls and the frame
// helpers. It exists because another module cannot import this module's
// internal/ tree, and clown (juggler's first consumer) needs exactly this
// slice: resolving a model name to an endpoint, listing models, and framing
// requests in its tests.
//
// Everything here is an alias or a one-line forward over internal/juggler, so
// the daemon and its clients cannot drift: there is one definition of each
// type and one owner of the wire format.
package jugglerrpc

import (
	"bufio"
	"io"

	"code.linenisgreat.com/juggler/internal/juggler"
)

type (
	// Client is a connection to the juggler daemon's control socket.
	Client = juggler.Client

	// Envelope is one JSON-RPC-style frame on the wire.
	Envelope = juggler.Envelope
	// Error is the error member of a response Envelope.
	Error = juggler.Error

	// Model is one entry of a ListModels result.
	Model = juggler.Model
	// ModelKind says whether a Model is served by a local llama-server or is
	// a registered remote endpoint.
	ModelKind = juggler.ModelKind

	ListModelsResult   = juggler.ListModelsResult
	ResolveModelParams = juggler.ResolveModelParams
	ResolveModelResult = juggler.ResolveModelResult
)

const (
	ModelKindLocal  = juggler.ModelKindLocal
	ModelKindRemote = juggler.ModelKindRemote

	MethodListModels   = juggler.MethodListModels
	MethodResolveModel = juggler.MethodResolveModel
)

// SocketPath is the daemon's control socket path for the current user.
func SocketPath() (string, error) { return juggler.SocketPath() }

// NewClient dials the daemon's control socket.
func NewClient(socketPath string) (*Client, error) { return juggler.NewClient(socketPath) }

// WriteFrame writes env as one frame.
func WriteFrame(w io.Writer, env Envelope) error { return juggler.WriteFrame(w, env) }

// ReadFrame reads one frame.
func ReadFrame(r *bufio.Reader) (Envelope, error) { return juggler.ReadFrame(r) }

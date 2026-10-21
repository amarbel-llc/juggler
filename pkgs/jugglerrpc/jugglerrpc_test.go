package jugglerrpc

import (
	"bufio"
	"bytes"
	"testing"
)

// The facade is aliases and forwards; this pins that the frame helpers it
// exports still round-trip an Envelope and that the method names clown relies
// on are the daemon's wire names.
func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	in := Envelope{Method: MethodResolveModel}
	if err := WriteFrame(&buf, in); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}
	out, err := ReadFrame(bufio.NewReader(&buf))
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if out.Method != MethodResolveModel {
		t.Errorf("Method = %q, want %q", out.Method, MethodResolveModel)
	}
}

func TestMethodNames(t *testing.T) {
	if MethodResolveModel != "ResolveModel" || MethodListModels != "ListModels" {
		t.Errorf("wire method names changed: %q %q", MethodResolveModel, MethodListModels)
	}
	if ModelKindRemote != "remote" || ModelKindLocal != "local" {
		t.Errorf("model kinds changed: %q %q", ModelKindLocal, ModelKindRemote)
	}
}

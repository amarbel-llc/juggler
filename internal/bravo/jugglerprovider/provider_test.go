package jugglerprovider

import (
	"context"
	"errors"
	"testing"
)

func TestForName(t *testing.T) {
	if p, err := ForName(""); p != nil || err != nil {
		t.Errorf(`ForName("") = %v, %v, want no provider`, p, err)
	}
	for _, name := range []string{NameOpenRouter, NameTinfoil} {
		p, err := ForName(name)
		if err != nil || p == nil || p.Name() != name {
			t.Errorf("ForName(%q) = %v, %v", name, p, err)
		}
	}
	if _, err := ForName("nope"); err == nil {
		t.Error("unknown provider must be an error")
	}
}

// The stubs must fail closed, never pass verification by accident.
func TestStubsDoNotVerify(t *testing.T) {
	for _, p := range []Provider{OpenRouter{}, Tinfoil{}} {
		if err := p.Verify(context.Background()); !errors.Is(err, ErrNotImplemented) {
			t.Errorf("%s.Verify = %v, want ErrNotImplemented", p.Name(), err)
		}
	}
}

func TestScriptedCodecIsAStub(t *testing.T) {
	if _, err := (ScriptedCodec{}).DecodeResponse(nil); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("DecodeResponse = %v", err)
	}
}

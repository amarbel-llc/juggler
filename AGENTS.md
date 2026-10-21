# juggler

Headless agent substrate and llama-server control plane, extracted from clown
(clown#258). One binary, `juggler`: the `daemon` (llama-server control plane),
model management, and the FDR 0019 agent-substrate verbs (`spawn`, `run`,
`decide`, `resolve`, `clean`, `exit-wake`).

Orientation, not reference: the reference lives in the manpages
(`man/man1/juggler.1`, `man/man7/juggler.7`, `man/man7/juggler-testing.7`) and
FDR `docs/features/0019-juggler-agent-substrate.md`.

## Layout

- `cmd/juggler` — the binary. `testdata/` holds two fixture mains
  (`fake-llama-server`, `fake-platform`) the bats lane builds.
- `internal/juggler*` — nine packages, no import cycles. Height 0: `juggler`
  (RPC frames, socket paths), `jugglermodels`, `jugglereval`, `jugglerdecide`,
  `jugglerrun/jugglerruntest`; then `jugglerloop`, `jugglerbrief`,
  `jugglertools`, `jugglerrun`.
- `pkgs/jugglerrpc` — the public facade over the daemon wire protocol, for
  other modules (clown). The only importable Go surface; everything else is
  `internal/`.
- `zz-tests_bats/` — `juggler.bats`, `juggler_lifecycle.bats`,
  `juggler_reaper.bats`.

## Build and test

No ambient `go`, no `go.mod`: dependencies live in `go.nix` (godyn,
`godyn(7)`). Use the justfile: `just` runs lint, build and test (the
pre-merge gate). `just test-go` is the per-package godyn lane, `just
test-bats` the sandboxed bats lane.

## Boundaries

- Nothing here may import `code.linenisgreat.com/clown` (`just
  lint-boundary`). Clown reaches juggler only through `pkgs/jugglerrpc` and the
  binary.
- moxy is not a flake input (moxy pins clown, clown will pin juggler). A
  consumer burns its moxy in with `lib.<system>.mkJugglerGo { moxyPath }`.
- Juggler exact-matches ringmaster's job-platform protocol version
  (`jobwake.ProtocolVersion`, currently 3).
- `CLOWN_SESSION_ID` is a wire-level environment contract, not a clown import.

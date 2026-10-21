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
- `internal/<level>/<leaf>` — the NATO-level layout (`dagnabit(7)`): a package
  depends only on lower levels, and the level is computed from the import graph,
  never chosen by hand. Refer to packages by leaf (`*/jugglerrun`), not by level;
  levels move as dependencies change. `just codemod-layout` re-tiers and
  `just lint-layout` is the drift gate.
- `pkgs/juggler` — the dagnabit-generated facade over `internal/0/juggler` (the
  daemon wire protocol: socket path, client, frames, resolve/list-models), for
  other modules (clown). The only importable Go surface; everything else is
  `internal/`. Do not edit it: change `internal/0/juggler` and run `just
  codemod-facade` (`just lint-facade` is the drift gate; a purse-first bump
  restamps the header, so the facade lands with the bump).
- `zz-tests_bats/` — `juggler.bats`, `juggler_lifecycle.bats`,
  `juggler_reaper.bats`.

## Build and test

No ambient `go`, no `go.mod`: dependencies live in `go.nix` (godyn,
`godyn(7)`). Use the justfile: `just` runs lint, build and test (the
pre-merge gate). `just test-go` is the per-package godyn lane, `just
test-bats` the sandboxed bats lane. Go tools that need a module (dagnabit,
`go generate`) run through godyn-go, which ingests the post-run resolution into
`go.nix` (godyn(7)); that is by design and a no-op while `go.nix` is resolved, so
`git diff go.nix` after a run is a dependency signal, not noise. To preview a
resolution without touching it, `just debug-godyn-go-scratch`.

`just debug-test-go-race` is the `-race` lane (godyn systems). It is opt-in and
currently red: 5 tests in `internal/charlie/jugglerrun` (parent-cancel, SIGTERM
past the deadline, the three ExitWake done/read retries) die with `ringmaster
...: signal: killed` from the fake ringmaster (an `sh` script that re-execs the
race-instrumented test binary); no data race is reported and the cause is not
yet found.

## Boundaries

- Nothing here may import `code.linenisgreat.com/clown` (`just
  lint-boundary`). Clown reaches juggler only through `pkgs/juggler` and the
  binary.
- moxy is not a flake input (moxy pins clown, clown will pin juggler). A
  consumer burns its moxy in with `lib.<system>.mkJugglerGo { moxyPath }`.
- Juggler exact-matches ringmaster's job-platform protocol version
  (`jobwake.ProtocolVersion`, currently 3).
- `CLOWN_SESSION_ID` is a wire-level environment contract, not a clown import.

# this repo's justfile. Conventions: conformist-justfile(7). `default` is the
# read-only CI lane; `just` passing means the tree is conformant. Add your own
# build-*/test-* aggregates (and put them in `default`) as the repo grows.

default: lint build test

# --- build ---

build: build-juggler

# Builds the juggler binary (godyn on godyn systems, buildGoApplication
# elsewhere; see buildGoAuto in godyn(7)). Result is not linked.
#
# build the juggler binary
build-juggler:
    nix build '.#juggler' --no-link --print-build-logs

# --- test ---

test: test-go test-bats

# godyn's per-package go test lane (checks.tests = passthru.checkAll of the
# juggler build): each tested package's test binary is built and run in its
# own derivation. The bats lane (bats-default) is a separate recipe.
#
# run the Go unit tests per package
test-go:
    #!/usr/bin/env bash
    set -euo pipefail
    system=$(nix eval --raw --impure --expr 'builtins.currentSystem')
    nix build ".#checks.${system}.tests" --no-link --print-build-logs

# Runs the bats integration lanes (juggler.bats, juggler_lifecycle.bats,
# juggler_reaper.bats) in the nix sandbox against the fake llama-server and
# fake platform fixtures.
#
# run the bats integration suite
test-bats:
    #!/usr/bin/env bash
    set -euo pipefail
    system=$(nix eval --raw --impure --expr 'builtins.currentSystem')
    nix build ".#checks.${system}.bats-default" --no-link --print-build-logs

# --- lint ---

lint: lint-fmt lint-boundary lint-layout lint-facade

# Read-only formatting + the eng preset's file-based linters, via the sandboxed
# checks.formatting derivation.
#
# run the read-only formatting check
lint-fmt:
    #!/usr/bin/env bash
    set -euo pipefail
    system=$(nix eval --raw --impure --expr 'builtins.currentSystem')
    nix build ".#checks.${system}.formatting" --no-link --print-build-logs

# Keeps the extraction's Go boundary closed (clown#258): no package in this
# module may import a code.linenisgreat.com/clown path. Runs the pure
# checks.juggler-boundary derivation.
#
# check that nothing imports clown
lint-boundary:
    #!/usr/bin/env bash
    set -euo pipefail
    system=$(nix eval --raw --impure --expr 'builtins.currentSystem')
    nix build ".#checks.${system}.juggler-boundary" --no-link --print-build-logs

# Drift gate: checks.dagnabit-reposition runs `dagnabit -n internal` in the
# vendored module tree; any NATO-level move it would make fails. Fix with `just
# codemod-layout`.
#
# check that internal/<level>/<leaf> matches the dependency depth
lint-layout:
    #!/usr/bin/env bash
    set -euo pipefail
    system=$(nix eval --raw --impure --expr 'builtins.currentSystem')
    nix build ".#checks.${system}.dagnabit-reposition" --no-link --print-build-logs

# Drift gate: checks.dagnabit-codegen regenerates the pkgs/ facade in the
# vendored module tree and fails on any diff from the committed source, then runs
# `dagnabit export -check`. A purse-first bump restamps the facade header; fix
# with `just codemod-facade`.
#
# check that pkgs/ is what dagnabit export would emit
lint-facade:
    #!/usr/bin/env bash
    set -euo pipefail
    system=$(nix eval --raw --impure --expr 'builtins.currentSystem')
    nix build ".#checks.${system}.dagnabit-codegen" --no-link --print-build-logs

lint-impure: lint-worktree

# The impure eng checks (git remotes, sweatfile, agents-md) against the working
# tree, where .git is available. Needs SSH remotes + a sweatfile; add lint-impure
# to the `lint` aggregate above once your repo has them. Runs conformist from the
# devShell (direnv `use flake`).
#
# run the impure eng checks against the working tree
lint-worktree:
    #!/usr/bin/env bash
    set -euo pipefail
    cfg=$(nix build --no-link --print-out-paths '.#conformist-impure-config')
    conformist check --config-file "$cfg" --tree-root .

# --- codemod ---

codemod-fmt: codemod-fmt-tree

codemod-layout: codemod-layout-internal

# Moves every internal/<level>/<leaf> package to the NATO level its dependency
# depth dictates (dagnabit(7)) and rewrites callers' imports. Runs inside
# godyn-go (dagnabit type-loads through the go.mod rendered from go.nix; the
# checkout has none), which patches the result back. dagnabit moves with `git
# mv`, so the sandbox copy gets a throwaway repo, removed again so it stays out of
# the patch. Gotchas, all from godyn-go: it rewrites go.nix from the resolution
# (run `git checkout go.nix` unless a dependency change is intended); the trailing
# ingest cannot see moved files until they are staged, so `git add -A` after the
# run; and dagnabit does not follow imports under testdata/ (fix by hand).
#
# reposition internal/ packages by dependency depth
codemod-layout-internal:
    nix run --inputs-from . igloo#godyn-go -- -- sh -c 'git init -q && git add -A && dagnabit -v internal; rm -rf .git'

codemod-facade: codemod-facade-export

# Regenerates pkgs/<leaf> from the `//go:generate dagnabit export` directives in
# internal/, through godyn-go with the flake's dagnabitPinned (facade-format pass
# pinned to the dagnabit-facade config). Run after changing an exported symbol or
# bumping purse-first (the header stamps dagnabit's version). Same go.nix gotcha
# as codemod-layout.
#
# regenerate the dagnabit pkgs/ facade
codemod-facade-export:
    nix run --inputs-from . igloo#godyn-go -- -- go generate -run dagnabit ./...

# format the tree in place (repair mode) via `nix fmt`
codemod-fmt-tree:
    nix fmt

# --- debug ---

# Previews what dagnabit would do to the internal/ layout (the NATO level rule,
# dagnabit(7)) without moving anything. dagnabit needs a module with a go.mod, so
# it runs inside godyn-go (rendered from go.nix), like cutting-garden's
# codemod-generate-dagnabit. Prints nothing when the layout is already correct.
#
# preview the dagnabit internal/ layout moves
[group('debug')]
debug-plan-layout:
    nix run --inputs-from . igloo#godyn-go -- -- sh -c 'git init -q && git add -A && dagnabit -n -v internal; rm -rf .git'

# One-shot after the NATO relayout: comments, man pages and the FDR named
# packages as internal/<leaf>; levels move, so name them internal/*/<leaf> (the
# dagnabit(7) convention). Skips pkgs/ (generated; regenerate it with `just
# codemod-facade`). Idempotent.
#
# rewrite internal/<leaf> mentions to internal/*/<leaf>
[group('debug')]
debug-rewrite-leaf-refs:
    grep -rlE 'internal/(juggler|jugglerbrief|jugglerdecide|jugglereval|jugglerloop|jugglermodels|jugglerrun|jugglertools)\b' cmd internal man docs zz-tests_bats | xargs sed -i -E 's#internal/(juggler|jugglerbrief|jugglerdecide|jugglereval|jugglerloop|jugglermodels|jugglerrun|jugglertools)\b#internal/*/\1#g'

# One-shot for the clown#258 extraction (step 3): the module moved from
# code.linenisgreat.com/clown to code.linenisgreat.com/juggler. Rewrites the
# Go import paths in place; idempotent. Not part of any lane.
#
# rewrite Go import paths from the clown module to the juggler module
[group('debug')]
debug-rewrite-imports:
    find cmd internal -name '*.go' -print0 | xargs -0 sed -i 's#"code\.linenisgreat\.com/clown/#"code.linenisgreat.com/juggler/#g'
    grep -rn 'code.linenisgreat.com/clown/' --include='*.go' cmd internal || true

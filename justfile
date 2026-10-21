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

lint: lint-fmt lint-boundary

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

# format the tree in place (repair mode) via `nix fmt`
codemod-fmt-tree:
    nix fmt

# --- debug ---

# One-shot for the clown#258 extraction (step 3): the module moved from
# code.linenisgreat.com/clown to code.linenisgreat.com/juggler. Rewrites the
# Go import paths in place; idempotent. Not part of any lane.
#
# rewrite Go import paths from the clown module to the juggler module
[group('debug')]
debug-rewrite-imports:
    find cmd internal -name '*.go' -print0 | xargs -0 sed -i 's#"code\.linenisgreat\.com/clown/#"code.linenisgreat.com/juggler/#g'
    grep -rn 'code.linenisgreat.com/clown/' --include='*.go' cmd internal || true

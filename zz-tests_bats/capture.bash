#!/usr/bin/env bash
# Writes the recorded adapter cases: for every recorded scenario in
# lib/adapter.bash SCENARIOS, one fresh world, the real juggler binary, and the
# NORMALISED case as <out>/<case>/{args,stdout,rc,stderr}, plus <out>/NORMALISE
# (the rules and program that produced them). Roustabout adds COMMIT and README
# and commits the result as its adapter testdata; this script only captures.
#
# Run by the flake package `adapter-cases` (build-only, in the sandbox), never
# by hand with ambient binaries:
#   JUGGLER_BIN=... FAKE_PLATFORM_BIN=... capture.bash <out dir>
# Same honest limits as juggler_adapter_contract.bats: spawn --wait cases are
# real juggler output over ledgers this script seeds (systemd never runs the
# unit); decide answers come from the fake decisions endpoint.

set -euo pipefail

out=${1:?usage: capture.bash <out dir>}
: "${JUGGLER_BIN:?}" "${FAKE_PLATFORM_BIN:?}"

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
LIB_DIR=$here/lib
CONTRACT=$here/contract/roustabout-adapter.json
scratch=$(mktemp -d)
trap 'kill "${DEC_PID:-}" 2>/dev/null || true; rm -rf "$scratch"' EXIT
export HOME=$scratch/home
mkdir -p "$HOME" "$out"

DEC_DIR=$scratch/decisions
mkdir -p "$DEC_DIR"
"$FAKE_PLATFORM_BIN" serve-decisions --dir "$DEC_DIR" >"$DEC_DIR/server.log" 2>&1 &
DEC_PID=$!
for _ in $(seq 100); do
  [[ -s $DEC_DIR/port ]] && break
  sleep 0.1
done
[[ -s $DEC_DIR/port ]] || { echo "fake decisions server never wrote its port" >&2; cat "$DEC_DIR/server.log" >&2; exit 1; }
DEC_PORT=$(<"$DEC_DIR/port")
export LIB_DIR CONTRACT DEC_DIR DEC_PORT JUGGLER_BIN FAKE_PLATFORM_BIN

# shellcheck source=lib/world.bash
. "$LIB_DIR/world.bash"
# shellcheck source=lib/adapter.bash
. "$LIB_DIR/adapter.bash"

n=0
for row in "${SCENARIOS[@]}"; do
  IFS='|' read -r name _ code recorded <<<"$row"
  [[ $recorded == 1 ]] || continue
  record_scenario "$name" "$scratch/w-$name" "$out"
  [[ -s $out/$name/stdout ]] || { echo "case $name recorded no stdout" >&2; exit 1; }
  [[ $(<"$out/$name/rc") == "$code" ]] || { echo "case $name exited $(<"$out/$name/rc"), want $code" >&2; exit 1; }
  n=$((n + 1))
done
[[ $n -gt 0 ]] || { echo "no recorded scenarios" >&2; exit 1; }

cp "$LIB_DIR/normalise.jq" "$out/NORMALISE"
echo "captured $n cases into $out" >&2

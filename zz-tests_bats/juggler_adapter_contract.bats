# The adapter contract lane: roustabout's juggler adapter lists, in a schema-1
# JSON file it owns, the argv it builds for each juggler verb and what it reads
# back (stdout keys, their types, which exit codes they must appear or be
# absent at). This lane vendors that file (contract/roustabout-adapter.json,
# with a sidecar naming the roustabout commit and the file's sha256; refresh it
# with `just debug-sync-contract`) and drives the BUILT juggler binary through
# every (verb, exit code) the file lists, in a fresh fake platform world each
# time (lib/world.bash), with and without the optional argv groups.
#
# What this pins: the binary's stdout shape and exit codes for the calls the
# adapter makes. What it does not: roustabout's graphs; and the spawn --wait
# cases are real juggler output over ledgers the test seeds (systemd never runs
# the unit), the decide answers come from the fake decisions endpoint.
#
# The recorded scenarios (lib/adapter.bash SCENARIOS, flag 1) are also what the
# capture package writes out as roustabout's adapter testdata; the stability
# test below is the precondition for committing them: two runs in two fresh
# worlds must agree byte for byte after lib/normalise.jq.
#
# Binds loopback only; no capability escalation (docs/adrs/0007).

setup_file() {
  load 'lib/common.bash'

  export BATS_NO_PARALLELIZE_WITHIN_FILE=true

  require_bin JUGGLER_BIN juggler
  require_bin FAKE_PLATFORM_BIN fake-platform

  export DEC_DIR="$BATS_FILE_TMPDIR/decisions"
  mkdir -p "$DEC_DIR"
  "$FAKE_PLATFORM_BIN" serve-decisions --dir "$DEC_DIR" >"$DEC_DIR/server.log" 2>&1 &
  export DEC_PID=$!
  wait_for_file "$DEC_DIR/port" 5
  if [[ ! -s $DEC_DIR/port ]]; then
    echo "fake decisions server never wrote its port; log:" >&2
    cat "$DEC_DIR/server.log" >&2
    return 1
  fi
  export DEC_PORT
  DEC_PORT=$(<"$DEC_DIR/port")
}

teardown_file() {
  [[ -n ${DEC_PID:-} ]] && kill "$DEC_PID" 2>/dev/null || true
}

setup() {
  load 'lib/common.bash'
  export LIB_DIR="$BATS_TEST_DIRNAME/lib"
  export CONTRACT_DIR="$BATS_TEST_DIRNAME/contract"
  load 'lib/world.bash'
  load 'lib/adapter.bash'
}

@test "adapter contract: sidecar digest matches the vendored file" {
  local file="$CONTRACT_DIR/roustabout-adapter.json" side="$CONTRACT_DIR/roustabout-adapter.source"
  [[ -f $file && -f $side ]] || { echo "file and sidecar must both exist: $file / $side" >&2; return 1; }
  local want have
  want=$(sed -n 's/^roustabout [0-9a-f]\{7,40\} sha256:\([0-9a-f]\{64\}\)$/\1/p' "$side")
  [[ -n $want ]] || { echo "sidecar is not 'roustabout <commit> sha256:<hex>': $(<"$side")" >&2; return 1; }
  have=$(sha256sum "$file" | cut -d' ' -f1)
  [[ $have == "$want" ]] || { echo "vendored copy was edited: sha256 $have, sidecar says $want (re-run just debug-sync-contract)" >&2; return 1; }
}

@test "adapter contract: the binary honours roustabout's contract file" {
  contract_check "$CONTRACT_DIR/roustabout-adapter.json" "$BATS_TEST_TMPDIR/runs"
}

@test "adapter contract: the runner fails a contract the binary does not honour" {
  local bad="$BATS_TEST_TMPDIR/bad.json"
  jq '(.verbs[] | select(.name == "spawn-new-run") | .stdout) += [{"key":"no_such_key","type":"string","present_at_exit":[0]}]' \
    "$CONTRACT_DIR/roustabout-adapter.json" >"$bad"
  run contract_check "$bad" "$BATS_TEST_TMPDIR/runs" 3>/dev/null
  [ "$status" -ne 0 ]
  [[ $output == *'"no_such_key" must be present'* ]]
}

@test "adapter contract: the runner fails an exit code with no scenario" {
  local bad="$BATS_TEST_TMPDIR/bad.json"
  jq '(.verbs[] | select(.name == "resolve") | .exit) += [9]' \
    "$CONTRACT_DIR/roustabout-adapter.json" >"$bad"
  run contract_check "$bad" "$BATS_TEST_TMPDIR/runs" 3>/dev/null
  [ "$status" -ne 0 ]
  [[ $output == *'no scenario for resolve exit 9'* ]]
}

@test "adapter contract: the runner fails an undeclared placeholder" {
  local bad="$BATS_TEST_TMPDIR/bad.json"
  jq '(.verbs[] | select(.name == "resolve") | .argv) += ["{mystery}"]' \
    "$CONTRACT_DIR/roustabout-adapter.json" >"$bad"
  run contract_check "$bad" "$BATS_TEST_TMPDIR/runs" 3>/dev/null
  [ "$status" -ne 0 ]
  [[ $output == *'{mystery}'* ]]
}

@test "adapter contract: recorded cases are byte-stable across fresh worlds" {
  CONTRACT="$CONTRACT_DIR/roustabout-adapter.json"
  local row name code recorded n=0
  for row in "${SCENARIOS[@]}"; do
    IFS='|' read -r name _ code recorded <<<"$row"
    [[ $recorded == 1 ]] || continue
    n=$((n + 1))
    record_scenario "$name" "$BATS_TEST_TMPDIR/a-$name" "$BATS_TEST_TMPDIR/out-a"
    record_scenario "$name" "$BATS_TEST_TMPDIR/b-$name" "$BATS_TEST_TMPDIR/out-b"
    # not vacuous: the case holds real output and the exit code it is named for
    [[ -s $BATS_TEST_TMPDIR/out-a/$name/stdout || -s $BATS_TEST_TMPDIR/out-a/$name/stderr ]] || { echo "case $name recorded no output" >&2; return 1; }
    [[ $(<"$BATS_TEST_TMPDIR/out-a/$name/rc") == "$code" ]] || { echo "case $name recorded exit $(<"$BATS_TEST_TMPDIR/out-a/$name/rc"), want $code" >&2; return 1; }
    if ! diff -r "$BATS_TEST_TMPDIR/out-a/$name" "$BATS_TEST_TMPDIR/out-b/$name" >&2; then
      echo "case $name differs between two fresh worlds after normalisation" >&2
      return 1
    fi
  done
  [[ $n -gt 0 ]]
  echo "# $n recorded cases stable" >&3
}

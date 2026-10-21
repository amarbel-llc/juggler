# Conformance test for ringmaster's host-service liveness reaping (ADR-0001,
# RFC-0020 §5.4, consumed via the pinned flake input; clown#257, ported to
# ringmaster S7).
#
# juggler_lifecycle.bats exercises ExitWake's race-handling logic (the
# hook's own `done` losing a race against "whatever already wrote a
# terminal") entirely against the FAKE ringmaster binary's race-once knob;
# it never runs a real service. This file drives the real, pinned
# `ringmaster` binary's own producer surface directly — `ringmaster lock`,
# not `juggler run` — because dead-producer reaping is a ringmaster
# mechanism, not a juggler-specific one: a full agent run would need a
# bash-reachable model/tool-plane fixture this suite does not have, which
# is disproportionate for exercising a mechanism with no juggler-specific
# moving parts. `ringmaster lock <job> -- <command>` (ringmaster.1) is the
# documented way a CLI producer holds the job on the host service for the
# life of its command (RFC-0016, RFC-0020 §5): running `sleep` under it and
# SIGKILLing the wrapper simulates a producer that crashed without writing a
# terminal record, exactly as a killed `juggler run` unit would. Since S7 the
# store is private to `ringmaster serve`: the service itself notices the
# dropped hold and writes `interrupted` after three continuous seconds of
# "gone" (there is no separate `ringmaster reap` to run any more).
#
# No network: the service listens on a unix socket under $XDG_RUNTIME_DIR and
# keeps its store under $XDG_STATE_HOME. It runs in the standard nix sandbox
# and every other Linux sandbox we use.

setup() {
  load 'lib/common.bash'

  # bats-island isolation: fresh $HOME + XDG dirs (incl. XDG_STATE_HOME
  # and XDG_RUNTIME_DIR) all under $BATS_TEST_TMPDIR, same as
  # job_wakeup.bats.
  setup_test_home

  require_bin RINGMASTER_BIN ringmaster

  # Gotcha (job_wakeup.bats): setup_test_home's XDG_RUNTIME_DIR is too deep
  # for an AF_UNIX sun_path (the service's socket is
  # $XDG_RUNTIME_DIR/ringmaster/service.sock); short-circuit it.
  CJR_RUNTIME_DIR="$(mktemp -d /tmp/cjr.XXXXXX)"
  export XDG_RUNTIME_DIR="$CJR_RUNTIME_DIR"

  export CLOWN_SESSION_ID="test/reaper-chan"
}

teardown() {
  cleanup_pids "${LOCK_PID:-}" "${SERVE_PID:-}"
  if [[ -n ${CJR_RUNTIME_DIR:-} ]]; then
    rm -rf "$CJR_RUNTIME_DIR"
  fi
}

# start_service runs `ringmaster serve` in the foreground of a background job
# and blocks until its "listening at" line (its readiness signal, ringmaster.1)
# lands in the log, or fails the test.
start_service() {
  "$RINGMASTER_BIN" serve >"$BATS_TEST_TMPDIR/serve.log" 2>&1 &
  SERVE_PID=$!
  local tries=0
  while ! grep -qF "listening at" "$BATS_TEST_TMPDIR/serve.log" 2>/dev/null && [[ $tries -lt 100 ]]; do
    sleep 0.1
    tries=$((tries + 1))
  done
  grep -qF "listening at" "$BATS_TEST_TMPDIR/serve.log"
}

# status's JSON liveness field: alive while the service holds the job for a
# connected producer, gone once the holder dies without a terminal record.
status_liveness() {
  "$RINGMASTER_BIN" status "$1" --json | jq -r '.liveness // empty'
}

status_state() {
  "$RINGMASTER_BIN" status "$1" --json | jq -r '.state // empty'
}

@test "the host service terminalizes a job whose lock-holding producer died without a terminal record" {
  start_service

  local job
  job=$("$RINGMASTER_BIN" start --source test --label reaped)

  # A producer that holds the job for the life of `sleep 300`; killed before
  # it ever gets to run `done`.
  "$RINGMASTER_BIN" lock "$job" -- sleep 300 &
  LOCK_PID=$!

  local tries=0
  while [[ $(status_liveness "$job") != alive && $tries -lt 50 ]]; do
    sleep 0.1
    tries=$((tries + 1))
  done
  [[ $(status_liveness "$job") == alive ]]

  kill -KILL "$LOCK_PID"
  wait "$LOCK_PID" 2>/dev/null || true
  unset LOCK_PID

  # The hold is a connection the kernel closes the instant the holder dies,
  # so the service reads the producer as gone immediately — before its
  # three-second grace (ringmaster.1 PRODUCER LIVENESS) has let it act.
  [[ $(status_liveness "$job") == gone ]]
  [[ $(status_state "$job") == running ]]

  # Three continuous seconds of "gone", then the service writes `interrupted`
  # itself. Poll well past that; this is the "within the grace" assertion.
  tries=0
  while [[ $(status_state "$job") == running && $tries -lt 100 ]]; do
    sleep 0.1
    tries=$((tries + 1))
  done
  [[ $(status_state "$job") == interrupted ]]

  # The terminal is the service's own, not merely "some terminal landed".
  run "$RINGMASTER_BIN" read --job "$job" --json
  assert_success
  [[ $(jq -s -r '.[-1].type' <<<"$output") == interrupted ]]

  # The service exits 0 on SIGTERM (ringmaster.1 `serve`).
  kill -TERM "$SERVE_PID"
  wait "$SERVE_PID"
  local rc=$?
  unset SERVE_PID
  [[ $rc -eq 0 ]]
}

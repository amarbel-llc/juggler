# The adapter contract runner and its scenarios (lib/world.bash supplies the
# isolated world). Used by juggler_adapter_contract.bats.
#
# A contract file (schema 1, written by the consumer; see
# contract/example-adapter.json for every construct) lists, per verb, the argv
# the consumer builds ({name} placeholders), which stdout keys it reads and at
# which exit codes they must be present or absent, and the exit codes it
# claims. The runner:
#   - renders each argv with fixed placeholder values (an undeclared or
#     unsupported placeholder FAILS, so a new one cannot slip in),
#   - for every (verb, exit code) runs each scenario that produces that code on
#     the real binary in a fresh world, with and without the optional argv
#     groups, and asserts the exit code, the presence/absence rules and the
#     types of the listed keys,
#   - fails with "no scenario for <verb> exit N" when a listed code has none.
# Exit codes juggler produces that the file does not list are not an error.

# SCENARIOS: name|verb|exit code|recorded (1 = a recorded adapter-testdata case)
SCENARIOS=(
  "new-run-ok|spawn-new-run|0|1"
  "new-run-existing|spawn-new-run|0|1"
  "new-run-existing-resolved|spawn-new-run|0|1"
  "new-run-pending-failure|spawn-new-run|1|1"
  "wait-succeeded|spawn-wait|0|1"
  "wait-result|spawn-wait|0|1"
  "wait-result-null|spawn-wait|0|1"
  "wait-result-error|spawn-wait|0|1"
  "wait-failed-cannot-complete|spawn-wait|2|1"
  "wait-failed-with-result|spawn-wait|2|1"
  "wait-aborted|spawn-wait|3|0"
  "wait-dead-agent|spawn-wait|4|1"
  "wait-still-running|spawn-wait|5|0"
  "wait-usage-error|spawn-wait|1|0"
  "decide-ok|decide|0|1"
  "decide-usage-error|decide|1|0"
  "decide-no-choice|decide|2|0"
  "decide-room-post-failed|decide|3|0"
  "decide-below-threshold|decide|4|0"
  "resolve-ok|resolve|0|1"
  "resolve-already-resolved|resolve|0|1"
  "resolve-no-minter|resolve|1|0"
)

# ---------------------------------------------------------------- placeholders

# ph_defaults: the fixed placeholder values of the current world.
ph_defaults() {
  declare -gA PH=(
    [run_key]=rk-rec
    [input_file]="$W_ROOT/input.json"
    [room_domain]=rooms.test
    [operator_jid]=operator@xmpp.test
    [issuer]=issuer-rec
    [brief_file]="$W_ROOT/issue-filer.toml"
    [task_file]="$W_ROOT/task.txt"
    [timeout]=30s
    [model]=jev
    [room]=rk-rec@rooms.test
    [state]=failed
    [reason]='{"why":"contract check"}'
    [artifacts]='[{"tool":"orgzly_note","kind":"note","uris":["orgzly://note/9"]}]'
    [run_job]=
    [parent]=
  )
  # decide's gate: the contract entry's own `question`, else this lane's.
  local q
  q=$(verb_field decide '.question // empty' 2>/dev/null | jq -r . 2>/dev/null || true)
  PH[question]=${q:-route}
}

# verb_field <verb> <jq expr over the verb object>
verb_field() { jq -c --arg v "$1" ".verbs[] | select(.name == \$v) | $2" "$CONTRACT"; }

# render_argv <verb> <base|full>: fill RENDERED with the verb's argv.
render_argv() {
  local verb=$1 mode=$2 el s name
  RENDERED=()
  local els=()
  mapfile -t els < <(verb_field "$verb" '.argv[]' | jq -r .)
  if [[ $mode == full ]]; then
    local more=()
    mapfile -t more < <(verb_field "$verb" '(.argv_optional // [])[][]' | jq -r .)
    els+=("${more[@]}")
  fi
  for el in "${els[@]}"; do
    s=$el
    while [[ $s =~ \{([a-z_]+)\} ]]; do
      name=${BASH_REMATCH[1]}
      if [[ $(jq -r --arg n "$name" '(.placeholders // {}) | has($n)' "$CONTRACT") != true ]]; then
        echo "placeholder {$name} in $verb's argv is not declared in the contract's placeholders" >&2
        return 1
      fi
      if [[ -z ${PH[$name]+x} ]]; then
        echo "placeholder {$name} (verb $verb) has no value in lib/adapter.bash ph_defaults: add it" >&2
        return 1
      fi
      s="${s%%\{"$name"\}*}${PH[$name]}${s#*\{"$name"\}}"
    done
    RENDERED+=("$s")
  done
}

# ------------------------------------------------------------------- fixtures

LEDGER_OK='{"schema":1,"calls":[{"tool":"ring_create_issue","kind":"issue","ok":true,"uris":["https://forge.test/o/r/issues/1"],"args_digest":"d1"}],"end":{"reason":"end_turn"},"cannot_complete":null,"steps":2,"elapsed_ms":10}'
LEDGER_RESULT='{"schema":1,"calls":[],"end":{"reason":"end_turn","result":{"findings":[1]}},"cannot_complete":null,"steps":1,"elapsed_ms":1,"transcript_digest":"sha256:0000000000000000000000000000000000000000000000000000000000000000"}'
LEDGER_NULL='{"schema":1,"calls":[],"end":{"reason":"end_turn","result":null},"cannot_complete":null,"steps":1,"elapsed_ms":1}'
LEDGER_RESULT_ERROR='{"schema":1,"calls":[],"end":{"reason":"end_turn","result_error":"the final message is not valid JSON"},"cannot_complete":null,"steps":1,"elapsed_ms":1}'
LEDGER_CANNOT='{"schema":1,"calls":[],"end":{"reason":"cannot_complete"},"cannot_complete":{"reason":"no repo matches"},"steps":1,"elapsed_ms":5}'

# The decisions fixtures: the consumer's own (contract entry fields `stdin` and
# `fake_response`) when it gives them, else this lane's.
DEFAULT_DECIDE_PAYLOAD='{"state":"file an issue on moxy that restart should reconnect","questions":{"route":{"type":"choice","instructions":"Which kind of capture is it?","criteria":{"issue":"File, open or log an issue against a project.","note":"Anything else."}}}}'
DEFAULT_DECIDE_RESPONSE='{"answers":{"route":{"type":"choice","choice":"issue","probabilities":{"issue":0.9,"note":0.1},"confidence":0.8}},"id":"gen-dec-1","model":"typesafe/jev-test","usage":{"cost":0,"input_tokens":5,"output_tokens":1}}'

# ------------------------------------------------------------ scenario helpers

# subject <verb>: run the contract's argv for <verb> (mode $MODE) in the world.
subject() {
  local verb=$1 name
  shift
  render_argv "$verb" "${MODE:-base}" || return 1
  # The consumer says which variables it does NOT set for this verb
  # (`env_absent`): the verb must work without them.
  while IFS= read -r name; do
    [[ -n $name ]] && unset "$name"
  done < <(verb_field "$verb" '(.env_absent // [])[]' | jq -r .)
  case $verb in
    spawn-new-run) jug -as issuer-rec "${RENDERED[@]}" "$@" ;;
    spawn-wait) jug -as "$ROOT" "${RENDERED[@]}" "$@" ;;
    decide)
      local payload="$W_ROOT/decide-stdin.json"
      local custom
      custom=$(verb_field decide '.stdin // empty')
      if [[ -n $custom && $(jq -r type <<<"$custom") == object ]]; then
        printf '%s' "$custom" >"$payload"
      else
        printf '%s' "$DEFAULT_DECIDE_PAYLOAD" >"$payload"
      fi
      jug -as "$ROOT" --stdin "$payload" "${RENDERED[@]}" "$@"
      ;;
    resolve) jug "${RENDERED[@]}" "$@" ;;
    *) echo "no subject runner for verb $verb" >&2; return 1 ;;
  esac
}

# setup_new_run: a fresh open run, with ROOT, PH[job], PH[parent] and PH[room].
setup_new_run() {
  jug -as issuer-rec spawn --new-run --run-key "${PH[run_key]}" --input "${PH[input_file]}" \
    --room-domain "${PH[room_domain]}" --operator-jid "${PH[operator_jid]}" --issuer "${PH[issuer]}"
  [[ $J_STATUS -eq 0 ]] || { echo "setup: spawn --new-run exited $J_STATUS: $J_ERR" >&2; return 1; }
  ROOT=$(jq_out .root_principal)
  PH[run_job]=$(jq_out .run_job)
  PH[parent]=$(jq_out .root_stanza)
  PH[room]=$(jq_out .room)
}

# setup_resolved: resolve the run (setup only; not the recorded call).
setup_resolved() {
  jug resolve "${PH[run_job]}" --state succeeded --reason "setup"
  [[ $J_STATUS -eq 0 ]] || { echo "setup: resolve exited $J_STATUS: $J_ERR" >&2; return 1; }
}

# setup_child: launch a child of the run (no wait); CHILD_JOB.
setup_child() {
  jug -as "$ROOT" spawn --brief "${PH[brief_file]}" --task "${PH[task_file]}" --run-key "${PH[run_key]}" --user
  [[ $J_STATUS -eq 0 ]] || { echo "setup: spawn --brief exited $J_STATUS: $J_ERR" >&2; return 1; }
  CHILD_JOB=$(jq_out .job)
}

# seed_terminal <state> <message> [ledger json]: play the agent: write the
# job's spool and terminal record the way juggler run or the post-stop hook would.
seed_terminal() {
  local state=$1 message=$2 ledger=${3:-} args
  args=(done "$CHILD_JOB" --target "$ROOT" --state "$state" --message "$message")
  if [[ -n $ledger ]]; then
    printf '%s' "$ledger" >"$FAKE_DIR/rm/$ROOT/$CHILD_JOB.out"
    args+=(--result-ref "$FAKE_DIR/rm/$ROOT/$CHILD_JOB.out")
  fi
  "$FAKE_BIN/ringmaster" "${args[@]}" >/dev/null
}

# wait_with <state> <message> [ledger]: launch a child, seed its terminal, wait.
wait_with() {
  setup_new_run && setup_child && seed_terminal "$@" && subject spawn-wait
}

# decision <confidence-aware body or contract fake_response>: set the endpoint's answer.
set_ok_decision() {
  local custom
  custom=$(verb_field decide '.fake_response // empty')
  set_decision 200 "${custom:-$DEFAULT_DECIDE_RESPONSE}"
}

# --------------------------------------------------------------------- scenarios

sc_new_run_ok() { subject spawn-new-run; }
sc_new_run_existing() { setup_new_run && subject spawn-new-run; }
sc_new_run_existing_resolved() { setup_new_run && setup_resolved && subject spawn-new-run; }
sc_new_run_pending_failure() {
  mkdir -p "$FAKE_DIR/fail"
  : >"$FAKE_DIR/fail/troupe-muc-send"
  subject spawn-new-run
}

sc_wait_succeeded() { wait_with succeeded "evaluator passed" "$LEDGER_OK"; }
sc_wait_result() { wait_with succeeded "evaluator passed" "$LEDGER_RESULT"; }
sc_wait_result_null() { wait_with succeeded "evaluator passed" "$LEDGER_NULL"; }
sc_wait_result_error() { wait_with succeeded "evaluator passed" "$LEDGER_RESULT_ERROR"; }
sc_wait_failed_cannot_complete() { wait_with failed 'cannot_complete: "no repo matches"' "$LEDGER_CANNOT"; }
sc_wait_failed_with_result() { wait_with failed "evaluator false" "$LEDGER_RESULT"; }
sc_wait_aborted() { wait_with aborted "cancelled by a holder"; }
sc_wait_dead_agent() { wait_with interrupted "crash: unit result exit-code"; }
sc_wait_still_running() {
  setup_new_run && setup_child || return 1
  PH[timeout]=1s
  # roustabout's argv has no --stop-grace; the default grace is 30 s, so this
  # scenario adds one (and says so): the shape and the exit code are the same.
  subject spawn-wait --stop-grace 1s
}
sc_wait_usage_error() {
  setup_new_run || return 1
  PH[brief_file]="$W_ROOT/no-such-brief.toml"
  subject spawn-wait
}

sc_decide_ok() { setup_new_run && set_ok_decision && subject decide; }
sc_decide_usage_error() { setup_new_run || return 1; PH[room]=; subject decide; }
sc_decide_no_choice() { setup_new_run || return 1; set_decision 500 '{"error":{"message":"upstream down"}}'; subject decide; }
sc_decide_room_post_failed() {
  setup_new_run && set_ok_decision || return 1
  mkdir -p "$FAKE_DIR/fail"
  : >"$FAKE_DIR/fail/troupe-muc"
  subject decide
}
# The adapter passes --min-confidence 0 itself; this scenario asks for 1 (a later
# --min-confidence wins) so the lane's 0.8 answer falls below the threshold.
sc_decide_below_threshold() { setup_new_run && set_ok_decision || return 1; subject decide --min-confidence 1; }

sc_resolve_ok() { setup_new_run && subject resolve; }
sc_resolve_already_resolved() { setup_new_run && setup_resolved && subject resolve; }
sc_resolve_no_minter() { setup_new_run || return 1; export TROUPE_MINT_PASSWORD_FILE= TROUPE_MINT_USER=; subject resolve; }

# run_scenario <name> <mode> <world root>: a fresh world, then the scenario.
run_scenario() {
  local name=$1 mode=$2 root=$3
  MODE=$mode
  world_up "$root"
  ph_defaults
  ROOT="" CHILD_JOB=""
  "sc_${name//-/_}"
}

# ---------------------------------------------------------------------- checks

# check_keys <verb> <scenario>: the last run against the verb's listed keys.
check_keys() {
  local verb=$1 scenario=$2 spec key type present kt ok
  while IFS= read -r spec; do
    key=$(jq -r .key <<<"$spec")
    type=$(jq -r '.type // "any"' <<<"$spec")
    present=false
    if [[ -n $J_OUT ]] && jq -e --arg k "$key" 'type == "object" and has($k)' <<<"$J_OUT" >/dev/null 2>&1; then
      present=true
    fi
    if jq -e --argjson c "$J_STATUS" '(.present_at_exit // []) | index($c) != null' <<<"$spec" >/dev/null && [[ $present != true ]]; then
      echo "[$verb/$scenario] exit $J_STATUS: key \"$key\" must be present; stdout: $J_OUT" >&2
      return 1
    fi
    if jq -e --argjson c "$J_STATUS" '(.absent_at_exit // []) | index($c) != null' <<<"$spec" >/dev/null && [[ $present == true ]]; then
      echo "[$verb/$scenario] exit $J_STATUS: key \"$key\" must be absent; stdout: $J_OUT" >&2
      return 1
    fi
    if [[ $present == true ]]; then
      kt=$(jq -r --arg k "$key" '.[$k] | type' <<<"$J_OUT")
      # A JSON null is "no value" for a key the consumer reads as optional
      # (juggler prints "cannot_complete":null), but it is a value for a
      # `json` key and a failure where the key is claimed present.
      if [[ $kt == null && $type != json ]]; then
        if jq -e --argjson c "$J_STATUS" '(.present_at_exit // []) | index($c) != null' <<<"$spec" >/dev/null; then
          echo "[$verb/$scenario] exit $J_STATUS: key \"$key\" is null but must carry a $type; stdout: $J_OUT" >&2
          return 1
        fi
      elif [[ $type != any && $type != json ]]; then
        ok=false
        local want
        for want in ${type//|/ }; do
          [[ $want == bool ]] && want=boolean
          [[ $kt == "$want" ]] && ok=true
        done
        if [[ $ok != true ]]; then
          echo "[$verb/$scenario] key \"$key\" has type $kt, the contract says $type; stdout: $J_OUT" >&2
          return 1
        fi
        # items: for an array, the types of the keys each element carries
        # (a key that is absent is not a failure).
        if [[ $type == array ]] && jq -e 'has("items")' <<<"$spec" >/dev/null; then
          local bad
          bad=$(jq -r --arg k "$key" --argjson items "$(jq -c .items <<<"$spec")" '
            .[$k][] | . as $e
            | $items | to_entries[]
            | select($e | has(.key))
            | select(($e[.key] | type) != (if .value == "bool" then "boolean" else .value end))
            | "\(.key) is \($e[.key] | type), want \(.value)"' <<<"$J_OUT" | head -n1)
          if [[ -n $bad ]]; then
            echo "[$verb/$scenario] an element of \"$key\": $bad; stdout: $J_OUT" >&2
            return 1
          fi
        fi
      fi
    fi
  done < <(verb_field "$verb" '(.stdout // [])[]')
}

# contract_check <file> <scratch dir>: the whole runner over one contract file.
contract_check() {
  CONTRACT=$1
  local scratch=$2 verb code row name rverb rcode mode matched n=0
  [[ $(jq -r .schema "$CONTRACT") == 1 ]] || { echo "$CONTRACT: schema is not 1" >&2; return 1; }
  while IFS= read -r verb; do
    while IFS= read -r code; do
      matched=0
      for row in "${SCENARIOS[@]}"; do
        IFS='|' read -r name rverb rcode _ <<<"$row"
        [[ $rverb == "$verb" && $rcode == "$code" ]] || continue
        matched=1
        for mode in base full; do
          n=$((n + 1))
          run_scenario "$name" "$mode" "$scratch/w$n" || { echo "[$verb/$name/$mode] scenario failed: $J_ERR" >&2; return 1; }
          if [[ $J_STATUS -ne $code ]]; then
            echo "[$verb/$name/$mode] exit $J_STATUS, want $code; argv $J_ARGV; stdout: $J_OUT; stderr: $J_ERR" >&2
            return 1
          fi
          check_keys "$verb" "$name/$mode" || return 1
        done
      done
      if [[ $matched -eq 0 ]]; then
        echo "no scenario for $verb exit $code (add one to lib/adapter.bash SCENARIOS)" >&2
        return 1
      fi
    done < <(verb_field "$verb" '.exit[]')
  done < <(jq -r '.verbs[].name' "$CONTRACT")
  echo "# contract check: $n scenario runs over $(jq '.verbs | length' "$CONTRACT") verbs" >&3
}

# --------------------------------------------------------------- recorded cases

# normalise_case <raw dir> <out dir> <world root>: the case's three text files
# through lib/normalise.jq with ids numbered across all three.
normalise_case() {
  local raw=$1 out=$2 root=$3 all f
  mkdir -p "$out"
  all=$(cat "$raw/args" "$raw/stdout" "$raw/stderr")
  for f in args stdout stderr; do
    jq -Rsj --arg all "$all" --arg root "$root" -f "$LIB_DIR/normalise.jq" "$raw/$f" >"$out/$f"
  done
  cp "$raw/rc" "$out/rc"
}

# record_scenario <name> <world root> <out dir>: run the scenario once (base
# mode) in a fresh world and write the NORMALISED case to <out dir>/<name>/.
record_scenario() {
  local name=$1 root=$2 out=$3 raw="$2/raw"
  run_scenario "$name" base "$root" || return 1
  mkdir -p "$raw"
  printf '%s\n' "$J_ARGV" >"$raw/args"
  cp "$root/stdout" "$raw/stdout"
  cp "$root/stderr" "$raw/stderr"
  printf '%s\n' "$J_STATUS" >"$raw/rc"
  normalise_case "$raw" "$out/$name" "$root"
}

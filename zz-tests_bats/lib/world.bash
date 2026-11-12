# A fresh, isolated platform world for one scenario: its own fake ringmaster /
# troupe / systemd-run / moxy / systemctl state, juggler state directory, models
# file and brief template, so a scenario starts from nothing and two runs of the
# same scenario can be compared byte for byte (after lib/normalise.jq).
#
# Needs from the including file: JUGGLER_BIN, FAKE_PLATFORM_BIN, and a running
# fake decisions endpoint (DEC_DIR, DEC_PORT; see setup_file in
# juggler_adapter_contract.bats). Loaded with `load 'lib/world.bash'`.

# world_up <root>: create the world under <root> and export its environment.
world_up() {
  W_ROOT=$1
  rm -rf "$W_ROOT"
  mkdir -p "$W_ROOT/fake" "$W_ROOT/fakebin" "$W_ROOT/state"
  export FAKE_DIR="$W_ROOT/fake" FAKE_BIN="$W_ROOT/fakebin"
  local tool
  for tool in ringmaster troupe systemd-run moxy systemctl; do
    printf '#!%s\nJUGGLERRUNTEST_FAKE=%s JUGGLERRUNTEST_DIR=%s exec %s "$@"\n' \
      "$BASH" "$tool" "$FAKE_DIR" "$FAKE_PLATFORM_BIN" >"$FAKE_BIN/$tool"
    chmod +x "$FAKE_BIN/$tool"
  done
  export JUGGLER_RINGMASTER_BIN="$FAKE_BIN/ringmaster"
  export JUGGLER_TROUPE_BIN="$FAKE_BIN/troupe"
  export JUGGLER_SYSTEMD_RUN_BIN="$FAKE_BIN/systemd-run"
  export JUGGLER_MOXY_BIN="$FAKE_BIN/moxy"
  export JUGGLER_SYSTEMCTL_BIN="$FAKE_BIN/systemctl"
  export XDG_STATE_HOME="$W_ROOT/state"
  export JUGGLER_MODELS_PATH="$W_ROOT/models.toml"
  unset CLOWN_SESSION_ID TROUPE_XMPP_USER TROUPE_XMPP_PASSWORD_FILE TROUPE_XMPP_DOMAIN
  export TROUPE_MINT_PASSWORD_FILE="$W_ROOT/minter.pw" TROUPE_MINT_USER=troupe-minter

  cat >"$JUGGLER_MODELS_PATH" <<EOF
[[model]]
name = "jev"
style = "decisions"
url = "http://127.0.0.1:$DEC_PORT"
token = "tok-test"
model = "typesafe/jev-test"
EOF
  cat >"$W_ROOT/issue-filer.toml" <<'EOF'
schema = 1
model = "jev"
system = "You file one issue per actionable item."
tools = ["ring_create_issue"]

[evaluator]
kind = "jq"
program = ".cannot_complete == null"

[limits]
steps = 3
wall_clock = "20s"
EOF
  printf '%s' '{"recording":"contract"}' >"$W_ROOT/input.json"
  printf 'task contract\n' >"$W_ROOT/task.txt"
}

# jug [-as <principal>] [--stdin <file>] <juggler args>: run the real binary in
# the current world. Sets J_STATUS, J_OUT (stdout) and J_ERR (stderr) and
# J_ARGV (the arguments as a JSON array). Never fails the caller.
jug() {
  local principal="" stdin_file=/dev/null
  while [[ ${1:-} == -as || ${1:-} == --stdin ]]; do
    case $1 in
      -as) principal=$2 ;;
      --stdin) stdin_file=$2 ;;
    esac
    shift 2
  done
  local a
  J_ARGV='[]'
  for a in "$@"; do J_ARGV=$(jq -c --arg a "$a" '. + [$a]' <<<"$J_ARGV"); done
  local envs=()
  [[ -n $principal ]] && envs+=("CLOWN_SESSION_ID=$principal")
  J_STATUS=0
  # stdout and stderr go to files first so their bytes (trailing newline
  # included) can be recorded exactly; J_OUT and J_ERR are the convenient
  # trimmed copies.
  env "${envs[@]}" "$JUGGLER_BIN" "$@" >"$W_ROOT/stdout" 2>"$W_ROOT/stderr" <"$stdin_file" || J_STATUS=$?
  J_OUT=$(<"$W_ROOT/stdout")
  J_ERR=$(<"$W_ROOT/stderr")
}

# jq_out <jq args...>: apply jq to the last stdout (fails on null/false: -e).
jq_out() { jq -er "$@" <<<"$J_OUT"; }

# set_decision <http status> <body>: the decisions endpoint's next answer.
set_decision() {
  printf '%s' "$1" >"$DEC_DIR/status"
  printf '%s' "$2" >"$DEC_DIR/body"
}

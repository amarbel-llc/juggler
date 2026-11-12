# Native restart and retry (juggler#4)

Status: design note, not built. Date: 2026-11-12. Tracking: juggler#4. Docs
only; no code until the operator rules on the questions at the end.

## What is asked, and what exists

Callers of `juggler spawn --wait` (roustabout's adapters, and the wait/retry
logic clown wraps around sessions) each write their own loop: map the exit code,
decide whether to try again, back off. The ask is a native restart facility: a
`[restart]` table in the brief, attempts recorded under the same run key, the
final state returned by `spawn --wait`, no duplicate after a caller crash,
explicit state across attempts, compatibility with futures and elastic
deadlines, and additive WaitRecord fields.

Facts from the code that shape the design (all read in the standalone at
c48de5e):

- **A job has one terminal, first write wins** (ringmaster RFC-0018 §2.4,
  quoted in `RunAgent`). A restart cannot be a second terminal on the same job.
- **A child record is one principal, one job, one unit**
  (`ChildRecord`, store.go) keyed by (run key, brief digest). `SpawnChild` is
  already idempotent on that key: the same brief and task returns the same child
  and starts no second unit (lifecycle.bats, "same brief and task returns the
  same child"). That is the existing answer to a caller that crashes and comes
  back.
- **`juggler run --job J` holds the job's advisory lock for the whole run**
  and refuses a duplicate producer without writing anything (agent.go
  `ErrAlreadyLocked`). So two supervisors cannot both drive one job.
- **The unit's lifetime is `RuntimeMaxSec = wall_clock + stop grace`**; the
  exit-wake hook (ExecStopPost) is the sole emitter of the wake and, for a unit
  that died, writes `interrupted` (reason `crash` or `killed`).
- **The loop already knows why a run ended** (`EndReason`: end_turn,
  cannot_complete, step_cap, timeout, tool_error, model_error, max_tokens,
  evaluator_pass), and `AgentVerdict` folds all but a passing evaluation into
  `failed` with a message prefix. A caller sees only exit 2 and a string.
- **Calls have kinds**; the read-only ones (list, search, read, get, query) are
  the ones whose repetition is harmless (`KindProducesArtifacts`).
- **Within an attempt there are already two retry layers**: the provider
  harness retries a 429/5xx per request (FDR 0021 `Transport`), and a parked
  tool call can be awaited (FDR 0022). A run-level restart is a third layer and
  must not multiply them blindly.

## Design

### 1. The brief setting

An optional table, absent by default (so nothing changes for any existing
brief, digest or ledger):

    [restart]
    max_attempts   = 3              # including the first; 1 = never restart
    on             = ["model_error", "tool_error", "max_tokens", "timeout"]
    backoff        = "5s"           # base; doubles, 20% jitter
    max_backoff    = "1m"
    deadline       = "10m"          # overall budget across attempts and backoff
    carry          = "fresh"        # "fresh" | "summary"
    after_effects  = false          # restart even if the failed attempt did real work

- `on` names loop end reasons, plus `evaluator_false` (end_turn, evaluator said
  no) and, for completeness, `cannot_complete`. `aborted` (a holder's cancel or
  SIGTERM) NEVER restarts, whatever `on` says: a cancel is a decision, not a
  fault.
- `limits.wall_clock` stays per attempt; `restart.deadline` bounds the whole
  sequence. The unit's `RuntimeMaxSec` becomes `restart.deadline + grace`, and
  `spawn --wait`'s default timeout the same (today `wall_clock + 2*grace`).
- The table is part of the brief, so it is part of the brief digest: a different
  restart policy is a different child. That is deliberate and consistent with
  the signed-contract rule.

### 2. Who restarts (the mechanism), with options

- **A. A supervisor inside `juggler run` (recommended for now).** `RunAgent`
  becomes a loop over attempts within ONE process, ONE job, ONE unit, holding
  the same job lock throughout. Attempts are journal `progress` records
  (journal-only, they wake nobody) and the ledger's `attempts`; the terminal is
  written once, after the last attempt. The exit-wake hook is untouched and still
  emits the single wake. Because the unit continues without any caller, **a
  crashed caller cannot cause a duplicate or a lost restart**: re-running
  `spawn --brief ... --wait` finds the child by digest and waits for the same
  job. Limit: it cannot restart a process that died (OOM, SIGKILL, host
  reboot), which is the `crash`/`killed` class the hook reports.
- **B. The exit-wake hook relaunches after a crash.** Covers process death, but
  the hook runs inside the stopping unit and would need systemd-run rights,
  the staged brief and the child's identity; and it must not write a terminal
  when it relaunches. Whether a confined unit can do that is UNVERIFIED.
- **C. The waiting caller loops** (the current external pattern, made native in
  `spawn --wait`). Needs a live waiter, so an un-waited `spawn` is never
  restarted, and two callers racing would need an atomic attempt claim on the
  child record.
- **D. systemd `Restart=`** on the transient unit. The hook would write
  `interrupted` on the first stop and, being first-write-wins, end the job;
  the hook would have to know a restart is pending. How systemd exposes that to
  ExecStopPost is UNVERIFIED.

Recommendation: **A now**, covering the failures the loop can classify
(model_error, tool_error, max_tokens, timeout, step_cap, evaluator false). Treat
process-death restarts (B or D) as a later phase, because they raise the
duplicate-effects hazard below and need their own ruling.

### 3. Recording attempts

Additive and omitted when there is no restart policy.

- **Ledger.** `attempts: [{n, started, ended, end, state, reason, message,
  ledger, transcript, transcript_digest}]`, and every `calls[]` entry gains
  `attempt: n`. `steps` and the provider totals are summed across attempts.
  The evaluator therefore sees all calls of the run, tagged by attempt, so
  "an ok issue exists" holds even when the issue was made on attempt 1.
- **Per-attempt files.** The transcript path gains the attempt
  (`transcripts/<principal>/<job>.<n>.json`); `traffic.jsonl` already appends
  per job. `juggler clean --resolved` sweeps them with the rest.
- **Room.** Each attempt's turns go to the same room, with one marker stanza
  per restart ("attempt 2 of 3 after model_error") so a reader can tell attempts
  apart.
- **Journal.** `progress` records "attempt n started" and "attempt n ended: ...".

### 4. State carried across attempts

- **`fresh` (default).** A new loop with the same task and tools. Simple, but a
  repeat can redo work.
- **`summary`.** Fresh, plus a short preface to the task: how the previous
  attempt ended and which calls already succeeded (tool, kind, URIs). It guides
  the model away from repeating.
- **`transcript` (replay the prior turns) is not in this design.** A failed
  attempt can end on an unanswered tool_call or a truncated reply, which
  providers reject, and the context only grows. It can be revisited.

### 5. The duplicate-effects hazard, and the default rule

A restart after a successful non-read-only call (a created issue, a posted
note) can do the work twice; this is the same hazard FDR 0022 removed for
timeouts. Default rule: **a failed attempt that has an `ok` call whose kind is
not read-only is not restarted** (`restart_refused: effects`, recorded in the
attempts), unless `after_effects = true`. Futures count: a parked call that
finished while the run ended is settled into the ledger first, so its effect
is seen.

### 6. Futures, elastic deadlines, provider retries

- A parked future (FDR 0022) is not a failure; a slow call does not trigger a
  restart. At the end of an attempt `finishFutures` already cancels what is
  still running, so nothing leaks into the next attempt.
- Elastic idle/ceiling deadlines (FDR 0022 §2, not built) govern one CALL.
  `restart.deadline` bounds the whole sequence and always wins, as the run's
  wall clock does for a call today.
- Provider-request retries (FDR 0021) happen inside an attempt. A `model_error`
  that reaches the run level has already exhausted them, so restarting on it is
  a coarse second chance, not a multiplication of the same fine retry. The
  defaults should keep the product small (for example, request retries of 3 and
  run attempts of 3 is up to nine requests to a failing endpoint) and the
  `deadline` is the cap.

### 7. WaitRecord and exit codes

- `state`, `reason`, `message` and the exit code are those of the FINAL
  attempt, so every caller that ignores the new fields keeps working.
- New optional key `attempts` (a summary array: n, state, reason, message,
  started, ended, ledger, transcript). Absent for a run with no restart policy.
- Exit 5 ("still running at the timeout") may now mean "still restarting";
  the default `--timeout` follows `restart.deadline`.
- The contract test (`contract:` in the lifecycle lane) gains the
  additive key; its existing assertions do not change.

### 8. Idempotence and a caller crash

With mechanism A the supervisor is the unit, not the caller, so: a caller that
dies before `spawn` returns is covered by the existing digest idempotence; a
caller that dies after it is irrelevant to the restarts; a caller that comes
back runs the same `spawn --brief ... --wait` and waits for the one job. No new
claim protocol is needed. A crash of the unit mid-sequence ends as today
(`interrupted`/`crash` written by the hook), now with the attempts so far in
the ledger when it was written.

### 9. What it does not do

Restart sessions of other tools; schedule across hosts; restart a cancelled
run; replay a transcript; restart after process death (phase 2, if ruled).

## Compatibility

Everything is additive and off by default: no `[restart]` means one attempt,
the same files, the same stdout keys and the same digests as today. A brief
with `[restart]` is rejected by an older juggler (unknown key), so, as with
`[result]`, it is safe only against this standalone.

## Open questions

1. **Mechanism.** A (in-process, recommended), with process-death restarts
   (B or D) deferred, or B/D now? B and D each rest on something I could not
   verify (a confined unit's ability to relaunch; what ExecStopPost can learn
   about a pending systemd restart).
2. **Default.** Off (`max_attempts` absent = 1)? Recommended.
3. **Effects rule** (section 5): block restart after a successful
   non-read-only call unless `after_effects`? Recommended.
4. **Carry modes**: `fresh` default, `summary` opt-in, `transcript` out of
   scope? Or a different default?
5. **Evaluator input**: all calls across attempts, tagged by `attempt`
   (recommended), or only the final attempt's?
6. **Which end reasons restart** when a policy is present and `on` is omitted:
   model_error, tool_error, max_tokens, timeout (recommended); not
   cannot_complete, step_cap or an evaluator-false verdict (the agent's own
   judgement); never aborted.
7. **Deadlines**: `restart.deadline` required whenever `max_attempts > 1`
   (recommended, so `RuntimeMaxSec` is never guessed)?
8. **Owners.** What rules do roustabout's adapters and clown's wrapper apply
   today (which exit codes, how many tries, what backoff)? I have not read
   clown's logic, and roustabout's retries are its lane. The defaults above
   are mine, not derived from theirs. Should the adapters drop their loops
   once this exists (roustabout decides), or keep them as an outer bound?
9. **Cost.** Provider usage and cost are summed across attempts in
   `ledger.provider`; is a per-run cost cap wanted before restarts can spend
   more? (None is proposed; FDR 0021 lists it as a lever.)
10. **Retention**: per-attempt transcripts multiply files; the 30-day
    `clean --resolved` sweep and the 8 MiB cap apply per file. Acceptable?

## Not built

No code, brief schema, ledger field or test exists for any of this yet. Next
step on a go: a `[restart]` parser with validation (tests first), the attempt
loop in `RunAgent` behind the table, the ledger/WaitRecord fields, `RuntimeMaxSec`
and the `--wait` default from `restart.deadline`, then the bats cases (restart on
model_error, no restart on aborted, no restart after an effect, caller re-run
waits for the same job).

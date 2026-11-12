---
status: proposed
date: 2026-11-12
promotion-criteria: >
  experimental once one tool call in a `juggler run` is a future the loop owns:
  it outlives the wait that timed out, the model gets a handle and can await or
  cancel it, an idle-timeout/ceiling pair replaces the fixed 30 s, and a host
  broker caps concurrent futures of one class across two runs, all under the
  fake ringmaster/moxy in the bats lane.
---

# Futures, elastic deadlines and host resource multiplexing for tool calls

> **Origin and reading.** Operator direction relayed by the coordinator,
> 2026-11-12: "futures managed by juggler, elastic deadlines, host resource
> multiplexing. Design note first." The direction is three phrases; the
> interpretations below are mine and are the first thing to confirm (see
> *Open questions*, 1). Design only; no code. It builds on juggler#2 (the
> per-call deadline as an error result) and FDR 0020 (async jobs), and
> supersedes nothing yet.

## Problem Statement

A tool call today has one fixed budget: `jugglertools.DefaultCallTimeout`, 30 s
per round trip, the same for a 10 ms lookup and a cold 370-issue listing
(juggler#2's production case). Since juggler#2 an expired call reaches the model
as an `isError` result, but the call is **abandoned**: the work may still be
running upstream, so the model's retry can run it twice, and a call that was
making steady progress is cut off exactly like one that was hung. Separately,
nothing coordinates the several runs and calls that share one host: each takes
what it takes, so a burst of concurrent heavy calls (or of local model
inference, which the juggler daemon already fronts) can starve each other and
the box, and a deadline set for an idle host is wrong on a loaded one.

## Interface

### 1. A tool call is a future the loop owns

Every tool call becomes a **future**: an id, the call (name, args digest), a
state, and a deadline policy, kept by the loop for the life of the run.

    pending -> running -> done | failed | cancelled | lost
                       \-> waiting (the wait expired; the work is still running)

- **A synchronous call is a future the loop awaits.** The model sees no change:
  it calls, it gets a result. There is one mechanism, not a sync path and an
  async path.
- **FDR 0020's async-start is the same future, not awaited.** `job_wait`,
  `job_status` and `job_cancel` from that record become `await`, `status`,
  `cancel` on futures, still acting only on futures this run started.
- **When the wait expires the future is NOT abandoned.** The model gets a
  result that says so and carries the handle: `{"state":"waiting",
  "future":"f-3","elapsed_ms":30000,"note":"still running; await or cancel"}`.
  It can `await` again (a second bounded wait), `cancel`, or carry on and be
  woken by the future's completion event (FDR 0020). This removes juggler#2's
  double-execution hazard: a retry is an `await`, not a second run.
  **This is a behaviour change from juggler#2**, which abandons and tells the
  model to retry; it must be confirmed (*Open questions*, 2).
- **Lost** is the honest terminal state for a future whose upstream is gone
  (moxy restarted, connection torn) without a result; the model is told, and the
  ledger records `ok:false` with that reason.
- Futures are recorded in the ledger and traffic log like calls today; a
  `waiting` that later completes updates its own entry, it is not a second
  call.

### 2. Elastic deadlines: idle timeout plus a ceiling, not one number

A fixed total timeout is replaced by two bounds per future class:

| Bound | Meaning | Today |
|---|---|---|
| `idle` | longest the upstream may show **no sign of life** (no progress event, no bytes) | the whole 30 s |
| `ceiling` | hard cap on one future's total running time, whatever its progress | none per call (only the run's wall clock) |

- **Liveness.** Any progress signal resets `idle`: an MCP progress
  notification, an SSE heartbeat, or a streamed chunk. The client's SSE reader
  already tolerates "heartbeat or progress events [that] may precede" the
  final reply (`transport.go`), but it reads the response whole and discards
  them, so using them means streaming the read. Whether moxy forwards a child
  tool's progress at all is unverified (*Open questions*, 4).
- **A tool that emits no progress** gets `idle` equal to its whole budget, i.e.
  today's behaviour: the elastic part costs nothing for tools that cannot show
  liveness.
- **Per-class budgets.** A tool's class comes from the `kind` it already
  declares (read-only `list`/`search`/`read`/`get`/`query` kinds exist today)
  or a brief entry `[tools.<name>] idle = "30s" ceiling = "10m"`; the default
  is `idle = 30s`, `ceiling = 30s` (exactly today's). Raising a tool's ceiling
  is an explicit brief choice, bounded by the run's wall clock, which still
  wins: a future never extends a run past its `WallClock`.
- **Load-aware (optional).** The broker (below) may stretch `idle` while the
  host is saturated, since a slow tool on a busy box is not a hung tool; the
  stretch is capped and never past `ceiling`.

### 3. Host resource multiplexing: a broker for the shared box

A per-host **broker** admits futures against resource classes so concurrent runs
do not starve each other:

- **Classes.** Small, named, declared by the brief or inferred from kind:
  `io` (network/tool-server calls), `cpu` (local compute), `infer` (local
  model inference via the juggler daemon). Each class has a slot count.
- **Admission, not a timeout.** A future waits for a slot before it runs; the
  time queued is not charged to its `idle` or `ceiling` (the run's wall clock
  still is). The model sees the state `queued` in `status`.
- **Host signals set the slot counts**, not hard-coded numbers: load average
  per CPU and available memory, the same two signals the fleet's gate rule
  already uses (per-CPU load under a threshold and a floor of free memory
  before starting a heavy build). The policy (thresholds, per-class floors) is
  configuration, with the operator's current numbers as an example, never a
  constant in the code.
- **Fairness.** Slots are shared across runs on the host by round-robin over
  runs, so one run's burst cannot hold all of a class.
- **Where it lives.** juggler already runs a per-host daemon (the llama-server
  control plane) and owns `infer`; the broker is a natural second duty of that
  process, with a daemon-less fallback (a run with no daemon uses a local,
  per-process broker and gets no cross-run fairness). Whether that is the right
  home is open (*Open questions*, 5).

## Examples

A brief that gives a slow, progress-reporting listing room and keeps the rest at
today's budget:

    [tools.issues_list_issues]
    idle    = "30s"      # dies only if it goes quiet for 30 s
    ceiling = "5m"       # but never runs past 5 minutes

    # call 1: model calls issues_list_issues -> future f-1 running
    # t+30s: progress events kept arriving -> no timeout
    # t+95s: result arrives, delivered as the call's result

The same call against an upstream that emits no progress: at t+30 s the wait
expires and the model receives `{state: "waiting", future: "f-1", ...}`. It
may `await f-1` once more, `cancel f-1`, or continue; the work is not run twice.

Two runs on one host each start four `cpu` futures; with the broker's `cpu`
slots at 4 (a loaded host: 2), each run is admitted two at a time, in
alternation, and `status` shows the rest as `queued`, with their queue time not
counted against their `idle`.

## Limitations

- Design only; every name here (`waiting`, `[tools.*]`, `idle`/`ceiling`,
  classes) is a proposal.
- A future's cancellation is best-effort: `cancel` tells the upstream to stop;
  whether the upstream honours it is the tool server's and moxy's (the loop
  records `cancelled` when it stopped waiting, not that the work stopped).
- Elasticity needs a liveness signal. Tools that emit none get no benefit and
  keep a fixed budget by design.
- The broker multiplexes juggler's own futures. It does not gate unrelated
  processes on the host (a `nix build` outside any run) and does not replace the
  fleet's operator-level gate rule; it takes the same signals as input.
- Single host. No cross-host scheduling.

## Tuning Levers

| Lever | Current | Rationale | Change signal |
|---|---|---|---|
| default `idle` | 30 s (today's) | no behaviour change until a brief opts in | tools routinely killed while progressing |
| default `ceiling` | = `idle` (30 s) | same | briefs set a ceiling on most tools |
| `waiting` re-wait bound | undecided (suggest 1 re-await, then cancel prompt) | stops a model looping on a stuck future | models await indefinitely |
| slots per class | undecided (derived from host signals) | host-dependent by nature | queue time dominates run time |
| load stretch cap | undecided (suggest 2x `idle`) | slow is not hung, within reason | hung tools survive too long |
| queue wait cap | none beyond the run wall clock | admission is not a failure | runs end timeout while queued |

## Open questions

1. **Operator / coordinator: confirm the reading.** (a) *Futures managed by
   juggler*: tool calls as loop-owned futures with handles, unified with FDR
   0020's async jobs, as above. (b) *Elastic deadlines*: progress-liveness
   `idle` plus a `ceiling`, optionally stretched by host load. (c) *Host
   resource multiplexing*: a per-host broker with resource classes and
   load-derived slots. If "elastic" meant only load-based, or "multiplexing"
   meant sharing the llama-server across runs only, §2 and §3 narrow.
2. **Operator: `waiting` instead of abandon.** juggler#2 chose abandon-and-retry;
   this record proposes keeping the work and giving the model a handle. Do you
   want that change (it removes double execution but keeps work running past a
   deadline the operator may have meant as a cut-off)? A brief flag could keep
   the abandon behaviour for tools that must not outlive a deadline.
3. **ringmaster:** is each future a ringmaster job (for `reap`, `exit-wake`,
   handles) or only the async ones FDR 0020 describes? The cost is a journal
   entry per tool call.
4. **moxy:** does moxy forward a child tool's progress notifications on
   `tools/call`, and can it cancel an in-flight call? Without both, `idle` has
   no signal and `cancel` is local only. Unverified; owner moxy.
5. **juggler daemon / spinclass:** is the daemon the right home for the broker,
   and should its host-signal policy read the same configuration spinclass's
   host-load tool uses (one source for "is this host too busy")? Owner: spinclass
   for the policy source, juggler for the broker.
6. **FDR numbering:** this is juggler's FDR 0022; clown's FDR numbers overlap
   juggler's by number (see FDR 0021).

## More Information

- juggler#2 — the per-call deadline as an error result (the behaviour this
  record would refine); `internal/bravo/jugglertools/client.go`
  (`DefaultCallTimeout`, `Call`), `transport.go` (SSE framing).
- FDR 0020 — async jobs and event sources: futures are its async job, unified.
- FDR 0019 — §5 tool plane (offered set, reserved moxy meta tools: moxy's own
  `async` stays refused; futures are juggler's), §4 ledger.
- FDR 0021 — the provider harness; model requests could be futures of class
  `infer` later (out of scope here).
- Code anchors: `internal/alfa/jugglerloop/loop.go` (`run.execute`, the
  synchronous `Exec.Call`), `internal/bravo/jugglertools/client.go`.

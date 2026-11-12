---
status: proposed
date: 2026-11-12
promotion-criteria: >
  experimental once a juggler run, with one event source and one async job
  wired, receives a wake between two model steps and the model acts on it,
  end to end under the fake ringmaster/troupe in the bats lane, with the
  worker unable to name, stop or reconfigure the source from the tool list.
---

# Loop event sources and async jobs — monitors the worker cannot stop

> **Scope.** This record is the juggler side of juggler#3 only: the loop's
> event-source interface, the tool-list shape for async jobs, and how events
> are delivered under the loop's permission model. It is a design note; no code
> exists. Questions that belong to clown, ringmaster, troupe, moxy or spinclass
> are listed under *Open questions* and are NOT answered here — their owners
> decide them in their own repos.

## Problem Statement

Under Claude Code, an unattended worker learns that a job finished or a chat
message arrived through two clown-owned monitors (the job-wakeup channel and
`troupe receive`) run as `Monitor` tasks, and it manages background work with
`Monitor` and `TaskStop`. clown#330 locked that down as a bridge: unattended
workers lose `Monitor`, and `TaskStop` is refused for the wake and troupe
monitors unless the worker started the task itself. A juggler run has none of
this: `jugglerloop.Run` is a synchronous model-then-tools cycle that ends the
first time the model replies without a tool call, so nothing can reach the
model mid-run and a long tool call can only be waited on. Now that juggler owns
the turn loop, the monitors and async jobs can be loop-level parts of the run
instead of harness tasks the worker could in principle stop or open-world poll.

## Interface

### 1. Event sources are loop inputs, not tools

The loop gains an event-source seam in `jugglerloop.Config`, next to `OnTurn`
and `StopWhen`:

    type Event struct {
        Source string          // "wake", "troupe", "job:<id>": set by the source, never by the model
        At     time.Time
        Text   string          // the rendering the model sees
        Raw    json.RawMessage // the source's own record, for the ledger/traffic only
    }
    type EventSource interface {
        Name() string
        Events() <-chan Event   // closed when the source ends
    }
    // Config.Sources []EventSource

Sources are constructed by the host (`juggler run`) from the brief and the unit
environment, never named by the model. A source is not a tool: it appears in no
`tools/list`, is not in the loop's `offered` set, and has no handle, id or name
the model can pass to anything. "The worker cannot stop it" is therefore a
structural property, not a refusal rule: there is no call that reaches it, and
the existing call-time filter (clown#261, `tools` enforced) already turns any
hallucinated name into an error result. The source dies only with the run
context (wall clock, parent cancel, terminal), which the loop owns.

The two sources v1 describes:

| Source | What it emits | Carried by |
|---|---|---|
| `wake` | one event per job-wakeup the run's principal receives (job terminal, job message) | the ringmaster job-wakeup channel for the run's session key |
| `troupe` | one event per message addressed to the run's principal (DM, its MUC) | `troupe` receive for the run's identity |

Their wire details are the owners' (see *Open questions*); juggler consumes
whatever they expose through a small adapter each, and the `EventSource`
interface is the only thing the loop depends on, so a fake source in tests
needs no ringmaster or troupe.

### 2. Delivery: between steps, as turns, with a distinct sender

Events are drained at exactly one point: at the top of the loop, after the
wall-clock and step-cap checks and before each model request, so an event can
never extend a run past either limit. Everything drained is appended as one
`BodyText` turn per event, in arrival order, ahead of that request.
Properties:

- **Sender.** `run.append` stamps `Config.Principal` on every turn it appends,
  and `classifyTurn` maps a text turn from the agent's own principal to the
  assistant role. An event turn MUST carry the event's source as sender
  (`wake`, `troupe`), so the codecs send it as a user turn and the MUC
  transcript shows who said it. `append` therefore takes a sender.
- **Untrusted data.** Event text is rendered inside a fixed envelope
  (`[event wake] ...`) and treated as data: it cannot add tools, widen
  `allow-tools`, or change the brief. A troupe message from another principal
  is input to the model, not an instruction to juggler. Size is capped per
  event and per drain; the excess is summarised as a count, not dropped
  silently (the ledger records it).
- **Steps.** Events cost nothing; model requests cost steps as today. A flood
  therefore cannot exhaust the step cap by itself, but it can exhaust the wall
  clock, which already bounds the run.
- **Transcript and ledger.** Event turns go through `OnTurn` like any other, so
  the run's MUC and journal show them; `Raw` goes to the traffic record, not
  the transcript, as large tool arguments do today (clown#255).

### 3. Idle: when the model stops but work is outstanding

Today a reply with no tool calls ends the run (`end_turn`). With event sources
or outstanding async jobs that is no longer always right: the model may be done
for now but waiting on a job. The loop gets one rule, chosen by the brief:

- `[loop] idle = "end"` (default, today's behaviour): a reply with no tool
  calls ends the run; outstanding jobs are abandoned and recorded as such in the
  ledger.
- `[loop] idle = "wait"`: a reply with no tool calls while at least one async
  job started by this run is outstanding parks the loop until an event
  arrives, the wall clock expires or the context is cancelled. With no
  outstanding job it ends as `end_turn`, so a run cannot idle forever on an
  empty queue. The park is bounded by the wall clock; it has no timer of its
  own.

### 4. Async jobs in the tool list

A tool call can be started asynchronously without an open-world `Monitor`:

- For each tool the brief offers, the loop MAY also offer an async-start form
  (name rule open, see below), which runs the same call through the same
  `ToolExecutor` in the background and returns at once with
  `{job: "<id>"}`. The call passes the same `offered`-set check, argument
  validation and (where moxy enforces it) permission tier as the synchronous
  form; async is a scheduling choice, not a widening.
- Three fixed loop tools are offered whenever async is: `job_wait {job,
  timeout}`, `job_status {job}`, `job_cancel {job}`. They act ONLY on jobs this
  run started (a set the loop keeps); an id outside the set is an error result,
  which is exactly clown#330's rule for `TaskStop` (the worker can stop what
  it started, nothing else). They are juggler's own tools, not moxy's `async`,
  `async-result` or `async-cancel`, which stay reserved and refused in a brief
  (FDR 0019 §5: they let a nested call bypass the filter and read the clown
  journal across sessions).
- Completion arrives two ways, both optional to the model: an event (source
  `job:<id>`) at the next drain, carrying the result, and `job_wait` for a
  model that wants to block. No polling is needed, so no `Monitor` is.
- Each async job is a ringmaster job in the run's namespace (`ringmaster
  start` with the run's source and target), so a spawner's handle, the reaper
  and `exit-wake` see it like any other job. Its terminal record is written by
  the loop when the call returns, with the run ledger entry as its result
  reference.

### 5. How the same events reach a worker under the permission model

The model sees events only as turns the loop appends; it acts on them only
through the offered tool set, which is the brief's `tools` plus
`cannot_complete` plus (when async is on) the three job tools. Nothing an event
says changes that set. A worker in a Claude Code harness (clown-mediated) keeps
getting the same events as `Monitor` notifications; the loop-level sources are
for juggler-run agents, and the two are the same ringmaster/troupe streams
consumed by different hosts, never both by one run (see *Open questions*, 2).

## Examples

A brief that runs a slow listing in the background and waits for it:

    [loop]
    idle = "wait"
    [events]
    sources = ["wake", "troupe"]

    # model turn 1: calls issues_list_issues_async {repo: "..."}  -> {job: "j-7"}
    # model turn 2: replies "listing in progress"  (no tool calls, 1 job outstanding)
    #   loop parks (idle = "wait")
    # event: [event job:j-7] ok, 370 issues (result in the ledger)
    # model turn 3: reads the event, calls create_issue ..., replies, run ends end_turn

The same flow today ends at turn 2 as `end_turn` with the job abandoned, or dies
as `tool_error` if the synchronous call outruns the 30 s per-call deadline
(juggler#2 turned that case into an error result the model can retry).

## Limitations

- Design only. Nothing here is implemented and the names (`Sources`, `[loop]
  idle`, the async-start naming) are proposals.
- The loop is single-threaded between steps: events are not delivered while a
  synchronous tool call runs, only at the next drain. Async is how a model gets
  concurrency.
- Events are not authoritative. A wake says a job finished; the ledger and the
  journal remain the record of truth, and the model never claims success
  (FDR 0019 §4).
- Multiple concurrent async jobs share the per-call deadline and moxy's
  concurrency; juggler does not schedule or prioritise them.

## Tuning Levers

| Lever | Current | Rationale | Change signal |
|---|---|---|---|
| `idle` default | `end` | today's behaviour; opt-in avoids stranding existing briefs | most event-source briefs set `wait` |
| per-event size cap | undecided (suggest 4 KiB) | an event is a pointer, not a payload | models routinely need the truncated tail |
| events per drain | undecided (suggest 16) | bounds prompt growth from a flood | drains regularly summarised |
| max outstanding async jobs | undecided (suggest 4) | bounds fan-out under one principal | legitimate briefs hit it |

## Open questions

Each is for the named owner to answer in their own repo; juggler's design above
holds whichever way they answer, because it depends only on the `EventSource`
interface.

1. **ringmaster:** can a run's principal subscribe to the wakeups for the jobs
   it started, rather than the whole session channel, and does the v3 protocol
   (`jobwake.ProtocolVersion`) already allow a non-session consumer? Who ACKs
   or advances the cursor when juggler, not Claude Code, is the reader?
2. **clown:** when a juggler run is hosted inside a Claude Code harness (clown
   mediation), does clown keep owning the monitors and juggler's `wake` and
   `troupe` sources stay off? What does the `Monitor`-notification text look
   like today, so the event envelope can match it?
3. **troupe:** what does `troupe receive` expose to a non-harness reader (JSON
   line per message? DM versus MUC distinction, reconnect and replay behaviour,
   the cursor `chat_read` advances), and is a message from the run's own
   principal echoed back?
4. **moxy:** are `async`, `async-result` and `async-cancel` going to be
   enforceable per call (moxy#485/#486), and should juggler's async-start reuse
   moxy's `async` as the mechanism instead of running the call itself?
   Recommendation: juggler-owned, because the ownership set and the ringmaster
   job record are the loop's.
5. **spinclass:** does an async job started by a run count as a child the
   spawner's handle (FDR 0032) covers, or only the run?

## More Information

- juggler#3 — this record's request; juggler#2 — per-call deadline as an
  error result, the first piece of the same tool-plane behaviour.
- FDR 0019 — the agent substrate: §5 tool plane (reserved moxy meta tools,
  `tools` enforced), §6 lifecycle (ringmaster jobs), §7 transcript.
- clown#330 — the monitors lockdown this replaces at the loop level;
  `docs/plans/2026-10-27-monitors-lockdown-e2e.md` in clown records the
  poshterity proof.
- Code anchors: `internal/alfa/jugglerloop/loop.go` (`Config`, `run.loop`,
  `run.append`, `offered`), `codec.go` (`classifyTurn`).

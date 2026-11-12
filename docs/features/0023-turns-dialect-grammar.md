---
status: proposed
date: 2026-11-12
promotion-criteria: >
  experimental once a loop dialect document is pinned by digest at spawn, a
  brief that violates it is refused before anything is minted, the digest is
  recorded in the child record, and the implicit `brief/1` dialect leaves every
  existing brief and ledger byte-identical, all in the bats lane with no network.
---

# The turns dialect — juggler's half of "one grammar, two dialects"

> **Joint note.** This is the juggler half. The other half is roustabout's
> `docs/grammar-dialects.md` (agents, tools, graphs, and the shared-core library
> API), written by the roustabout session; neither side writes into the other's
> repo, and each links to the other. The operator's framing, relayed by the
> coordinator: "a grammar with two dialects or applications would make sense
> here ... juggler for turns, roustabout for agents/tools/graphs". Design only;
> no code. **The operator ruled (2026-11-12, relayed): the grammar is a pinned
> schema for authored documents, like roustabout's grammar files; both dialects
> share the pinning and loader machinery** (option (a) of *Open questions*, 1).
> So a dialect here validates *authored documents* (briefs, scripts); the
> runtime records a run produces (turns, ledger, provider metadata, event turns,
> future handles) are what the document *produces*, mapped in *Runtime records*
> below, not what the grammar validates.

## Problem Statement

roustabout (the graph runtime above juggler and ringmaster, circus FDR-0043)
has a grammar for graphs: a host-supplied, digest-pinned document that says
which node kinds, edges and result steps a graph may use, validating
"sentences". juggler has the same need one level down and no such thing: a
`juggler run` is shaped by a brief, a closed TOML vocabulary that rejects unknown
keys, but nothing lets a host pin *what shape of run is acceptable* (which
tools may be offered at which step, which stop conditions and budgets are
allowed, whether futures and event sources are permitted) separately from the
per-run parameters. Without it, the permission surface of a run is whatever
its brief says, and two repos grow two incompatible notions of "pinned,
versioned, closed-vocabulary document".

## Interface

### 1. The boundary

roustabout orders **runs**: agents, tools, graph, edges, the grammar right.
juggler orders **turns inside one run**. A roustabout `agent` node references a
loop dialect by digest the way it references a brief today (`brief`: a sha256
and a file) and passes it through to juggler; roustabout does not learn what a
turn is (its stated position). Conversely juggler never orders runs.

### 2. What a loop dialect describes

All of this exists in code already, as brief fields or loop behaviour; the
dialect names it and makes it pinnable. No new runtime concepts.

| Dialect element | Today | Source |
|---|---|---|
| **Turn kinds** | `text` (model reply, or a non-agent sender's input), `tool_call`, `tool_result`, `terminal`; a future handle is a `tool_result` with a `waiting` content shape; an injected event (FDR 0020) is a `text` turn from a non-agent sender | `jugglerloop.Body`, `classifyTurn` |
| **Tools offered** | the brief's `tools` list plus loop-injected `cannot_complete` and, with futures, `future_await`/`future_cancel`; enforced at call time | `Config.Tools`, `offered` |
| **Per-step tool offering** | not expressible; the set is fixed for the run | (new) |
| **Stop conditions** | `end_turn`, `cannot_complete`, `step_cap`, `timeout`, `tool_error`, `model_error`, `max_tokens`, `evaluator_pass` | `EndReason` |
| **Budgets** | `limits.steps`, `limits.wall_clock`, `limits.call_wait`, `limits.abandon_tools`; token/cost cap not proposed | `jugglerbrief.Limits` |
| **Futures / deadlines** | FDR 0022: handles, abandon flag; idle/ceiling and broker are later slices | `Config.Futures` |
| **Provider adapter** | FDR 0021: `provider` per model entry | `jugglerprovider` |
| **Script** | `ScriptTemplate`: canned replies | `jugglerprovider.ScriptTemplate` |

The one genuinely new capability is **per-step tool offering**: a dialect may
say "steps 1..k offer `list_*` only, then `create_*`". It can only **narrow**
the brief's `tools`, never widen them (§4).

**`ScriptTemplate` and the dialect.** A script is a sentence with no model:
canned replies in the same turn vocabulary. It is *not* the first sentence of
the loop dialect; it belongs to a degenerate sibling dialect `script/1`, in the
way roustabout's `attest/1` is a second request type beside `issue-filer/1`.
Recommendation: yes to a shared turn vocabulary, no to calling it the loop
dialect's first sentence. It earns its keep as the deterministic double and as
the "bottom fallback script" of FDR 0019.

### 3. The shared grammar machinery

roustabout's existing shape (read from `internal/0/graph/grammar.go`): a
`Grammar` JSON document `{grammar, version, kinds, edges, constraints, result,
request}`; `ParseGrammar` refuses unknown fields and unknown versions; the host
**pins the grammar by sha256 digest** and the document never carries the
grammar that validates it; the **grammar right** is derived from the digest
(`roustabout:grammar:<digest>`), never supplied by a document; a grammar names
a **request type** (`issue-filer/1`, `attest/1`) that selects the sentence
parser.

Proposed shared core (roustabout's own suggestion, which I adopt): *pin by
digest; a versioned document with a name and a closed vocabulary; the request
type a grammar names; the grammar right named from the digest.* Roustabout-only:
the node-kind table, outcomes, edges, result steps, the walker.

**Where the library lives (options).**

- **A. A small shared package below both** (working name `grammar`): `Pin(raw,
  wantDigest)`, a strict versioned decoder (`DisallowUnknownFields`, a known
  version set), `Right(owner, digest)`, and a request-type registry. **Ruled
  (operator, 2026-11-12, Q22): "shared spec first, then a small module below
  both, but the shared module lives in juggler".** So: the spec first (this
  section, no code); the module is created in **juggler**, exposed through
  `pkgs/` as the only importable surface (the facade rule in AGENTS.md), when
  the turns dialect has a parser; roustabout, which sits above juggler, imports
  it, so the layering holds. Neither owns the other's dialect.
- **B. Each repo carries its own copy** with a shared conformance fixture set.
- **C. juggler imports roustabout's package.** Rejected: roustabout is *above*
  juggler (nothing here may import what sits above it), and its package is
  `internal/`.

Ruled: **A**, in juggler (above); B and C are not pursued. API sketch (names
are proposals; written when the module is created):

    package grammar
    func Digest(raw []byte) string                      // "sha256:<hex>"
    func Pin(raw []byte, want string) error             // digest equality, constant-time
    func Decode[T any](raw []byte, versions ...int) (*T, error) // closed, versioned
    func Right(owner, digest string) string             // "<owner>:grammar:<digest>"

roustabout would change only how `ParseGrammar` decodes/version-checks and how
`GrammarRight` is built; its kinds, edges and walker stay. That is roustabout's
call; I assert nothing about its code beyond what it published.

### 4. The permission surface

A dialect that offers tools per step is a **permission surface**. Rules:

1. **Monotone.** A dialect can only narrow: the tools it offers at any step are a
   subset of the brief's `tools`, which are a subset of the spawner's effective
   moxyfile (the launcher-side `moxy narrows` check of FDR 0019 §5 stands and
   runs first). A dialect cannot add a tool, widen `allow-tools`, or raise a
   budget past what the brief states.
2. **Pinned by the host.** The dialect is supplied by the spawner and pinned by
   digest; the brief never carries the dialect that validates it, mirroring
   roustabout.
3. **Who may pin** (the "grammar right") is **not decided here.** juggler can
   check digest equality and record the digest; whether the spawner holds the
   right to pin a given digest is a policy question for spinclass FDR 0032 and
   the principal machinery. As relayed to me, ajour is a debugging surface
   today, not an enforcer; I treat enforcement as unowned until the operator
   says otherwise (*Open questions*, 3).
4. **Recorded.** The digest is recorded in the child record and the run ledger,
   so a post-hoc reader sees which dialect a run was held to.

### 5. What is dynamic

Event sources, futures and elastic deadlines (FDR 0020/0022) are runtime
behaviour that a static document cannot enumerate step by step. Options:

- **A. The dialect is an envelope of permitted dynamics.** It states
  capabilities and ceilings (`futures = true`, `event_sources = ["wake"]`,
  `ceiling <= 10m`); the run behaves dynamically inside it; the ledger and
  transcript are the trace. Static to check, dynamic to run.
- **B. The dialect is a state machine** including events and timeouts.
  Verifiable, but heavy, and every new dynamic feature changes the grammar.
- **C. Exclude dynamics** from dialects; they stay brief fields.

Recommendation: **A**. It matches how the brief already treats `limits` and keeps
the grammar closed while the runtime evolves behind flags.

### 6. Migration: no behaviour change

Today's brief is already a sentence of a closed, versioned (`schema = 1`),
unknown-key-rejecting vocabulary. Define it as the **implicit dialect
`brief/1`**, with its own digest, applied when no dialect is given:

- `juggler spawn --brief B --task T` (no flag): validated under `brief/1`
  exactly as now; every existing brief, ledger and digest is byte-identical.
- `juggler spawn --brief B --dialect D --dialect-digest sha256:...`: the
  document D must match the digest, B must satisfy D, otherwise the spawn is
  refused before anything is minted (like the narrowing check). The digest is
  recorded. (`spawn` has **no** such flag today; this is the answer to the
  question "would it take a dialect digest": it would, as an optional flag.)
- `BriefDigest` (today the sha256 of template and task, the idempotency key
  within a run) is unchanged; the dialect digest is a separate field.

## Shared core (the contract)

> This section is duplicated verbatim in juggler FDR 0023 and in roustabout's
> `docs/grammar-dialects.md`. **Source of truth for the shared core: until
> juggler's shared grammar module exists, the text of this section; once it
> exists (it is created in juggler when the turns dialect has a parser), that
> module, and both repos defer to it. roustabout's `internal/0/graph`
> (`grammar.go`) is the implementation it is drawn from.** Dialect-specific
> content is each repo's own.

1. **Pin by digest.** The host supplies the grammar document and pins it by its
   sha256 (`sha256:<hex>`). A document never carries the grammar that
   validates it.
2. **Versioned, closed.** A grammar document has a `grammar` name, a `version`,
   and a closed vocabulary; the loader refuses unknown fields and any version it
   does not know.
3. **Request type.** A grammar may name a request type that selects the
   sentence parser (`issue-filer/1`, `attest/1` in roustabout; `brief/1`,
   `script/1` proposed in juggler); absent means the dialect's default, so a
   grammar written before the field keeps its digest.
4. **Grammar right.** The ambient right a run of a grammar needs is named from
   the pinned digest, `<owner>:grammar:<digest>` (roustabout's is
   `roustabout:grammar:<digest>`), derived by the host and never supplied by a
   document.

Not shared: roustabout's node-kind table, outcomes, edges, result steps and
walker; juggler's turn vocabulary, tool offering, limits and stop conditions.

## Runtime records (what a dialect-validated document produces)

Per the ruling these are not grammar-validated; they are the stable shapes the
two layers exchange. The mapping, from the code:

| Record | Shape | Produced by | Notes |
|---|---|---|---|
| **Turn** | `{id, sender, parent, signature?, at, body, provider?}` | loop | `body` is a tagged union: `text`, `tool_call`, `tool_result`, `terminal` |
| **Event turn** | a `text` turn whose `sender` is the event source (FDR 0020, proposed) | loop | data, not instruction; not built |
| **Future handle** | a `tool_result` whose content is `{"state":"waiting","future":"f-N",...}` (FDR 0022) | loop | per tool call, not per run |
| **Ledger** | `{schema, calls[], end, cannot_complete, steps, elapsed_ms, notes?, provider?}` | loop | the evaluator's input; `calls[]` = `{tool, kind, ok, uris, error?, args_digest, future?}` |
| **ProviderMeta** | `{name, attempts, prompt_tokens, completion_tokens, cost_usd}` | provider harness (FDR 0021) | on the first turn of a reply and totalled in `Ledger.provider` |
| **Terminal** | `{reason, result?, result_error?}` | loop | `result`/`result_error` only when the brief opts in (`[result] format = "json"`) and the run ended `end_turn`; mirrored in `ledger.end` |

These are additive optional fields on a schema-1 wire shape (ledger and turn
fields the operator accepted for `provider`; `future` follows the same rule).

## What roustabout asked of turns

- *The terminal body of a run as a structured result.* Built (*Open questions*,
  4): an optional `result` on the terminal body and `end.result` in the ledger,
  opt-in by the brief's `[result] format = "json"`. roustabout no longer has to
  parse findings out of the `message` string.
- *Per-call ledger fields (tool, kind, ok, uris).* Exist and are stable
  (`LedgerCall`, plus optional `future`, `args_digest`).
- *A stable handle or digest for a run's turn log.* FDR 0022 handles are
  **per tool call**, not per run, so they are not that. A `transcript_digest`
  (sha256 of the canonical transcript) written into the run record at finish
  would be; whether the transcript is stored where a digest is cheap to take is
  unverified (*Open questions*, 5).

## Examples

A host pins a dialect that allows read tools first, then one write, with
futures but no events:

    # loop-dialect/1 (illustrative; names are proposals)
    dialect  = "issue-filing"
    version  = 1
    futures  = true
    event_sources = []
    ceilings = { steps = 12, wall_clock = "5m", call_wait = "60s" }
    [[step]]  # steps 1..3
    offer = ["smith_list_repos", "smith_list_issues"]
    [[step]]  # remaining steps
    offer = ["smith_create_issue"]

    juggler spawn --brief issue.toml --task - \
      --dialect issue-filing.toml --dialect-digest sha256:9f2c...

The brief's `tools` must include every offered name; a brief that lists a tool
no step offers is allowed (the dialect only narrows), one whose `limits` exceed
the ceilings is refused.

## Limitations

- Design only; every name here is a proposal. Nothing in juggler changes until
  the operator rules on *Open questions*, 1.
- A dialect describes the permitted shape of a run, not the policy of who may
  pin it (§4.3) and not the agents or graph around it (roustabout's lane).
- Per-step offering is the only new run-time capability; everything else is a
  name for existing behaviour.
- The static document cannot express every dynamic behaviour (§5, option A).

## Open questions

1. **RESOLVED (operator, 2026-11-12): option (a), a pinned schema for authored
   documents**, shared pinning and loader machinery, with the runtime records
   mapped (above) rather than grammar-validated. (Was: (a) that, (b) a wire
   schema both layers emit/read, (c) a type system both compile to.)
2. **RESOLVED (operator, 2026-11-12, Q22): the shared module lives in juggler**,
   spec first, created when the turns dialect has a parser; roustabout imports
   it (§3). The source-of-truth line in the shared-core section changed
   accordingly and is mirrored byte for byte in roustabout's note.
3. **Operator / spinclass: who holds the right to pin a dialect?** juggler can
   only check digest equality and record it. Owner: FDR 0032's principal and
   handle machinery; ajour is not an enforcer today.
4. **ACCEPTED and BUILT (operator, 2026-11-12, Q23): structured terminal
   `result`**, an additive optional field on the terminal body, schema stays 1.
   Shape: brief `[result] format = "json"` (the only format); on an `end_turn`
   run the final model message must be exactly one JSON value (leading and
   trailing whitespace allowed, nothing else, at most 64 KiB), carried compacted
   as `terminal.result` on the terminal turn and as `end.result` in the ledger;
   otherwise `result_error` (a sentence) on both and no `result`. A run that did
   not opt in, or ended another way, carries neither field; existing
   transcripts and ledgers serialise unchanged. The code was written before its
   tests (the ruling asked for tests first).
5. **ACCEPTED (operator, 2026-11-12, Q23): a run transcript digest**, additive,
   schema stays 1. Being built.
6. **DEFERRED (operator, 2026-11-12, Q24): per-step tool offering is NOT built
   yet**; the dialect (the pinned, versioned document and its envelope) comes
   first. Recorded here as deferred; §2 keeps it as the one new capability.
7. **Numbering.** juggler's FDR 0023 and roustabout's file carry the shared slug
   `grammar-dialects`; others' records are not renumbered.

## More Information

- roustabout `docs/grammar-dialects.md` (the other half, being written),
  `docs/generalizing-the-graph.md`, `docs/attestation-graph.md`,
  `internal/0/graph/grammar.go`: read-only sources for §3.
- circus FDR-0043 (roustabout, decisions 18 to 22); spinclass FDR 0032
  (principals, handles).
- juggler FDR 0019 (brief §3, tool plane §5), 0020 (event sources), 0021
  (provider harness, `ScriptTemplate`), 0022 (futures).
- Code anchors: `internal/alfa/jugglerbrief/brief.go` (closed schema, unknown
  keys rejected), `internal/charlie/jugglerrun/spawn.go` (`BriefDigest`),
  `internal/alfa/jugglerloop/turn.go` (`Body`), `ledger.go` (`EndReason`).

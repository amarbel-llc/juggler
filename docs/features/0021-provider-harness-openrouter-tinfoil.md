---
status: proposed
date: 2026-11-12
promotion-criteria: >
  experimental once the OpenRouter provider drives one `juggler run` against
  a recorded OpenRouter transcript (httptest replay, no live call) with retry,
  usage accounting and a provider-routing header exercised, and the Tinfoil
  provider refuses to send a prompt when its attestation check fails.
---

# Provider harness — OpenRouter and Tinfoil behind the agent loop

> **Status of the code.** Design plus compile-only stubs
> (`internal/*/jugglerprovider`). Every stub returns `ErrNotImplemented`; no
> network call is made and no secret is read. The operator-approved scope
> (relayed by the coordinator, 2026-11-12) is: OpenRouter and Tinfoil as
> first-class providers for `juggler run`, plus a provider-agnostic harness
> layer; the interactive/session harness stays FDR 0020's. Attestation
> verification is a stub interface only.

## Problem Statement

Both providers already work today as `style = "openai-compat"` remote-model
entries with a `token_file` (`internal/0/juggler/remotemodels.go`), because
both speak OpenAI chat completions. What that gives is a bare codec and
`RoundTrip`: one HTTP request, any non-2xx or transport error ends the run as
`model_error`, there is no retry or backoff on 429/5xx, no usage or cost
accounting, no provider-specific request fields (OpenRouter's routing and
attribution headers), and no way to refuse a provider whose trust condition is
not met (Tinfoil's attested enclave). A "proper" harness is the layer between
the loop and the codec that supplies those, identically for every provider,
so a new provider is a small adapter and a run's behaviour under provider
failure is specified rather than accidental.

## Interface

### 1. A `Provider` adapter around the existing codec

The loop keeps its `Codec` seam (`EncodeRequest`/`DecodeResponse`). A new
package, `jugglerprovider`, adds a `Provider` that wraps a codec and the
resolved model, and the loop calls the provider's round trip instead of
`jugglerloop.RoundTrip`:

    type Provider interface {
        Name() string
        // Verify runs once per run, before the first prompt is sent. A
        // failure ends the run as model_error and nothing is transmitted.
        Verify(ctx context.Context) error
        // Prepare may add provider-specific headers or body fields to an
        // encoded request (attribution headers, routing preferences).
        Prepare(req *http.Request) error
        // Usage extracts token counts and cost from a response body, when the
        // provider reports them.
        Usage(body []byte) (Usage, bool)
        // Retry classifies a failed round trip: retry after d, or stop.
        Retry(attempt int, status int, body []byte, err error) (after time.Duration, again bool)
    }

A provider is selected by a new optional `provider` key on a remote-model
entry (`"openrouter"`, `"tinfoil"`; absent means today's plain codec, so no
existing registry changes behaviour). `style` keeps naming the wire shape.

### 2. OpenRouter

`provider = "openrouter"`, `style = "openai-compat"`,
`url = "https://openrouter.ai/api/v1"`, `model = "<vendor>/<model>"`.

- `Prepare` adds the optional attribution headers (`HTTP-Referer`, `X-Title`)
  from `[model.provider]` keys, and an optional routing preference (provider
  order / fallbacks) as the documented request-body field. All opt-in; absent
  keys add nothing.
- `Usage` reads the response's `usage` object (and cost, where reported) into
  the ledger.
- `Retry` backs off on 429 and 5xx with jitter, honours `Retry-After`, and
  gives up after a bound (below); 4xx other than 429 is never retried.

### 3. Tinfoil

`provider = "tinfoil"`, `style = "openai-compat"`, `url` the enclave endpoint.

- `Verify` is the attestation seam: fetch the enclave's attestation, check it
  against a pinned expected measurement from the entry
  (`[model.provider] measurement = "<digest>"`), and bind the check to the
  TLS session the prompt will use, so a passing check on one connection does
  not authorise sending on another. Failing, or an unpinned measurement,
  refuses the run before any prompt leaves (fail closed). **Only the interface
  is defined here**; the verification itself, and how binding to the TLS key
  is done, are open (see *Open questions*).
- No `Prepare` extras; `Usage` and `Retry` as the OpenAI-compatible default.

### 4. Harness policy layer (provider-agnostic)

`jugglerprovider.Do` runs one round trip under the provider: `Verify` once,
then `Prepare`, send, and on failure `Retry` until the per-step budget (below)
or the run's wall clock ends it. It returns the reply, a `Usage`, and an
attempt count that the loop records in the ledger and in the turn
(`Turn.Provider`: name, attempts, usage), so a post-hoc reader sees what a
step cost without parsing provider bodies. A retry never re-issues a *tool*
call; it repeats the model request only, and a request that reached the
provider but whose response was lost is retried as a new, idempotent-by-
construction model request (the transcript is unchanged until a reply is
accepted).

### 5. Scripted templates and turns

Two things the coordinator put in scope:

- **Script templates.** A `ScriptTemplate` is a named, parameterised list of
  canned model replies (`{text | tool_call}` per step, `{{.Task}}`-style
  substitution from the brief) that a `ScriptedCodec` plays back with no
  network. It is the deterministic test double for the harness (retry, usage
  and verification paths are exercised against it and against `httptest`
  replays) and the same shape the FDR 0019 "script as the bottom fallback"
  uses. The format is TOML beside the brief; the type is stubbed here.
- **Turns.** `Turn` gains an optional `Provider *ProviderMeta` (name, attempts,
  usage). It is carried through `OnTurn`, the MUC transcript and the journal
  like the rest of a turn, never sent to a provider, and absent for non-model
  turns.

## Examples

Registry entries (illustrative; no secret inline, key by file reference):

    [[model]]
    name = "or-sonnet"
    style = "openai-compat"
    provider = "openrouter"
    url = "https://openrouter.ai/api/v1"
    model = "anthropic/claude-sonnet"
    token_file = "~/.config/juggler/openrouter.key"
    [model.provider]
    title = "juggler"

    [[model]]
    name = "tf-llama"
    style = "openai-compat"
    provider = "tinfoil"
    url = "https://<enclave-host>/v1"
    model = "<model id>"
    token_file = "~/.config/juggler/tinfoil.key"
    [model.provider]
    measurement = "<pinned attestation digest>"

A run on `tf-llama` whose attestation check fails ends `model_error` with the
verification error in the ledger and zero prompts transmitted; on `or-sonnet` a
429 with `Retry-After: 2` is retried once after 2 s and the step's ledger entry
records `attempts: 2` and the reported token usage.

## Limitations

- Design and stubs only. Provider names, the `provider` registry key and the
  `Provider` method set are proposals; the stubs return `ErrNotImplemented`.
- No streaming. The loop is request/response today and stays so here.
- Cost is reported, not enforced: no budget cap is proposed (a lever, below).
- Attestation verification is not designed in detail: which attestation format,
  how it is bound to the TLS channel and how the measurement is obtained and
  rotated are open, and the first implementation MUST NOT ship a fail-open
  "verify later" mode.
- Only chat completions. No embeddings, images or batch endpoints.

## Tuning Levers

| Lever | Current | Rationale | Change signal |
|---|---|---|---|
| retry attempts per step | undecided (suggest 3) | rides out brief 429/5xx | steps regularly exhaust it |
| backoff base / cap | undecided (suggest 1 s / 20 s) | well under the 120 s default wall clock | runs end on wall clock mid-retry |
| honour `Retry-After` | yes, capped by the above | provider knows its own limit | provider values exceed the cap routinely |
| per-run cost cap | none | no policy proposed | an unattended run overspends |

## Open questions

For the operator, or the named owner; none blocks the stubs.

1. **Tinfoil attestation:** which attestation evidence and format, how it binds
   to the TLS session (certificate or key pinning versus a signed report
   carrying the key), how the expected measurement is obtained and rotated, and
   whether verification lives in juggler or a shared library. Owner: operator,
   with Tinfoil's published verification procedure as the source.
2. **OpenRouter privacy/routing:** which routing preferences a juggler brief
   may set (for example restricting to providers that do not retain prompts)
   and whether that is a brief-level or registry-level setting. Owner: operator.
3. **Where the credentials live** beyond a `token_file` path (a piggy-backed
   store is a deploy question): out of scope here; the entry only references a
   file.
4. **Numbering.** This is juggler's FDR 0021; clown also has an FDR 0021 that
   FDR 0019 cites, so a bare "FDR 0021" is ambiguous across repos.

## More Information

- FDR 0019 — the agent substrate: §8 internal representation and codecs, §10
  daemon-free remote models, the script fallback.
- FDR 0020 — loop event sources and async jobs (the session/interactive
  harness stays there).
- Code anchors: `internal/0/juggler/remotemodels.go` (`RemoteModel`, `token_file`),
  `internal/alfa/jugglerloop/codec.go` (`Codec`, `CodecFor`, `RoundTrip`),
  `loop.go` (`run.loop` calls `RoundTrip`).

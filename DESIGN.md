# functy Design Rationale

This document records *why* functy exists and *why* it is built the way it is.
It is a design record, not a reference; for the language itself see
`doc/language.md`.

## Vision

functy is a small imperative language whose values are `cty` values and whose
expressions are HCL. It exists to give software ("hosts") already built on HCL
a real procedural escape hatch — one that feels like the surrounding configuration
rather than a foreign body bolted onto it.

The motivating problem is what happens when you outgrow `ext/userfunc`-style
function definitions. HCL's `function` blocks (and hand-built equivalents) work
well for a single expression, but the moment you need an intermediate binding,
a branch, a loop, or a side-effect-only statement, you are forced to borrow
HCL's block/attribute grammar for a job it was never meant to do:

- A variable cannot be reassigned within the same block.
- There is no syntax for evaluating an expression purely for its side effects —
  every statement must be an assignment or a block, forcing `_1 = …`, `_2 = …`
  discard names.
- Conditions must be quoted strings (`if "x > 0"`), with no `${}` templating.
- HCL's brace/newline rules leak through (`} else {` on one line is a syntax
  error).
- Parameter and return declarations are verbose.

functy replaces that borrowed grammar with a small, purpose-built one in a
familiar Go/C style, while **reusing HCL's expression engine wholesale**. Every
expression in a `.cty` file — operators, function calls, templates, indexing,
conditionals — is parsed by `hclsyntax.ParseExpression` and evaluated lazily
through the same `*hcl.EvalContext` the host already uses. So `cond()`, `try()`,
short-circuit evaluation, `${}` templates, and every built-in function behave
identically inside and outside functy.

The guiding principle is borrowed from go-cty's own description:

> *"One could think of cty as being the reflection API for a language that
> doesn't exist, or that doesn't exist yet."*

**functy aims to be that language.** Its values are cty values, its types are
cty types (declared with the same `typeexpr` grammar Terraform uses), and the
result of compiling a `.cty` file is a collection of `cty` functions added to an
eval context. functy is a thin, honest imperative skin over cty + HCL
expressions — no more, no less. The niche it fills is exactly that: an
imperative layer for a host that is already on HCL and needs more than a
single-expression function definition can express.

## Non-goals

These are deliberately out of scope. Some are deferred; some are structural.

- **First-class function values / closures stored in variables.** cty functions
  live in `EvalContext.Functions`, not `Variables`, so functions are not
  ordinary values. (Deferred.)
- **Go-style multi-value returns.** A `cty` function returns exactly one value;
  multiple results are expressed by returning an object or tuple. (A
  statement-level `value, err = expr` error-capture form — sugar over
  `try`/`catch`, not a function return — is planned future work.)
- **In-place mutation of collection elements** (`a[i] = x`). cty values are
  immutable; assignment targets are plain variable names only.
- **Get/set/delete and declaration shorthands** (`->`/`::`/`:=`). Designed but
  deferred.
- **New top-level host block types.** A `.cty` file declares functions only; it
  does not extend the host's configuration grammar.

## Why build a language instead of embedding one

Before committing to a purpose-built language, the obvious alternative was to
embed an existing Go-hostable scripting language — Starlark (`starlark-go`),
Tengo, Lua (`gopher-lua` / `go-lua`), CEL (`cel-go`), Expr, goja (JS), or yaegi
(Go). This section records why building won, and the conditions under which
embedding would be the better call.

### The real cost is the value boundary, not the language

Every candidate is a competent language with a competent runtime; that is not
where the decision lives. The host's entire data model is `cty.Value` —
messages, transforms, function arguments and returns are all cty. Embedding any
other language means marshalling `cty` ⇄ that language's value type at **every
call boundary**, and that boundary is the permanent, recurring cost.

Specific costs of a non-cty-native runtime:

1. **Capsules and rich objects don't survive the trip.** Host handles like a
   bus or a client are cty capsules (Go pointers); rich-objects expose attribute
   access like `u.scheme` or `b.content_type` *because* cty carries them. In a
   foreign runtime these become opaque handles, and attribute access works only
   via a hand-written wrapper-and-binding layer maintained **per capsule and rich
   type, forever** — reimplementing the machinery cty already provides.
2. **Two expression languages in one configuration.** A user writes
   `cond(x > 0, a, b)`, `jq(".payload")`, `"hello ${name}"` in the host's HCL.
   Dropping into a foreign script changes the operators, truthiness, null
   semantics, string interpolation, **and the entire available function set**.
   functy keeps the *expression* sublanguage byte-for-byte identical to the
   host's; only the statement skeleton is new.
3. **A parallel binding surface that drifts.** The host's value-add is its
   library of config-callable functions. In cty they are *already* callable from
   functy. A foreign runtime needs a shim for each, with its own calling
   convention, and that surface drifts out of sync over time.
4. **Type/number fidelity loss.** cty numbers are `big.Float`; cty distinguishes
   list/set/tuple, object/map, and typed-null — distinctions that flatten on the
   way out and cannot be reconstructed on the way back. Templates (`${}`) and the
   lazy `cond()`/`try()` semantics are also lost.

### What embedding would genuinely buy

These are real advantages, not strawmen:

- **Maturity**: battle-tested, fuzzed runtimes; no parser/interpreter edge cases,
  no step/time-limit work of one's own to get right.
- **Performance**: Tengo and Lua are bytecode VMs and would far outperform a
  tree-walker on tight loops.
- **Features for free**: closures, comprehensions, modules, good error messages,
  sandboxing.
- **Familiarity and documentation**: Starlark (a Python subset, from Bazel) and
  Lua are widely known; functy is novel.

And how each candidate reads:

| Option | Fit | Verdict |
| --- | --- | --- |
| **Starlark** (`starlark-go`) | Designed *as* a config language; Google-maintained; deterministic; Python-ish; real closures | Strongest "buy." But its determinism/hermeticity is a non-goal — side effects are wanted — so a key advantage is wasted, and the full cty boundary tax still applies. |
| **Tengo** | Fast bytecode VM, small | Best raw performance, but smaller ecosystem and its own quirks; same boundary problem. |
| **Lua** (`gopher-lua`/`go-lua`) | Ubiquitous embedding language, fast | Familiar, but 1-based indexing, nil/false/metatable quirks, not a config language; same boundary. |
| **CEL** / **Expr** | Typed, embeddable | **Out** — expression-only; no statements, loops, or locals, so they do not solve "procedures." |
| **goja** (JS) / **yaegi** (Go) | Maximal familiarity / real Go | Heavy runtimes, larger attack surface; same boundary; overkill for a glue escape-hatch. |

### The decisive asymmetry: incremental vs. additive

The build-vs-buy ledger flips once existing investment is counted. **functy is
*incremental*; embedding is *additive*.**

- A host on HCL already owns the expression engine and the scope/lazy-eval
  machinery. functy reuses both; its net-new code is a statement-level
  lexer/parser plus a handful of IR nodes (typed `var`, `try`/`defer`, richer
  `for`).
- Embedding adds a **whole second runtime** *and* a **permanent marshalling and
  binding layer** that never goes away.

So "buy" is not free, and "build" here is not a from-scratch language; the net
*maintained surface* is plausibly **smaller** with functy.

### What the decision turns on, and the recommendation

This is ultimately a product-strategy question. The procedure layer is
positioned as a **deliberately-limited glue escape-hatch**: if you find yourself
writing large programs in it, that is a signal to reach for a real programming
environment instead. Under that philosophy, programming *power* (closures,
modules, comprehensions) is not the priority — **seamlessness with the
configuration is** — which points at functy. If instead the layer were meant to
host **substantial programs**, a mature language with a debugger, ecosystem, and
docs would be the better long-term bet, and the boundary tax would be worth
paying.

**Decision:** build functy. The thing that makes an HCL-based host coherent is
its cty/HCL surface; a non-cty-native procedure language is a foreign body whose
seam shows in nearly every example (host handles, attribute access, `${}`
templates).

**Hedge (non-exclusive):** an embedded language is a natural fit for a *plugin*
later — a `starlark`/`lua` plugin registering a function source, for users who
want full programming power and will accept the boundary. That keeps functy
small and seamless while leaving the door open, rather than betting the native
layer on a foreign runtime now.

**Failure mode to watch:** if functy's tree-walker performance or parser
fragility (the expression-boundary scanning is the riskiest piece) becomes a
problem, that is the signal the escape-hatch is being used as a programming
environment — at which point a plugin-embedded mature language, not a larger
functy, is the answer.

## Open questions

- **Error value shape.** The `error` type is an object carrying at least a
  string `message`. Open: should it also carry a source range or a structured
  `code`? The type is an open type, so this is extensible later without breaking
  existing programs.
- **Set iteration index.** `for i, v in set` currently exposes a stable-order
  index, with the caveat that sets are unordered. Open: keep the index, or
  require `for v in set` only?

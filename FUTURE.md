# functy — Future Work

This file tracks designed-but-unimplemented enhancements to the functy language,
library, and CLI. They are recorded so they can be added later without a surprising
redesign — several extend the parser's `Result` / `FuncDecl` / `Decl` structs with
new fields, which is precisely why those are extensible structs rather than bare
maps. Implemented features are documented in `doc/`; nothing in this file is a
commitment, only a record of intent and rationale.

## Standard library

functy is designed to eventually ship its **own** standard library: a
`map[string]function.Function` of language-level builtins exposed by the package
(e.g. `functy.Stdlib()`), **distinct** from cty's own `cty/function/stdlib` and from
any host-specific functions. A host would assemble its eval context from layers: the
cty stdlib (base math / string / collection / encoding primitives), the functy
stdlib (host-agnostic, dependency-free builtins the language leans on), and its own
runtime-coupled host functions (`send`, `log_*`, `get`/`set`/`delete`, HTTP, …).

The proposed default `functy.Stdlib()` is pure, dependency-free, and pulls in no
external cty modules:

- **`cond`** — lazy multi-branch conditional (`c1, r1, …, else`).
- **`switch`** — lazy value dispatch over `(match, result)` pairs.
- **`typeof`** — friendly type name.
- **`error`** — raise an error (pairs with the built-in `error` type and `throw`).
- **`assert`** — diagnostic-rich check (see *Language* below).
- **`eval`** — evaluate a lazy / by-expression parameter (see *Functions* below).
- **`tostring` / `length` dispatch** — rich-object-aware versions that try a
  `Stringable` / `Lengthable` interface and fall back to the cty stdlib. Safe to
  include by default because they are a backward-compatible superset, and
  dependency-free: Go interfaces are structural, so functy defines its own tiny
  `Stringable` (`ToString() (string, error)`) / `Lengthable` (`Length() (int, error)`)
  and any capsule whose Go value already has those methods satisfies them
  automatically — no import of a rich-object package.

`cond` / `switch` build on `customdecode` lazy evaluation with a **single-eval**
guarantee — the same mechanism functy adopts for `assert` / `eval` — so the stdlib is
the canonical home for the single-eval control-flow builtins. These names don't
collide with any standard HCL/cty function, so default inclusion is safe.

**Held back from the default set (opt-in).** Builtins whose name collides with a
common HCL/cty function but diverges semantically are offered via a separate accessor
(e.g. `functy.Try()` / a `StdlibExtras()` set), not the default `Stdlib()`:

- **`try`** — functy's lazy, single-eval "first non-erroring expression" form. Its
  name collides with HCL `ext/tryfunc`'s `try`, which has different (eager,
  type-guaranteeing) semantics, so it is not a backward-compatible drop-in;
  auto-including it would silently change behavior for a host already using tryfunc's
  `try`. Same treatment for any future builtin that matches a common HCL/cty name but
  diverges (e.g. `can`).

The stdlib is opt-in overall — a host merges it in exactly as it does the cty stdlib,
keeping full control of the namespace. The residual risk for the novel names
(`error`, `eval`, `assert`, …) is merely that a host might already define a function
of the same name; that surfaces as a merge-time **duplicate** (an explicit collision
error), not a silent semantic swap.

## Language — expressions & sugar

- **`:=` declaration shorthand.** `x := expr` as pure sugar for `var x = expr`
  (untyped). Omitted to keep one obvious way to declare; trivial to add. Worth
  reconsidering partly for error-capture ergonomics: `v, err := expr` would give
  combined declare-and-capture (see *Error handling*) for free, mirroring Go's
  `v, err := f()`.
- **`fallthrough`** in `switch`.
- **Labeled `break` / `continue`** for nested loops.
- **Pure-expression-statement warning.** Warn when an expression statement is
  obviously side-effect-free (no function call) and its value is discarded.
- **`assert(cond, message?)` built-in (diagnostic-rich).** A check that raises an
  `error` (catchable, composes with `val, err =`) when `cond` is false. Implemented
  as a built-in **function**, not a statement, using HCL's
  `customdecode.ExpressionClosureType` — the same mechanism `try()` / `can()` use — so
  it receives `cond` *unevaluated* as an `hcl.Expression` together with the
  `EvalContext`. That gives it `cond`'s exact source range (`.Range()`) and AST, so it
  evaluates the condition itself and, on failure, emits a diagnostic whose `Subject` /
  `Expression` / `EvalContext` make the host's standard diagnostic writer underline the
  failed condition in-source — optionally showing operand values pytest-style by
  walking the AST. No new grammar, and it works because functy always evaluates within
  an HCL `EvalContext`. A dedicated `assert` *statement* was considered and rejected:
  the function gets the same source location and introspection, so the statement would
  add only marginal value.

## Functions

- **Doc-comment metadata.** A doc comment attached to a `func` (e.g. `///` or a
  contiguous leading `//` / `#` block) captured into `FuncDecl` (description, and
  ideally per-parameter docs). Powers generated docs and LSP hovers, and lets a host
  surface them — e.g. backing an MCP tool/prompt or HTTP handler with a functy
  function and pulling its description and parameter docs straight from source. Adds
  fields to `Result` / `FuncDecl`.
- **Function visibility (exported vs. internal helpers).** Today every top-level
  `func` is registered globally. A convention or keyword (Go-style lowercase, an
  `export` / `pub` keyword, or `_`-prefix) would let a file define local helpers
  without polluting the host's function namespace: compilation registers only the
  exported set; `Result` can expose both. Increasingly useful as files grow.
- **Function-local helper functions.** Nested `func` declared inside a body
  (file-private scope); related to closures below.
- **First-class function values / closures.** Storing / passing functions as values.
  Requires representing cty functions as storable values (they currently live in
  `EvalContext.Functions`, not `Variables`).
- **Lazy / by-expression parameters (author-defined `try`-like functions).** Today
  functy functions take eagerly-evaluated cty values, so authors cannot write their
  own short-circuiting / error-catching / retry / timing wrappers. The same
  `customdecode` mechanism behind `try` / `can` / `assert` can be exposed to authors: a
  parameter marked **lazy** (proposed spelling: a pseudo-type `expr`, e.g.
  `func f(cond: expr)`, or a `lazy` modifier) compiles to a cty parameter of type
  `customdecode.ExpressionClosureType`. When called from any HCL context that param
  receives the **unevaluated argument expression plus the caller's eval context**
  (`*customdecode.ExpressionClosure`), wrapped as a functy "expression handle". The
  body evaluates it explicitly with an `eval(x)` builtin, which composes with the
  `error` type: `v, err = eval(x)`.

  ```functy
  func coalesce(primary: expr, fallback) -> any {
      var v
      var err: error
      v, err = eval(primary)                  // run caller's expr now; capture failure
      return cond(err == null && v != null, v, fallback)
  }
  // call:  coalesce(risky(x), 0)             // risky() not evaluated unless coalesce evals it
  ```

  Properties / caveats: the closure captures the **caller's** scope (so `risky(x)` sees
  the caller's `x` — the point of laziness); re-evaluating via `eval` repeats side
  effects (enables `retry`, the author's responsibility, like call-by-name); it works
  only when the call goes **through HCL evaluation** (always true in-source, but a
  lazy-param function invoked as a raw `cty.Function` outside an `EvalContext` — e.g.
  `functy run -f f -- args` — cannot receive expressions, so the CLI/host must forbid or
  special-case that). Implementation: `FuncDecl` carries a per-param `lazy` flag;
  function building sets those params to `ExpressionClosureType`; the handle is a cty
  capsule wrapping the closure; `eval()` unwraps and runs it. Like `-> null`,
  `expr` / `lazy` needs front-end special-casing (it is not a typeexpr type), and counts
  as an explicit annotation under `RequireParamTypes`.
- **Dynamic expressions from strings — gated, OFF by default.** A builtin (working
  name `compile` / `parse_expr`) that parses a *string* into an expression handle at
  runtime via `hclsyntax.ParseExpression`, yielding the **same `expr` handle** as a lazy
  parameter so it composes with `eval`: `eval(compile(s))`. This lets a program build
  and evaluate expressions dynamically — but it is **arbitrary-expression injection**
  (the string can call any function in the eval context), i.e. an `eval()` of untrusted
  code. Therefore:
  - **Off by default**, enabled only by an explicit **host capability** (a builder
    option, e.g. `AllowDynamicExpr`) — a capability, so host-controlled and **not**
    grantable by an in-source pragma;
  - even when enabled, the host should evaluate dynamically-built expressions in a
    **restricted context** (a whitelist of pure/safe functions — excluding side-effecting
    ones like `send` / `http` / file) when the string may be untrusted, and/or pass
    explicit bindings rather than the full ambient scope; pairs with execution limits,
    since dynamic eval + unbounded loops compound;
  - a parse failure returns an `error` (catchable via `try` / `val, err =`).

  Open: the name (`compile` / `parse_expr` / one-shot `eval_string`) and the
  context-scoping (current scope vs. host-restricted vs. explicit bindings).
- **Default-param expressions referencing earlier params.** Relax the minimal-context
  rule so a later parameter's default may reference earlier parameters (Python-style),
  evaluated left to right.
- *(Not feasible — recorded so it is not re-proposed:)* **keyword / named call
  arguments** (`greet(name = "x")`). Call sites are HCL expressions and HCL function
  calls are **positional-only**; supporting named arguments would require forking HCL's
  expression parser, defeating the reuse-HCL premise.

## Top-level constructs

- **Module / import / namespacing.** e.g. `import "lib.cty" as lib` exposing
  `lib::foo()` via HCL's existing `::` namespaced-call token. The cleanest path to
  multi-file organization beyond the flat all-merged model; larger design, but recorded
  as the intended direction. (Type aliases already provide project-scoped *sharing*;
  this is for *namespacing / organization*.)

## Error handling

- **Combined declare-and-capture for `value, err`.** The `value, err = expr`
  capture form (now implemented — see `doc/language.md`, "Error capture") requires
  both targets pre-declared, costing two `var` lines in the common case. A future
  convenience would declare *and* capture in one line — either a multi-declaration
  `var v, err = expr` (needs a type-placement rule like `var v: T, err: error = expr`)
  or, more naturally, Go-style `v, err := expr` if `:=` is adopted. Opt-in sugar over
  the existing desugaring; no new semantics.
- **Typed / multiple `catch` clauses** (match on error shape / kind).
- **`defer` argument snapshotting.** Evaluate a deferred call's arguments at `defer`
  time while deferring the call itself, matching Go. Requires expression decomposition
  machinery beyond a plain `hcl.Expression`.

## Safety & execution

- **Execution limits (step / time budget).** functy permits unbounded `for` / `while`
  over a tree-walking interpreter; a host-configurable max-steps and/or wall-clock
  budget (via the builder or a run option) provides runaway-loop protection. Strong
  enough that it may warrant near-term, not deferred, treatment.
- **Sandbox / pure mode + memoization.** The host already controls which functions
  populate the eval context, so a "no side effects" mode is largely free; a `pure`
  marker on a function could additionally enable **memoization / caching** of its
  results for repeated identical calls.

## Tooling & ecosystem

The standalone `functy` binary already provides `run` and `check`. It exists for
development, testing, and experimentation — not for production use (a host application
links the library directly and supplies its own richer context). Planned additions:

- **`functy run -i [FILE …]`** (equivalently `functy repl`) — load all given files,
  **run the entry function if present, then start an interactive REPL** over the loaded
  context. Modeled on Python's `python -i script.py`: the entry function (`main` by
  default, or `--func NAME`) is executed first *if it exists*; a missing **default**
  `main` is **silently skipped** — so you need not declare `main` just to explore code
  in a REPL — whereas an explicitly named `--func` that is absent is still an error. The
  REPL is the same HCL expression engine used everywhere; a host with a richer context
  (its own functions and ambients) can layer that on top. Standalone it is necessarily
  limited — no app-specific functions or ambients — but still handy for exploring the
  stdlib, language semantics, and loaded functions.
- **`functy fmt [FILE …]`** — canonical formatter. For a language targeting the
  HCL/Terraform audience (who expect `terraform fmt` / `gofmt`), this is close to
  adoption-critical. Must preserve directive comments.
- **LSP / editor support** — diagnostics, hovers (using doc-comment metadata),
  go-to-definition, completion.
- **Inline tests** — co-located `test "…" { … }` blocks with assertions for functy
  functions, aiding the "real language" maturity story (and runnable via the CLI).
- **Add-on package "functy-readiness" convention.** Sibling cty add-on packages
  (`bytes-cty-type`, `url-cty-funcs`, `rich-cty-types`, `time-cty-funcs`, …) should let
  a program that links *both* functy and the package register the package's type(s) in
  one line — **without** the package importing functy. This is possible because functy's
  registration entry points consume only go-cty vocabulary: `RegisterType(name, cty.Type)`
  and `RegisterOpenType(name, func(cty.Value) error)`. So a package becomes functy-ready
  by exporting, in go-cty terms only:
  - its `cty.Type` (most already do) — for identity registration (`RegisterType`), which
    **nests** (`list(bytes)`) but is closed/exact; and
  - for marker-capsule / open types, a predicate `Is<Thing>(cty.Value) error` (e.g.
    `rich-cty-types` adding `IsContextObject` wrapping `GetContextFromValue`) — for
    non-destructive open registration (`RegisterOpenType`), currently leaf-only.

  The host picks identity vs. open per type. Rejected alternatives: a **string** form
  (`"object({ _capsule = … })"`) is circular (the capsule type is a Go value, not
  nameable in a type-expr string until itself registered), needs a parse/error step, and
  discards compile-time checking — the `cty.Type` value already *is* the canonical
  artifact; a **shared registration struct/interface** would force a shared dependency
  (the very thing being avoided). If host wiring boilerplate ever warrants it, functy
  could ship opt-in `functy/contrib/<thing>` adapters that import both sides — keeping
  that dependency direction out of functy core.
- **Terraform / OpenTofu provider binding (speculative — low priority).** A third
  embedding target, distinct from a host like Vinculum: expose functy-authored functions
  to Terraform as provider-defined functions, callable as `provider::functy::<name>(...)`.
  Since Terraform 1.8 / OpenTofu 1.7 this is the *only* sanctioned way to add custom
  functions, delivered through terraform-plugin-framework's `function` package (the
  provider declares typed parameters + a typed return per function and implements `Run`).
  The binding is conceptually thin — a compiled functy function is already a `cty.Function`
  with a static return type, and the framework's `Run` bridges to cty over the wire — so
  the adapter mainly translates functy/cty types into framework type definitions and
  marshals args. functy's typed signatures + strict typing are what make this clean:
  Terraform *requires* declared parameter/return types, which functy can now guarantee.

  **Worth it? Mostly no — recorded for the analysis, not as a roadmap item.** The core
  value of functy as a scripting extension is the no-build edit→run loop (drop a `.cty`,
  it's live — what Vinculum and the standalone CLI give you). Terraform's provider model
  structurally forbids exactly that: static schema, registry-distributed binaries pinned
  via `.terraform.lock.hcl`. So the only *robust* integration (option 1 below) reintroduces
  the build-and-publish loop functy exists to eliminate, and the paths that keep the
  no-build feel (options 2–3) are the unsafe ones. The robust option negates the value;
  the value-preserving options aren't robust — an intrinsic tension, not a gap to engineer
  around. Once a build step is mandatory the marginal saving over writing the functions in
  Go shrinks to "bodies in functy vs. Go" (the annoying framework boilerplate is mechanical
  and code-generatable *without* functy), and off-the-shelf providers (corefunc, validatefx,
  the AWS/time/google functions) already cover the common cases.

  **The one framing where it's not a waste** is narrow and worth stating precisely: not
  "functy as a Terraform scripting extension" (Terraform kills that), but *functy as a
  portable function-authoring format that can also compile to a TF provider*. If a `.cty`
  function library already exists for other reasons (Vinculum, a shared library), a
  generator that *additionally* emits a Terraform provider from it is a cheap bonus —
  reuse, not authoring-for-Terraform. It is never a reason to adopt functy in the first
  place; if the library doesn't already exist, doing this just for Terraform isn't worth it.

  The design analysis below is kept because it is correct and self-contained, should the
  portability framing ever justify the work.

  Two constraints shape the design:
  - **Pure-only.** Terraform functions must be deterministic and side-effect-free
    (evaluatable during plan), so only functy's pure core + pure stdlib are in scope —
    `send` / `log_*` / store style ops are excluded. This maps exactly onto the pure-vs-
    host-coupled split in *Standard library* above; the three embeddings line up as
    Vinculum (full, incl. side effects) / standalone CLI (pure core) / Terraform (pure
    core re-exposed). The natural fit is exactly what existing provider functions do:
    pure data transforms — parse↔build pairs, encoding/formatting, validation, CIDR /
    semver / time helpers — things awkward to express in raw HCL.
  - **Static function schema vs. file-loaded functions (the crux).** Terraform fetches a
    provider's function list as *static schema*, before and independently of provider
    configuration — so a provider cannot be told "load `./funcs/*.cty` and expose
    whatever they define." Options, increasing in cleverness and decreasing in honesty:
    (1) **build-your-own-provider** — `.cty` compiled into a custom provider binary at
    build time (cleanest typing, heaviest ergonomics; the honest MVP); (2) **one generic
    dynamic function** — `provider::functy::eval("expr", {args})` returning a dynamic
    type (sidesteps the static set but discards static typing and is *more* verbose —
    rejected as defeating the point); (3) **scan-at-schema-time** — enumerate `*.cty` in
    the working dir when Terraform requests the schema (most "magic", but makes the
    schema depend on local files, against Terraform's stability assumptions).

  The call-site verbosity (`provider::functy::add(2, 3)`) is inherent and inescapable:
  Terraform namespaces provider functions deliberately and offers no aliasing or import,
  and functions are not first-class values (so `locals { add = provider::functy::add }`
  is impossible). It is, however, idiomatic Terraform. This item is gated on choosing an
  answer to the static-schema question; option (1) is the recommended starting point.

  **Motivating examples.** The killer demo is not `add(2, 3)` — it is the two categories
  that dominate real provider functions, both of which are tedious in Go and painful in
  raw HCL. First, the **parse↔build round-trip** (HashiCorp's own `aws::arn_parse` /
  `aws::arn_build` and `terraform::decode_tfvars` / `encode_tfvars` are exactly this):

  ```functy
  // "arn:partition:service:region:account:resource…" -> structured object
  func arn_parse(arn: string) -> object({
      partition = string, service = string, region = string,
      account_id = string, resource = string,
  }) {
      var p = split(":", arn)
      if length(p) < 6 { throw error("malformed ARN: ${arn}") }
      return {
          partition  = p[1],
          service    = p[2],
          region     = p[3],
          account_id = p[4],
          resource   = join(":", slice(p, 5, length(p))),   // resource may contain ':'
      }
  }

  func arn_build(partition: string, service: string, region: string,
                 account_id: string, resource: string) -> string {
      return "arn:${partition}:${service}:${region}:${account_id}:${resource}"
  }
  ```

  Second, **validation** — the single largest community category (the actively-maintained
  `validatefx` ships ~70 such functions; the archived `hashicorp/assert` had 36), almost
  all of them "check a string/number against a rule, return `bool`," which functy expresses
  directly with `try` / `catch`:

  ```functy
  func valid_json(s: string) -> bool {
      try { jsondecode(s); return true } catch e { return false }
  }
  ```

  Re-exposed, these are called `provider::functy::arn_parse(...)` /
  `provider::functy::valid_json(...)` — i.e. a `validatefx` or `corefunc` you author in
  `.cty` instead of maintaining a Go provider. All such functions are pure (no I/O, no
  state), so they sit cleanly inside the pure-only constraint.

## Type system

- **Nested *open* types in the type resolver.** Capsule named types now nest
  (`list(bus)`, `object({ b = bus })`); the remaining gap is nesting an **open
  predicate** type (`error`, a marker-capsule `ctx`). Because an open type has no
  concrete cty type and is heterogeneous, this needs a structural constraint tree that
  applies the predicate at each nested position (rather than cty `convert`), and a
  decision on whether open types may nest only in heterogeneous positions (tuple/object)
  rather than homogeneous collections (list/set/map) at all.
- **Per-function directives.** File-scope directive comments are already collected into
  `Result.Directives`. The remaining gap is per-function directives — attached to a
  function's preceding comment block — exposed as `FuncDecl.Directives`. This shares the
  same comment-to-declaration attachment machinery as the doc-comment metadata above and
  is expected to land alongside it.

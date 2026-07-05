# functy — Future Work

This file tracks designed-but-unimplemented enhancements to the functy language,
library, and CLI. They are recorded so they can be added later without a surprising
redesign — several extend the parser's `Result` / `FuncDecl` / `Decl` structs with
new fields, which is precisely why those are extensible structs rather than bare
maps. Implemented features are documented in `doc/`; nothing in this file is a
commitment, only a record of intent and rationale.

An item marked ***shipped*** is kept only for the work that still remains on it (a
one-line status pointing to `doc/` for the rest) — so the file stays a map of what is
left to do, not a changelog of what is done.

## Standard library

functy ships its **own** standard library — `functy.Stdlib()` (`typeof`, `typekind`,
`cond`, `switch`, `error`, `assert`) and the opt-in `functy.StdlibExtras()` (`try`,
`can`) — dependency-free builtins that make HCL expressions more capable (see
`doc/stdlib.md`). Remaining additions to that library:

- **`assert` sub-expression decomposition** — `assert(cond, message?)` ships with
  variables-only operand capture and host-side diagnostic rendering (see
  `doc/stdlib.md`). **Remaining:** reporting `len(xs) = 2`, not just `xs`, by
  re-evaluating operand sub-expressions — opt-in, since it re-runs any function calls
  in the condition.
- **`eval`** — evaluate a lazy / by-expression parameter; ships with the lazy
  `expr`-parameter story (see *Functions* below), since the two are the same feature
  from the author's side.
- **`doc(name)` — function-doc reflection** — *shipped* (`functy.DocFunc`). Possible
  later extension: a value-taking overload (`doc(add)`) if functions ever become
  first-class values (see *First-class function values / closures*).
- **`help(name)` — function help reflection** — *shipped* (`functy.HelpFunc`) for both
  functy and non-functy functions. Known limitation: a Go builtin that emulates
  optional/defaulted args through a `VarParam` can't be shown with its intended
  signature — that structure isn't recoverable from cty. Possible extensions:
  - **Host-registered argument docs for Go builtins** (addresses the limitation
    above). Let a host supply proper signature/parameter metadata for its
    `VarParam`-based builtins — a small registry (name → parameter names, optionality,
    per-arg docs) that `HelpFunc` consults *before* falling back to raw cty
    introspection. Open: the registration shape (a plain struct vs. reusing `FuncDecl`),
    and whether it also feeds `doc()`.
  - A value form when/if functions become values, and a no-argument `help()` that lists
    all functions.
- **Reflection over global variables** (not urgent) — extend `doc()` / `help()` (or a
  sibling builtin) to *variables*, not just functions, with two sources mirroring the
  function story:
  - **functy-defined** top-level `var` / `const` already carry `Decl.Doc` (from
    doc-comment metadata) — the data exists; it just needs a lookup path, the reflection
    builtin consulting a name → `Decl` view alongside the function table.
  - **host-defined** globals — `env`, `sys`, ambient providers, `http_status`, … — are
    plain cty values in the eval context's `Variables`, and **a cty value carries no
    description** (unlike `function.Spec.Description`), so their docs must come from a
    **host-registered** table (name → doc) — the same registry shape proposed above for
    Go builtins' argument docs, so the two could share one mechanism.

  Open: name-collision handling (HCL keeps `Functions` and `Variables` in separate
  namespaces, but `doc("x")` takes a single string — check both, or add a namespacing
  convention); whether to document **nested attributes** of rich-object globals
  (`sys.os`, `env.HOME`) rather than only the top-level object; and the registration API.

Explicitly **out of scope**: `tostring` / `length` with `Stringable` / `Lengthable`
dispatch. They must *dispatch behavior* on a rich object's capsule (call its
`ToString` / `Length` methods), which only matters for rich-object users, who already
depend on [`rich-cty-types`](https://github.com/tsarna/rich-cty-types) (which provides
them). functy recognizes the `_capsule` / `_ctx` marker only to *name* a type
(`typeof` / `typekind`, a cheap read of the capsule's name) — not for method dispatch.

## Language — expressions & sugar

- **Pure-expression-statement warning.** Warn when an expression statement is
  obviously side-effect-free (no function call) and its value is discarded.
- **`assert(cond, message?)` sub-expression values.** The `assert` builtin ships (a
  **function**, not a statement, in `Stdlib()` — see `doc/stdlib.md` and *Standard
  library* above for the sub-expression-decomposition item). A dedicated `assert`
  *statement* was considered and rejected: the function gets the same source location
  and introspection, so a statement would add only marginal value.

## Functions

- **Doc-comment metadata → function-backed handler surfaces.** Doc-comment capture
  ships at function, declaration, and parameter level (`FuncDecl.Doc`, `Decl.Doc`,
  `Param.Doc`; see `doc/language.md#doc-comments`). **Future integration point:** a host
  that lets a functy function *back* a callable surface — an MCP tool/prompt, an HTTP
  handler — could pull that surface's description from the function's `Doc`. No host
  wires this up today; Vinculum's MCP/HTTP handlers map to **action expressions**, not
  functy functions (there is no plumbing from a function's `Doc` to a tool/handler
  description), so this needs a function-backed handler surface first. See also
  *Annotations* for an evaluated (`@name`) alternative to comment-based metadata.
- **`extern` declarations — doc registration for host-provided functions.** A function
  registered by a Go host (e.g. a cty function from `rich-cty-types`) has no functy
  source, so `help()` / generated docs / LSP hovers have nothing to show for it. Let a
  package ship an **`extern` declaration block** — normal functy declaration syntax with
  the `extern` keyword and **no body** — carrying the full signature and the usual
  doc-comment metadata (function doc, per-parameter docs, `@warn`/directive lines,
  defaults, param types). The parser reuses the ordinary declaration path but routes
  `extern` decls into a **separate map** (`Result.Externs`) — they are documentation
  records, never compiled or callable. `help()` and doc generation consult that map
  after the real function set.

  ```functy
  // Parse a CIDR block into a network object.
  // @warn Panics on malformed input at call sites without try().
  extern func cidr(
      s,              // the CIDR string, e.g. "10.0.0.0/8"
  )
  ```

  The point is **zero coupling in both directions**: the leaf package exports its extern
  block as an opaque `var Externs = []byte(…)` (a raw-string literal) and never imports
  functy; functy never
  imports the leaf package; the glue that already knows about both (the consuming program,
  or a `RegisterFunctyType`-style hook) calls `functy.RegisterExterns(pkg.Externs)`. This
  beats the obvious alternative — a shared `functydoc` struct package both sides import —
  which reintroduces a shared dependency and forces inventing a second doc format instead
  of reusing functy's own source as the single source of truth. Recorded so the `[]byte`
  choice isn't re-litigated.

  Design points to settle when built:
  - **`extern` keyword, not `;`-for-body.** The keyword lets the parser enforce the
    invariant both ways (extern *with* a body is an error; a non-extern *without* one is
    an error) and reads as a declaration rather than a "was this a typo?" empty function.
  - **Cost, stated honestly.** No *runtime* cost unless `RegisterExterns` is called
    (parsing is deferred to registration). Binary-size cost is at most the literal bytes,
    and only if the linker doesn't dead-code-eliminate the unreferenced `var` — never a
    parse. The trade-off of the bytes format: malformed extern blocks fail at
    **registration time (runtime)**, not compile time.
  - **Precedence + collisions.** A real function shadows an extern of the same name (or
    warn); two packages registering the same extern name collide and are reported, mirroring
    the host's existing transform/type collision detection.
  - **Optional leading `ctx` — currently unspellable, and a reason extern is the right home.**
    Some host functions (the rich-cty-types `get`/`set`/`count`/`call…` family) take an
    **optional leading context**: `get([ctx,] thing, …)`. This is not a real cty parameter —
    it is implemented as a `VarParam` first-argument sniff (a `contextAndThing`-style helper
    that consumes `args[0]` iff it is a context capsule/object, else treats it as the first
    real arg). So cty's `function.Parameter` list can neither express nor *reflect* it: an
    optional param can only sit at the tail, never the head, and the ctx slot has been erased
    into the varargs. functy has no way to spell it in a signature or render it in help today.
    That reflection-blindness is precisely why an author-written **extern** is the right place
    to declare it — the human states what the cty function's metadata cannot reveal. Proposed
    spelling: reuse the already-registered **`ctx` open type** (`IsContextObject` /
    `RegisterOpenType`) — a *leading* parameter typed `ctx` and marked optional is understood
    as the `[ctx,]` convention and rendered bracketed by `help()`. This needs one independent
    primitive: an **optional parameter without a default** (a bare `?` marker, e.g.
    `ctx?: ctx`), since a context has no spellable default value the way trailing `= 0` params
    do — useful on its own beyond ctx. `?` can be **limited to extern initially**: externs are
    docs-only and never compiled, so the marker just feeds signature rendering and carries no
    runtime semantics to pin down. Extending it to real functy functions (what an absent,
    default-less optional arg *evaluates* to — `null`? a distinct "unset"?) is a separable,
    later decision.
  - **Optional lint payoff.** Because externs name real registered functions, a host (or
    `functy check`) can cross-check them and flag *documented-but-unimplemented* and
    *implemented-but-undocumented*. A leaf package can take a **build-time-only** dependency
    on functy to run this validation over its own extern block in CI (optionally a
    `--externs-only` mode) without pulling functy into its runtime graph.
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

## Annotations

- **Declaration annotations (`@name` / `@name(args)`).** A syntactic annotation
  attached to a `var` (and possibly a `func`) declaration, capturing structured,
  host-interpreted metadata as *values* rather than as comment text:

  ```functy
  @annotation
  var foo: T = …
  ```

  An annotation is an **expression** evaluated in a dedicated, host-controlled
  eval context — separate from the normal one — in which the host registers the
  permitted symbols and functions. A bare symbol (`@bar`) resolves to a
  host-registered value; a call form (`@baz("qux")`) invokes a host-registered
  function. The resulting value is recorded on the declaration (a new
  `Annotations` field on `Decl` / `FuncDecl`), and a declaration may carry a
  **list** of annotations. The host likely declares which annotation names are
  allowed (an allowlist), so an unknown `@name` is a diagnostic rather than
  silently ignored — and so the annotation context is closed, not the full
  ambient scope.

  Because the values are evaluated (not just parsed as text), a host receives
  typed, validated metadata it can act on structurally. Illustrative use case in
  Vinculum: a top-level `var` normally maps to a Vinculum `var` block, but
  annotations could redirect it to a *different* host construct — e.g.
  a `metric` block:

  ```functy
  @help("Number of currently active jobs")
  @labels(["queue", "priority"])
  @namespace("myapp")
  @metric("gauge")
  var active_jobs: number
  ```

  (Illustrative only — whether Vinculum would actually let metrics be declared
  this way, as an alternative to `metric` blocks in VCL as today, is a separate
  host decision; the point here is the functy-side mechanism.)

  This is an **alternative to comment-based metadata** — the *Doc-comment
  metadata* (Functions) and *Per-function directives* (Type system) items below.
  Trade-off: annotations are first-class, evaluated, host-validated expressions
  (structured values, arguments, an allowlist), where directive/doc comments are
  free-form text parsed by convention. The two could coexist (comments for
  human-facing docs, annotations for machine-interpreted host metadata) or
  annotations could subsume the directive use cases. Open questions: whether
  annotations apply to `func` as well as `var` (and `const`), the exact evaluation
  timing (parse-time vs. a host pass, given the special context), and whether the
  annotation context may reference the declaration's own name/type.

## Error handling

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
- **`functy fmt`** — canonical formatter — *shipped* (`functy.Format` /
  `(*Parser).Format`, and the `functy fmt` CLI verb; see `doc/cli.md`). Remaining
  niceties, all cosmetic: block-comment interiors and the multi-line-vs-inline parameter
  choice follow the source rather than being canonicalized.
- **LSP / editor support** — diagnostics, hovers (using doc-comment metadata),
  go-to-definition, completion. Editor-agnostic; the VSCode extension below is its
  first client (and can ship static features ahead of the server).
- **VSCode extension for `.cty` — nearer-term.** A full-featured extension is the
  highest-leverage editor investment (the target audience overlaps the
  HCL/Terraform crowd, who live in VSCode) and, importantly, **stages cleanly**: the
  static half needs no language server and can ship first.
  - **Static, ship-first (no server):** a TextMate grammar with **embedded HCL** for
    expression/type regions (so operators, templates, and function calls highlight
    with the same rules as HCL elsewhere — mirroring how the parser hands expressions
    to `hclsyntax`); a language configuration (`//` and `#` line comments, bracket
    matching / auto-close, indentation); `.cty` file association and an icon;
    snippets for `func` / `if` / `for` / `switch` / `test`; and commands that shell
    out to the existing CLI — **Run** (`functy run`), **Check** (`functy check`), and
    **Format** wired to `functy fmt` as the document formatter / format-on-save (gated
    on that item shipping).
  - **Test Explorer integration** — surface `test "…"` blocks in VSCode's native
    Testing UI (run / re-run individual tests), driving `functy test --run <name>`
    under the hood. This is the concrete consumer of the shipped test runner and its
    **`--json` output** (see *Inline tests* below) — machine-readable results are what
    make per-test pass/fail/skip reporting in the UI clean.
  - **LSP-backed, later:** once the language server (above) exists, the extension
    becomes its client, upgrading diagnostics, hovers (doc-comment metadata),
    go-to-definition, and completion from "none / grammar-only" to full semantic
    support without changing the static layer.

  Recorded as **nearer-term** precisely because the static + CLI-command layer
  delivers most of the day-to-day value (highlighting, run/check/format, test
  running) with no server to build, deferring the LSP work rather than blocking on
  it.
- **Inline tests** — *shipped*: co-located `test "…" { … }` blocks, the core runner
  (`(*Result).RunTests` / `RunTestsMatching`), the `functy test` CLI verb (quiet/`-v`,
  `--run` name filter, machine-readable `--json` report), and a test-only
  `skip("reason")` builtin (see `doc/language.md#tests`). Per-test setup/teardown is
  already expressible (leading statements + `defer`). Remaining niceties: soft /
  non-fatal assertions or a `t`-style test context (today a test stops at its first
  failure, like Go/pytest); shared `beforeEach`-style setup (fresh mutable fixtures per
  test); and no-argument file discovery (`functy test` over `*.cty` in the working dir).
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
  is expected to land alongside it. The *Annotations* section proposes an evaluated,
  host-validated `@name` alternative to these comment-based directives.
- **User-defined rich-object types ("classes") — highly speculative, blue-sky, not
  near-term.** A `class` construct that **compiles to a cty capsule / rich-object type**
  authored in functy, letting a `.cty` file define new value types that behave like the
  host's built-in rich objects (eg `bus`, `url`, `bytes`). The hard constraint is that
  **cty values have no methods**: a value carries data, and behavior is reachable only
  through the *fixed set* of Go interfaces the rich-object machinery dispatches on —
  `Stringable` (`ToString`), `Lengthable` (`Length`), a callable interface, and the
  get / set / index / iterate interfaces the `generic` ops use. So a functy "class"
  could **not** expose arbitrary user-named methods (`x.foo()` — cty has no such
  value-method call syntax anyway); it could only *implement that closed interface set*,
  each implementation being a functy function bound to the instance's state (an instance
  being a capsule wrapping that state). This leans on two other items here: *First-class
  function values / closures* (Functions) — a "method" is a closure over the instance —
  and the rich-object dispatch functy deliberately delegates to
  [`rich-cty-types`](https://github.com/tsarna/rich-cty-types) (see *Standard library*,
  out-of-scope), which would be the natural runtime for the generated interface
  implementations. Open questions (and why it stays blue-sky): how a class registers its
  type at compile time so annotations / other files can name it; how instances are
  constructed and (given cty immutability) whether "mutating" methods simply return new
  values; and whether the payoff justifies the machinery versus authoring such capsule
  types in Go as today. Recorded as a direction, explicitly **not planned any time
  soon**.

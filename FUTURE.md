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

Designs that were worked out and then *declined* are not here — they live in
`REJECTED.md`, so this file stays a map of intent rather than a graveyard.

## Standard library

functy ships its own **optional** standard library — `functy.Stdlib()` (`typeof`, `typekind`,
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
  - A value form when/if functions become values.
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

## Language — expressions & sugar

- **Pure-expression-statement warning.** Warn when an expression statement is
  obviously side-effect-free (no function call) and its value is discarded.

## Functions

- **`extern` declarations — signatures and docs for host-provided functions.** A function
  registered by a Go host (e.g. a cty function from `rich-cty-types`) has no functy
  source, so `help()` / generated docs / LSP hovers have nothing to show for it — and
  `check` cannot type-check calls into the host library at all, since a host function is
  an opaque cty value. Let a package ship an **extern declaration file** — normal functy
  declaration syntax with **no bodies** — carrying the full signature and the usual
  doc-comment metadata (function doc, per-parameter docs, `@warn`/directive lines,
  defaults, param types). The parser reuses the ordinary declaration path but routes these
  decls into a **separate map** (`Result.Externs`) — they are declaration records, never
  compiled or callable. `help()` and doc generation consult that map after the real
  function set.

  ```functy
  //functy:extern

  // Parse a CIDR block into a network object.
  // @warn Panics on malformed input at call sites without try().
  func cidr(
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
  - **A file-level directive (`//functy:extern`), not an `extern` keyword.** Extern-ness is
    a property of the **file**, not of a declaration: `private extern` is meaningless (extern
    means global-and-defined-elsewhere by definition), and in practice an extern file is
    *only* declarations — it is shipped by a package documenting its Go functions, so it
    holds no real functy code. A package that also wants to contribute a functy-written
    function puts it in a separate file. Spelling the mode at the level it actually lives at
    costs no new keyword, and it composes with the `_` convention below (which is spelled at
    the declaration level, where *it* lives) rather than competing with it — so the two
    features are consistent, not arbitrarily different.

    The directive is also what makes the "no body" marker safe. The obvious alternative —
    letting a bodiless `func` mean extern anywhere, C-header style, with no marker at all —
    is a trap: a half-typed function (signature written, brace not yet opened) would silently
    become a valid extern declaration instead of a syntax error, which is directly hostile to
    the mid-edit tooling resilience the parser/lexer recovery work exists to protect. Gating
    bodiless decls on an opted-in file keeps them an error everywhere else, and a directive
    line is not something one typos into existence. In extern mode the parser stops expecting
    bodies and rejects `var` / `const` / `test`; `type` aliases are also rejected until a case
    for them appears (an extern signature names types the host registered via `RegisterType`,
    not aliases). A `_`-prefixed name in an extern file is an error — extern means global.
  - **Spelling.** `//functy:extern` keeps the term already used throughout this document and
    names the construct rather than one of its uses. Avoid `doc_only`-style names: docs are
    only the *first* consumer, and the bigger long-run payoff is that real signatures for
    host functions let `check` type-check calls into the host library — a name built around
    "doc" would be a lie by then. `//functy:declarations` is the runner-up (the TypeScript
    `.d.ts` framing) if a less C-flavored word is wanted, or if such a file might later carry
    non-function declarations.
  - **The three-set `Result` (shared with visibility below).** Between externs and `_`
    privates, `Result` carries three name sets rather than one — **exported** (goes into the
    map handed to the host), **private** (goes only into the interpreter's child context),
    and **extern-declared** (goes nowhere executable, but feeds reflection). Every consumer
    then has to answer *which set*: `symbols` wants all three for the outline, no-arg `help()`
    wants exported + extern, the host's merged map wants exported only, and the
    documented-but-unimplemented lint below is precisely a diff between extern and the host's
    registry. Worth building the three-set `Result` once rather than bolting the second
    feature onto a one-set design later.
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
- **Declaration visibility (`_` prefix)** — *shipped*, as one feature with namespacing; see
  **Namespaces + `_` visibility** under *Top-level constructs* for what remains, and
  `doc/language.md` for the feature itself.
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

- **Namespaces + `_` visibility — unit-scoped names** — *shipped.* A file may open with
  `namespace foo::bar`; its functions register with the host under qualified names while
  the namespace's own functions call each other by bare names, and a `_`-prefixed
  declaration is namespace-local (compiled, callable within the namespace, never handed
  to the host). A namespace spans files; nesting is a naming convention, not containment
  (no parent fallback). Implemented as a name-resolution *layer* in the eval-context
  chain: `unitCtxFn` (`builder.go`) wraps the host's late-bound context in a child whose
  `Functions` map holds the namespace's bare names, and HCL's chain walk does the
  resolving. See `doc/language.md` (*Namespaces and visibility*) and `CHANGELOG.md`.

  Remaining work, in rough order of appeal:

  - **Module / import.** Deliberately excluded from the shipped feature — a namespace is
    usable immediately via its fully-qualified name, so imports are purely additive and
    unconstrained by the mechanism. The layer already expresses every candidate design:
    prefix-preserving use (`lib::foo()`) needs **no** injection at all, since the
    qualified name is already in the host map, while `from foo import *` and
    `import foo::bar as baz` are each just entries in the importing unit's layer. The
    open questions are all language-level (spelling, aliasing, whether a bare import is
    even wanted), not runtime.
  - **Namespace-scoped type aliases.** Type aliases stay project-scoped (one flat
    `env.named`, resolved by a token pre-scan before any file is parsed). The blocker is
    not effort but spelling: `::` is a *function-call selector* in HCL, so
    `foo::bar::MyType` in a type annotation is a parse error — a namespace-scoped alias
    would be unreferenceable from outside its namespace, i.e. strictly worse than today.
    Doing this properly needs a functy-internal qualified-type syntax, or per-namespace
    `typeEnv` layers with bare-then-qualified resolution. Consequence today: two files in
    different namespaces still collide on `type Id = …`.
  - **HCL's "no functions in namespace" diagnostic is misleading inside functy bodies.**
    HCL builds its available-names list from the *innermost* context only
    (`hclsyntax/expression.go`), which at a functy eval site is the scope child (whose
    `Functions` is nil). So a typo'd qualified call reports *"There are no functions in
    namespace \"foo::bar::\""* even when the namespace exists and is populated. Pre-existing
    (the same defect affects name suggestions for unqualified calls), but namespaces make
    it the default experience for a mistyped call. The fix is upstream in HCL.
  - **REPL namespace context.** The REPL evaluates against the host context, so namespaced
    functions need qualified names and privates are unreachable from the prompt. Correct by
    design — the session is not "inside" any namespace — but a `:namespace foo::bar`
    meta-command that pushed the unit layer onto the session context would make a namespace
    explorable interactively. Tab completion (`repl/completion.go`) already treats `:` as a
    word rune, so qualified names prefix-complete correctly today.

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

- **Marker-annotation tier (`@name` only) as a first, self-contained slice —
  motivated by `@standalone` tests.** The full design above evaluates each
  annotation as an expression in a host-controlled context; a much smaller subset
  covers a real, immediate need, and the full feature later subsumes it. A
  **marker** is the no-argument, no-context case: the parser recognizes a leading
  `@ident` before a declaration and records the bare name (an `Annotations` field
  as `[]string` on `Decl` / `FuncDecl` / `TestDecl`), with no eval context and no
  allowlist evaluation — only the syntax and attachment plumbing the full feature
  also needs. Shipping markers first de-risks the larger design by proving that
  plumbing on a concrete use case.

  The motivating case is **test selection in host-coupled sources**. When functy is
  linked into a host, a `test` block may exercise host functions / ambient values
  (`send`, `bus.*`) that exist only under the host's full eval context — so bare
  `functy test` (baseline context) cannot run it. A marker resolves this
  declaratively:

  ```functy
  @standalone
  test "add sums" { assert add(2, 3) == 5 }        // runs under bare `functy test`

  test "publishes an event" { send(bus.out, "t", 1) }   // needs the host context
  ```

  Two decisions this introduces:
  - **`test` as an annotation target.** The full design lists `var` / `const` /
    `func`; tests are declarations too and are the first concrete target (`TestDecl`
    gains the same `Annotations` field).
  - **A functy-built-in annotation vocabulary, distinct from host-registered
    names.** The full design frames annotations as host-registered (the host
    declares the allowlist). But `@standalone` concerns functy's *own* test runner
    and its context, and bare `functy test` has no host to register it — so functy
    owns a small built-in set (`@standalone`, with room for `@slow` / `@skip` /
    `@integration`), recognized by its own tooling, alongside (not replacing)
    host-registered annotations.

  **Policy lives in the runner, not the annotation** (consistent with the
  caller-supplies-the-context discipline of `RunTests`): `functy test` (baseline
  context) runs `@standalone` tests and skips the rest into a *distinct* reported
  bucket ("skipped: requires host"), never silently; a host's `<host> test` (full
  context) runs everything, using the marker only for optional filtering (e.g.
  `--only @standalone`). The scheme is **self-checking**: an unbound host reference
  inside a `@standalone` test is a genuine *failure* (the author asserted "no host
  needed" and was wrong) — which is why this is preferred over auto-classifying
  unresolved references as skips, a heuristic that would silently hide typos. The
  existing test-only `skip("reason")` builtin remains for *conditional* skips (a
  runtime probe); `@standalone` is the static, declarative default. Beyond tests,
  markers generalize to plain tags for CLI filtering.

## Error handling

- **`defer` argument snapshotting.** Evaluate a deferred call's arguments at `defer`
  time while deferring the call itself, matching Go. Requires expression decomposition
  machinery beyond a plain `hcl.Expression`.

## Safety & execution

- **Execution limits (step / time budget).** functy permits unbounded `for` / `while`
  over a tree-walking interpreter, so a single `.cty` file can wedge the process.
  Worth knowing: this is the **one unclaimed safety property** among the embedded-language
  Terraform providers (see `OPENTOFU.md`). None of them bounds execution — the Starlark
  provider even *disables* Starlark's built-in termination guarantees and never sets a step
  budget, so a runaway script hangs `plan`. If functy ever ships a provider, this item
  stops being hygiene and becomes the differentiator. The
  design below is **cooperative, not preemptive** — functy can only
  checkpoint at its *own* interpreter boundaries, so a single long-running host or
  cty-stdlib call (a huge `range()`, a catastrophic regex) runs to completion regardless
  of the budget (see *limitations* below). Recorded so "why not just kill it mid-call"
  is settled: functy has no visibility inside a foreign `cty.Function`.

  Two tiers, the first shippable on its own:

  - **Tier 1 — per-frame loop guard (self-contained; ship first).** A step / iteration
    counter on `interp` (constructed per invocation in `BuildFunction`'s `Impl`, so each
    call gets its own), incremented at every loop backedge in `execForCond` /
    `execForClause` / `execForRange` — and optionally per statement in `execBlock` for a
    finer bound. The ceiling is a `Parser` setter (e.g. `MaxSteps(n)` / `MaxLoopIterations(n)`,
    mirroring `RequireParamTypes`), captured **immutably at compile time** into the `Impl`
    closure — so it rides the existing option plumbing and is **trivially concurrency-safe**:
    no shared mutable state, each invocation counts in isolation. This catches the
    motivating case — a single function's unbounded `for` / `while` — with zero cross-call
    machinery. It does **not** catch recursion / mutual recursion (each nested call is a
    fresh `interp` whose counter starts at zero) nor aggregate work spread across many
    small calls. That is Tier 2.

  - **Tier 2 — evaluation-wide budget (shared; the harder half).** A shared
    `budget{ steps, maxSteps, deadline }` reachable by **every** `interp` participating in
    one logical top-level evaluation — needed to bound recursion (a per-frame counter never
    sees the depth) and total aggregate work, and to carry a wall-clock `deadline`.
    Checkpoints add **function entry** (top of the `Impl` closure) to the loop backedges,
    so deep / mutual recursion and call-count blowups are caught before Go's own stack
    overflow turns them into an ungraceful panic.

    The delivery problem, stated honestly so the obvious reflex is pre-empted: go-cty's
    `function.Spec.Impl` is `func(args []cty.Value, retType cty.Type) (cty.Value, error)`
    — **no `context.Context`** — and each functy `Impl` rebuilds its `parentCtx` from the
    host's `evalCtxFn()` rather than inheriting the caller's scope context, so **there is
    no existing channel** to thread a per-evaluation budget from an outer functy frame into
    an inner one. "Just add a `context.Context` argument" is not available: cty's function
    type doesn't have one, and forking cty's signature defeats the reuse-cty premise (same
    reasoning as the named-arguments rejection under *Functions*). Two candidate channels:
    - **(a) reserved capsule in the eval context.** The host (or a functy `Run` helper)
      installs the budget as a `$`-prefixed capsule value in the `EvalContext` that
      `evalCtxFn()` returns; each `interp` pulls it out at construction and, if present,
      shares it (absent → unbounded, as today). Works **only** if `evalCtxFn()` yields the
      same budget-bearing context for every nested call of that evaluation, and requires
      the host to hand out **distinct** contexts per concurrent evaluation (which it already
      must, for any request-scoped data). This matches functy's "host owns the eval context"
      philosophy: functy defines the capsule type and the decrement/check; the host owns
      the budget's lifecycle and concurrency scoping.
    - **(b) a functy-owned run entry** that establishes both the budget and the context that
      carries it — a larger public surface; deferred in favor of (a).

  - **Error semantics.** Breaching a limit raises a sentinel `LimitError`, modeled on
    `SkipError` (errors.go): recovered from diagnostics at the function boundary the way
    `skipFromDiags` recovers a skip, and — crucially — **not** matched by `try` / `catch`
    or `val, err =`. Otherwise `try { while true {} }` would fire the guard, unwind into
    the catch, and loop again, swallowing the very protection. A breach therefore
    **terminates the whole evaluation**, uncatchable, straight out through every enclosing
    frame. Open: whether defers still run afterward — a small fixed grace (Go's
    panic-still-runs-defers) versus skipping them to bound post-breach cost; lean toward
    **skipping**, since a defer can itself loop.

  - **Config surface.** Tier 1 via `Parser` setters, captured at compile (no runtime
    state). Tier 2's wall-clock deadline and shared step budget via the per-evaluation
    channel above, since that counter is mutable and per-run. `0` / unset means **unbounded**
    (opt-in — existing embeddings are unchanged). Vinculum, as a long-lived server, would
    ship a non-zero default that config can override; the standalone CLI / REPL a generous
    default so interactive exploration isn't clipped.

  - **Limitations (in scope to *state*, out of scope to *solve* here).** No preemption
    inside a host or cty-stdlib call — functy checkpoints only at its own backedges, so
    bounding a runaway `range()` / regex is the host's job (a `context`-aware stdlib or an
    OS-level watchdog). No **memory** limit (an ever-growing list/map is not a step/time
    budget) — a separate mechanism, not planned. Composes with the *Sandbox / pure mode*
    item below: a `pure`, no-side-effect function run under a budget is the safe-to-run-
    untrusted target the two features jointly enable.
- **Sandbox / pure mode + memoization.** The host already controls which functions
  populate the eval context, so a "no side effects" mode is largely free; a `pure`
  marker on a function could additionally enable **memoization / caching** of its
  results for repeated identical calls.

## Tooling & ecosystem

The standalone `functy` binary already provides `run` and `check`. It exists for
development, testing, and experimentation — not for production use (a host application
links the library directly and supplies its own richer context). Planned additions:

- **`functy repl` / `functy run -i [FILE …]`** — interactive REPL — *shipped*
  Remaining niceties, all
  optional: a public statement-eval API (today the REPL evaluates expressions, not
  `var`/`for`/`if` statements) and per-session execution limits once the *Execution
  limits* item lands.
- **`functy fmt`** — canonical formatter — *shipped* (`functy.Format` /
  `(*Parser).Format`, and the `functy fmt` CLI verb; see `doc/cli.md`). Remaining
  niceties, all cosmetic: block-comment interiors and the multi-line-vs-inline parameter
  choice follow the source rather than being canonicalized.
- **LSP / editor support** — diagnostics, hovers (using doc-comment metadata),
  go-to-definition, completion. Editor-agnostic; the VSCode extension below is its
  first client (and can ship static features ahead of the server).
- **VSCode extension for `.cty`** — *shipped* (separate repo `vscode-functy`, its own
  `README.md` / `CHANGELOG.md` / `FUTURE.md`). The static + CLI-command layer is done:
  TextMate grammar, language config, snippets, Run/Check/Format, Evaluate Selection,
  Run-with-arguments, REPL integration, version gating, tasks + workspace commands, the
  `functy symbols`-backed outline, the Test Explorer (driven by `functy test --json`) with
  continuous run, and a Get Started walkthrough. **Remaining on the functy side:** the
  language server (see *LSP / editor support* above and the linked `functy lsp --stdio`
  work) — the extension already has the client wiring planned and becomes its consumer,
  upgrading diagnostics/hover/definition/completion without touching the static layer.
- **Machine-readable CLI surfaces for editor tooling (a pre-LSP bridge).** Small,
  JSON-emitting CLIs that expose *LSP primitives over the CLI* (reusing the parser, type
  checker, `help`/`doc` reflection, and `functy.Format`) so an editor gets "IDE feel"
  without the language server. The high-leverage ones have **shipped and are adopted** by
  `vscode-functy`: `check --json -` / `run --json -` (stdin + `--filename` → live on-type
  diagnostics), `eval --json`, `symbols --json` (outline + test discovery), and
  `version --json`. Their JSON contracts now have downstream consumer tests in
  `vscode-functy` (`src/protocol.ts`), so treat a shape change as breaking. Still wanted:
  - **`functy doc --json <name>` / `functy help --json <name>`.** `DocFunc` / `HelpFunc`
    already produce this content; a JSON form lets a client render **pre-LSP hovers** (spawn
    on hover, cache) — a stepping stone toward the LSP hover, reusing the same reflection
    data. Lower urgency: a client-side hover is exactly what the LSP subsumes, so this may
    just fold into the language server.
  - **`functy fmt --range START:END`** — range formatting / format-on-type for the editor's
    range formatter. Lower priority; whole-document `fmt` covers the common case.

  The CLI forms are not throwaway: the LSP later provides the same information in-process,
  but they remain useful for scripting and non-LSP editors.
- **Parser/lexer error-recovery hardening (for mid-edit tooling)** — *the two
  file-swallowing cases are shipped.* The parser already recovered well —
  `recoverToTopLevel` / `skipToParamBoundary` / `recoverToStatementEnd` let
  `symbols`/`check` still report the declarations *around* a syntax error, so an editor
  outline survives most transient edits (a half-typed expression, an incomplete parameter).
  Two cases used to truncate everything after the error, both common **while typing**;
  both are now fixed:
  - **An unterminated `{`** (you've typed `func f() {` but not the closing brace) made the
    body swallow the rest of the file — the block-open had no matching close, so
    `recoverToTopLevel` never found the next declaration boundary. *Fixed* in
    `parseStatements` (`parser.go`): a `func` keyword can never appear at statement
    position (closures / nested functions are a non-goal — see DESIGN.md), so it is an
    unambiguous signal that an enclosing block was left open and the next top-level
    declaration has leaked into this body. The statement loop breaks on it, the body
    reports its missing `}`, and `parseFile` resynchronizes on the leaked `func`.
    (`func` is the only *unconditionally* safe resync token: `var` is legal in a body,
    `const` has its own in-body diagnostic, and `test`/`type` are contextual idents that
    are legal as ordinary expressions — so gating those on column-0 would risk breaking
    valid code, whereas `func` alone covers the dominant real case.)
  - **Unterminated quoted string** (`return "oops` with no closing quote) — the *real*
    lexer-level derailment. HCL enters string mode and consumes the entire rest of the
    file as `TokenQuotedLit`/`TokenQuotedNewline` content (the closing brace, every later
    declaration), so `recoverToTopLevel` never sees another keyword. *Fixed* in `lexAll`
    (`lexer.go`): on the `TokenQuotedNewline` marker HCL emits for the offending newline,
    the broken string is cut, a real newline is emitted to terminate the statement, and
    the remainder is re-lexed from just past the marker (`LexConfig` honors the start
    position, so ranges stay absolute); one genuine "Invalid multi-line string"
    diagnostic is kept and the phantom per-line ones are dropped. (Note: the *originally
    recorded* second case — "stray `@`, `$`, backtick" — turned out **not** to be broken:
    HCL emits a lone `TokenInvalid`/`TokenBacktick` and keeps the rest of the stream
    coherent, so existing parser recovery already handled those. The unterminated string
    is the case that actually swallowed the file.)

  Remaining nicety (lower priority): an **unterminated heredoc** (`<<EOT` with no closing
  marker) is the same class as the unterminated string but derails via a different token
  pattern (`TokenOHeredoc` without a close) and is not yet resynchronized. Rare enough to
  defer. Neither shipped fix is required for correctness — a file that doesn't parse is
  genuinely broken — but both let any `symbols`/`check`-backed tool (outline, test
  discovery, on-type diagnostics) keep working through the transient breakage of live
  editing, matching the resilience of the regex scanner the parser-backed outline replaced.
- **Inline tests** — *shipped*: co-located `test "…" { … }` blocks, the core runner
  (`(*Result).RunTests` / `RunTestsMatching`), the `functy test` CLI verb (quiet/`-v`,
  `--run` name filter, machine-readable `--json` report, and no-argument discovery of
  `.cty` files in the working directory) and a test-only `skip("reason")` builtin (see
  `doc/language.md#tests`). Per-test setup/teardown is already expressible (leading
  statements + `defer`). Remaining niceties: soft / non-fatal assertions or a `t`-style
  test context (today a test stops at its first failure, like Go/pytest); and shared
  `beforeEach`-style setup (fresh mutable fixtures per test).
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

  The host picks identity vs. open per type.
- **An OpenTofu provider — see `OPENTOFU.md`.** OpenTofu 1.7 lets a *configured* provider
  mint functions dynamically (an OpenTofu-only protocol feature), so a `functy` provider
  handed `file("./lib.cty")` can expose real, statically-typed `provider::functy::name`
  functions — **no core change, no fork, and no build step for the user.** Two
  proof-of-concept providers (Go/Yaegi and Lua) already do this with other languages;
  functy fits better than either, since a compiled functy function *is* already a
  `cty.Function` with a static signature. Nothing is planned, but this is the one
  Terraform-adjacent idea that is buildable today. What is *not* possible — on Terraform,
  and in the language itself — is analyzed separately in `TERRAFORM.md`.

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

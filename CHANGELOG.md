# Changelog

All notable changes to functy are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and the project will follow [Semantic Versioning](https://semver.org/) once
tagged. Until then, everything lives under **Unreleased**.

## [Unreleased]

## [0.7.1] - 2026-07-07

### Added

- **Release pipeline for the `functy` CLI.** A GoReleaser configuration
  cross-compiles the `cmd/functy` binary for linux/darwin/windows on
  amd64/arm64, and a tag-triggered GitHub Actions workflow publishes the
  archives and checksums on each `v*` tag. The GitHub Release body is the
  matching section of this changelog.
- **`functy version` command and `--version` flag.** Report the build's
  version, commit, and date, injected at release time via `-ldflags`.

## [0.7.0] - 2026-07-06

### Added

- **Interactive REPL — `functy repl` / `functy run -i`, and the `repl` package.**
  A read-eval-print loop over the HCL expression engine: each line is parsed as a
  single expression and evaluated against a live context, with results echoed
  HCL-style and numbered into a `_` / `_N` history, session bindings
  (`NAME = EXPR`), `:`-meta-commands (`:help`, `:set`/`:unset`/`:vars`, `:quit`),
  multi-line continuation, value formatting, and tab-completion (including dotted
  attribute paths). The new `repl` package exposes the engine as a reusable
  library: it is parameterized over a small `Host` interface
  (`EvalContext`/`CompletionContext`/`Reserved`) plus an `Options` struct (banner,
  history path, extra meta-commands, lifecycle hooks), and depends only on
  readline/HCL/cty — logging and tracing stay host concerns — so a richer host can
  layer its own context and commands on top. `NewStaticHost` drives it over a fixed
  context; the CLI uses it over the baseline. `functy repl [FILE...]` (equivalently
  `functy run -i`) loads all files into one context, runs the entry function if
  present, then starts the REPL: a missing default `main` is silently skipped (an
  explicitly named `--func` that is absent is still an error), and zero files are
  allowed interactively — non-interactive `run` still requires at least one.
- **`functy test --json` — machine-readable test report.** A single self-contained
  JSON report (a `tests` array plus a summary) as an alternative to the
  human-readable output, for CI and editor tooling (e.g. a VSCode Test Explorer).
  Each entry carries status, duration, and the test block's source location; a
  failure adds the assert/throw message, operand detail, and the range to
  underline; a skip adds its reason. A compile failure still emits a well-formed
  empty report and exits non-zero, so consumers can always parse stdout. Exit
  status is unchanged.

## [0.6.0] - 2026-07-05

### Added

- **Source formatter — `functy.Format` / `functy fmt`.** A canonical formatter for
  `.cty` source: four-space indentation, normalized spacing (expressions via
  `hclwrite`, matching `terraform fmt`), blank-line runs collapsed to one, and
  comments — including doc comments and per-parameter comments — kept in place
  (standalone, trailing, and dangling). A multi-line parameter list is preserved one
  per line (an inline list stays inline); `while` and the `x := 0` shorthand are kept
  as written. It reformats a file only when it parses cleanly, so it never drops or
  reorders code, and it is idempotent. The
  `functy fmt` CLI verb formats stdin→stdout, or files/directories with `-w`
  (rewrite in place) or `-l` (list files that differ). `Format` is also a
  `(*Parser)` method so a host whose parser registers named types can format its own
  files. See [doc/cli.md](doc/cli.md#fmt).

### Fixed

- **Parser: a malformed parameter list no longer loops forever.** `func f( {` (an
  unclosed `(` before the body brace) previously hung the parser; it now reports an
  "unterminated parameter list" error and recovers.

## [0.5.0] - 2026-07-03

### Added

- **Doc-comment metadata + comment retention.** A contiguous leading `//` / `#`
  (or `///`) comment block directly above a declaration is now captured as its
  documentation: `FuncDecl.Doc` (functions) and `Decl.Doc` (top-level `var` /
  `const`). Directive lines are excluded from the prose; block comments do not form
  docs. This is built on a new comment-retention foundation — `Result.Comments`
  holds **every** comment with its source position (the statement parser stream
  stays comment-free), captured in a single lex pass. Each function's `Doc` is also
  wired into its compiled cty `function.Spec.Description`, so the description reaches
  the runtime function object and any cty tooling. A host can use `Doc` for generated
  docs, editor hovers, or anywhere it wants a function's description available at
  runtime. Foundation for a future `functy fmt`. See
  [doc/language.md](doc/language.md#doc-comments).
- **`doc(name)` reflection builtin — `functy.DocFunc(evalCtxFn)`.** Returns a
  function's description by string name (`doc("add")`), read from the merged eval
  context's function table. Tri-state: `null` (no such function), `""` (exists but
  undocumented), or the description. Not part of `Stdlib()` because it needs the
  late-binding context handle. See
  [doc/stdlib.md](doc/stdlib.md#docfuncevalctxfn--context-aware).
- **Per-parameter docs — `Param.Doc`.** A parameter can be documented with a
  trailing comment on its line (`a: T, // desc`) or a leading `//` / `#` block above
  it (leading wins if both), confined to the multi-line parameter layout. Required
  parameters' docs also flow to the compiled cty `function.Parameter.Description`.
  Feeds `help()` (below). See [doc/language.md](doc/language.md#parameter-docs).
- **`help(name)` reflection builtin — `functy.HelpFunc(funcs, evalCtxFn)`.** Returns
  a human-readable help summary — signature (names, types, defaults, variadic, return
  type), description, and per-parameter docs — for a function by name, or `null` if
  unknown. functy functions render from their declaration (accurate); non-functy
  functions fall back to best-effort cty metadata. See
  [doc/stdlib.md](doc/stdlib.md#helpfuncfuncs-evalctxfn--context-aware).

- **Function declarations may span multiple lines.** Newlines inside the parameter
  list (after `(`, between parameters — including comment lines — and before `)`,
  with a trailing comma allowed) and across the rest of the signature (before `->`,
  its type, and the `{`) are now insignificant; previously a declaration's signature
  had to be written on a single line. Only affects `func` *declarations*; call
  argument lists (parsed by HCL) already allowed multiple lines.

## [0.4.0] - 2026-07-01

### Added

- **Inline tests — `test "description" { … }` blocks + `functy test`.** A top-level
  `test` block declares a co-located test whose body is ordinary functy statements
  (calling functions, using `assert`). A test passes if its body runs to completion
  and fails if an error unwinds — driven by `assert`, so a failure reports *where* and
  *why* (source line + operand values). `test` is a *contextual* keyword (special only
  at top-level declaration position), so it stays usable as an ordinary identifier;
  test blocks are collected in `Result.Tests`, not the function namespace, so
  `functy run` ignores them. A host runs them with `(*Result).RunTests(evalCtxFn)`
  (returning a `TestOutcome` per test, each with a `Diagnostics()` for rendering
  failures); the `functy test` CLI verb reports pass/fail with source context and exits
  non-zero on failure. See `doc/language.md#tests` and `doc/cli.md#test`.
  - **`skip("reason")`** — a test-only builtin that marks the current test skipped
    (neither passed nor failed), callable directly or from a helper the test invokes;
    surfaces as the exported `SkipError`. `TestOutcome` gains `Skipped`/`SkipReason`,
    `Failed()`, and a `Duration`.
  - **`functy test --run PATTERN`** runs only tests whose description matches a regular
    expression (core: `(*Result).RunTestsMatching`), and **`-v`/`--verbose`** lists
    every test (`ok`/`SKIP`/`FAIL`) with per-test timings; the default output is quiet
    (failures + summary only). Skips and `--run`-deselected tests are not failures.
- **Render an uncaught error with source context — `functy.ErrorDiagnostics` /
  `(*ThrownError).Diagnostics()`.** An error that no `catch` handles unwinds to the
  host as a `*ThrownError`; these turn its carried value into `hcl.Diagnostics` (message
  → summary, the operand `detail` → detail, the `range` → `Subject`) for the standard
  `hcl.NewDiagnosticTextWriter`, so a host renders the failing source line and operand
  values instead of a flat message. The `functy run` CLI now does this for uncaught
  errors. The renderer takes an error *value*, so it also serves a future inline-test
  runner. (Adds `ctyToRange`, the inverse of the existing `rangeToCty`.)
- **Standard library — `functy.Stdlib()` / `functy.StdlibExtras()`.** Dependency-free
  expression builtins a host merges into its eval context, making HCL expressions more
  capable: `typeof` (a value's type in functy's annotation grammar, e.g.
  `object({ a = string })`, so it round-trips — and a rich object carrying a
  `_capsule` / `_ctx` marker is named by that capsule, e.g. `bytes` / `ctx`),
  `typekind` (the top-level kind, for dispatch), `cond` (lazy multi-branch — the
  single-eval conditional HCL's eager `?:`
  can't be), `switch` (lazy value dispatch), and `error` (raise from expression
  position, composing with `try`/`catch` and `val, err =`); plus the opt-in `try`
  (single-eval, unlike stock HCL `try()`) and `can`. All lazy builtins evaluate
  each branch at most once. Uses `hcl/v2/ext/customdecode` and `ext/tryfunc` — no new
  module dependency. See `doc/stdlib.md`. (The `switch()` builtin is
  expression-position only, since `switch` is a statement keyword.)
- **`assert(cond, message?)` in `Stdlib()`.** A runtime check that raises a
  catchable error when `cond` is false — the expression-position analog of a guard.
  The condition is received unevaluated, so the error carries the *condition's*
  source `range`; on success it returns `true`. The optional `message` (a string or
  object, like `error()`/`throw`) is lazy — evaluated only on failure — and defaults
  to `"assertion failed"`. It composes with `try`/`catch` and `val, err =`, and a
  condition that itself fails to evaluate propagates that error (a structured throw
  survives) instead of masking it as an assertion failure. On failure it also reports
  *why*: it captures the values of the variables the condition references (pytest-style)
  and attaches them as `detail` (a rendered string, e.g. `"n = -3"`) and `operands`
  (a list of `{ name, value }` with the raw values, inspectable in a `catch`). Only
  referenced variables are captured — via `expr.Variables()`, which never re-runs
  function calls — so gathering operands is side-effect-free.

### Changed

- **Error `value` attribute is now present only when meaningful.** Previously every
  string/eval-failure error carried a null `value` (and object throws carried none at
  all). Now `value` appears **only** when you throw a bare non-string/non-object value
  (`throw 42` → `{ message = "error", value = 42, range }`), the sole case it is used;
  string, object, and evaluation errors no longer carry it. The `error` type still
  requires only `.message`.

## [0.3.0] - 2026-07-01

### Added

- **Errors carry a source `range`.** Every caught error now includes a `range`
  attribute — `{ filename, start = { line, column, byte }, end = { … } }` — marking
  where it was raised (the `throw`, or the failing expression for an evaluation
  error). It is preserved across function-call boundaries and through a rethrow
  (`throw e` keeps the original site), and is `null` when no location is available.
  As part of this, `val, err = expr` now recovers a structured error thrown by a
  called function (matching `try`/`catch`) instead of flattening it.

- **Typed / multiple `catch` clauses.** A `try` may now have several `catch`
  clauses, tried in order (first match wins), each optionally filtered by a type
  (`catch e: T`) and/or a guard (`catch e if cond`). The type filter reuses the
  type system as an error taxonomy — a host-registered open type is an error
  category, a structural object type gives duck-typed matching, `error` matches
  any — and the guard is an arbitrary expression over the bound error. The binding
  is always the **raw** error (a `: T` filter is a gate, not a cast, so attributes
  survive). A catch-all (no type, no guard) must be the last clause; an unmatched
  error re-raises after `finally`, and `throw e` inside a clause rethrows. The
  `Try` AST now carries `Catches []CatchClause` (replacing `HasCatch` /
  `CatchName` / `Catch`).
- **Structured errors survive function-call boundaries.** A thrown error now keeps
  its full object (`code`, `kind`, custom attributes) when caught from a *called*
  function, not just when thrown in the same function — so typed/guarded `catch`
  works across calls. Uncaught throws surface at a function's `cty.Function` boundary
  as the exported `ThrownError` (carrying the error value); a functy caller recovers
  it automatically, and a Go host can via `errors.As(err, &functy.ThrownError{})`.
  Genuine evaluation failures still flatten to `{ message, value }`.
- **Labeled `break` / `continue`.** A `for`/`while` may carry a `label:` prefix,
  and `break label` / `continue label` then target that loop instead of the
  innermost one — the standard way to exit or advance an outer loop from a nested
  one. The label must name an enclosing labeled loop (siblings are out of scope)
  and be unique among enclosing loops; `continue label` runs the target loop's
  post clause, and a labeled break/continue unwinds through intervening
  `try`/`finally` blocks like `return`. The `For` AST gains `Label`, `Break` and
  `Continue` gain a target `Label`.
- **`fallthrough` in `switch`.** As the final statement of a clause it transfers
  control to the next clause in source order, running its body without testing
  (it may fall into a `default`, and a `default` that is not last may fall into
  the following clause). Rejected anywhere but a clause's last statement — not
  nested in an `if`/loop, and not in the last clause. Switch clauses are now kept
  in source order internally (the `Switch` AST exposes `Clauses []Clause` with a
  per-clause `IsDefault`, replacing the separate `Cases`/`Default` fields).
- **`:=` declaration shorthand.** `x := expr` is sugar for an untyped
  `var x = expr`, with the same scoping and duplicate-declaration rules as `var`
  (it declares a new binding and cannot reuse an existing one). It carries no type
  annotation, so it is rejected under strict declared-types (use `var x: T = expr`
  there). The two-target form `val, err := expr` declares both targets and
  captures, combining `:=` with the error-capture assignment below: the value
  target is dynamic and the error target is pinned to the built-in `error` type
  (matching a hand-written `var err: error`). A target may be the blank `_`.
- **Error-capture assignment — `val, err = expr`.** A two-target assignment that
  captures an evaluation failure into a variable instead of unwinding the function
  (the statement-level analog of Go's `v, err := f()`). The right-hand side is
  evaluated once; on success the value target receives the result and the error
  target receives `null`, and on failure the value target receives `null` and the
  error target receives the caught error (the object shape `catch` binds). It is
  exactly sugar for a `try { val = expr; err = null } catch e { val = null; err = e }`.
  Both non-blank targets must already be declared. Either target may be the blank
  identifier `_` to discard it (`_, err = expr` for error-only; `val, _ = expr` for
  Go-style best-effort); `_, _ = expr` is rejected. Pairs with the built-in `error`
  type.

## [0.2.0] - 2026-06-30

### Added

- **Public `TypeResolver`** — functy's type system as a standalone, reusable
  component (independent of parsing `.cty` programs): `NewTypeResolver()` with
  `RegisterType` / `RegisterOpenType`, `ResolveType(hcl.Expression)` (the
  `typeexpr.TypeConstraint` analog, yielding an enforcing `TypeConstraint`), and
  `ParseType([]byte, filename)` for annotations stored as strings. `Parser.Types()`
  exposes the parser's resolver, so a host registers named types once and uses them
  for both parsing and standalone resolution. Added `ConvertType(cty.Type)` to wrap
  an existing cty type as a constraint.
- **Nested capsule types** — a named capsule type (and aliases over one) now
  composes inside collections and structural types (`list(widget)`,
  `object({ w = widget })`), enforced element-wise by identity. Open/predicate
  types (`error`, host open types) remain whole-annotation leaves, since they have
  no single concrete cty type and do not form homogeneous collections.
- **Built-in `error` type** — an open type (an object with at least a string
  `message`, the shape `throw` raises and `catch` binds), usable in any annotation
  (`var err: error`, `func f(e: error)`). It accepts a caught error or `null` and
  rejects anything else; `error` is a reserved type name. The `value, err = expr`
  capture sugar (§13.4) remains future work.
- **Strict typing** — opt-in requirements that type annotations be written
  (`any` still allowed, but explicit). Parser flags `RequireParamTypes`,
  `RequireReturnType`, `RequireDeclaredTypes`, and the in-source directives
  `//functy:strict` / `//functy:require param_types return_type declared_types`.
  Off by default; tighten-only (a file may add a requirement but not relax a
  host-set one); violations report whether the rule came from the host or a file
  directive. `: any` / `-> any` / `-> null` are the explicit escapes.
- **Generalized directive comments** — a public `Directive{Namespace, Name, Args,
  Range}`; a directive comment is a line comment with no space after `//`, of the
  form `//<namespace>:<name> [args]`. Each source's leading-block directives are
  collected into `Result.Directives`; functy interprets only its own `functy:`
  namespace and passes the rest through for the host. (Per-function directives are
  deferred.)
- **`type` aliases** — `type Name = <type>` at file top level, reusable in any
  annotation. Aliases are **order-independent** (may be used before declaration;
  cycles are reported) and **project-scoped**: every alias from the sources parsed
  together is visible to all of them, like the function namespace (a function in one
  file may use a type declared in another). A concrete alias (`type Id = string`)
  nests inside collection/structural types; an alias over a host capsule/open type is
  a whole-annotation leaf only. Aliasing a built-in or registered type name, a
  duplicate name (within or across files), and cycles are errors. Parser exposes
  collected aliases as `Result.Types`.
- **Own type resolver with a host-pluggable named-type environment** (replaces
  delegation to `ext/typeexpr`, which is now used only as a test oracle):
  - `Parser.RegisterType(name, cty.Type)` — register a named/capsule type,
    enforced by **type identity** (the value must already be of that type, or
    `null`).
  - `Parser.RegisterOpenType(name, pred)` — register an **open** type enforced by
    a predicate; a satisfying value passes through untouched, so extra attributes
    are preserved (non-destructive).
- **`null` (void) return type** — `func f() -> null { … }`. Inside such a
  function a `return` may only be bare or `return null`; any other
  `return <expr>` is a compile-time error. `null` is rejected as a
  `var`/`const`/parameter type.

### Changed

- Type annotations are resolved by functy's own resolver. The built-in grammar
  (`string`/`bool`/`number`/`any`, `list`/`set`/`map`/`object`/`tuple`/`optional`)
  is unchanged and validated against `typeexpr` for parity in tests.
- A declared type is now represented by a single public `TypeConstraint`
  (`Cty()` for the underlying `cty.Type`, `Coerce()` for enforcement) on
  `FuncDecl.RetType`, `Param.Type`, `VarDecl.Type`, and `Decl.Type` — replacing
  the previous `cty.Type` field. A bare `cty.Type` cannot represent an open
  (predicate) type, so the constraint is the single source of truth.

### Documentation

- The original design spec is retired in favor of authoritative documents:
  `doc/` (language + CLI reference), `FUTURE.md` (planned/unimplemented work),
  `DESIGN.md` (vision and build-vs-embed rationale), and `CONTRIBUTING.md`
  (architecture and the expression-boundary algorithm).

### Notes

- Nesting an **open** (predicate-backed) named type — `error`, or a host-registered
  marker-capsule type — inside a collection or structural type is not yet supported
  and is reported as an error in nested position. Capsule named types *do* nest
  (`list(widget)`, `object({ w = widget })`). See `FUTURE.md` ("nested open types").

## [0.1.0] - 2026-06-25

The first functy implementation: a self-contained imperative language whose
values are cty values and whose expressions are HCL, compiling `.cty` files to
`cty` functions.

### Added

- **Language**: function declarations with required / optional / variadic
  parameters and typed or dynamic params and returns; `var` declarations
  (typed and dynamic) and reassignment; expression statements; bare block
  scopes; `if` / `else if` / `else`; `for` (condition, three-clause, and
  `for k, v in` range forms) and `while`; `switch` (value and expression-less
  forms, no fallthrough); `break` / `continue`; structured error handling with
  `try` / `catch` / `finally`, `throw`, and `defer` (LIFO).
- **Expressions** are real HCL expressions (operators, templates, function
  calls, conditionals, …) evaluated lazily against a late-bound eval context,
  enabling recursion and mutual recursion.
- **Library API**: `Parser` (`Parse`, `ParseAll`), `Result.Compile`,
  `ParseSources`, and the `Source`/`Result`/`Decl` types. Top-level
  `var`/`const` collection is opt-in via `AllowTopLevelVar` /
  `AllowTopLevelConst`.
- **CLI** (`cmd/functy`): `functy run` (entry function, argument evaluation,
  `json`/`hcl`/`raw` output, top-level const/var resolution) and
  `functy check` (parse + validate with source-located diagnostics), over a
  baseline context of the cty standard library plus `print`/`println`.
- Documentation, examples, and CI.

The library depends only on `github.com/hashicorp/hcl/v2` and
`github.com/zclconf/go-cty`.

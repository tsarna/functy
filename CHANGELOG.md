# Changelog

All notable changes to functy are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and the project will follow [Semantic Versioning](https://semver.org/) once
tagged. Until then, everything lives under **Unreleased**.

## [Unreleased]

### Added

- **`eventually` / `never` — test-only polling assertions.** Two builtins for
  testing state that settles asynchronously, injected into each test's eval
  context alongside `skip` (absent from `functy run`/`check`).
  `eventually(cond, timeout, interval?)` re-evaluates `cond` until it holds,
  failing — like `assert`, with the condition's source range and operand values —
  if it never did within `timeout`; `never(cond, timeout, interval?)` is the
  inverse, failing the instant `cond` becomes true. `timeout`/`interval` are
  Go-parseable duration strings (`"250ms"`, `"1s"`) or a number of seconds
  (`interval` defaults to 10ms). The condition is captured lazily (like `assert`)
  so it can be re-evaluated each poll; it observes change only when it re-reads
  state that mutates between evaluations (a host function or a mutable capsule),
  not a plain immutable local. See
  [doc/language.md](doc/language.md#tests). The shared assert-failure builder was
  factored out as `assertionError`.

## [0.11.0] - 2026-07-18

### Added

- **Namespace-scoped and private type aliases.** A `type` alias is now scoped to its
  file's namespace with **own-then-global** resolution, matching functions and consts:
  an alias declared in `namespace foo` is seen by foo's annotations first, then falls
  back to the global (unnamespaced) aliases and host-registered types. Two files in
  different namespaces may each declare `type Id = …` without colliding; a duplicate
  *within* a namespace is still an error. A namespaced alias shadows a same-named
  global alias silently and a host-registered type with a **warning** (the enforcement
  silently changes from identity to structural); a built-in keyword stays reserved in
  every namespace. A leading-underscore alias (`type _spec = …`) is namespace-local: it
  resolves and inlines into other aliases of its namespace, but a consumer projecting an
  export surface — the `symbols` library and `functy symbols` — withholds it via the new
  `TypeAlias.IsPrivate()`. `TypeAlias` gains a `Namespace` field; `type _` (the blank
  identifier) is now rejected. See
  [doc/language.md](doc/language.md#type-aliases). Previously aliases were project-scoped
  in one flat table, so cross-namespace names collided and privates could not be withheld.

- **Namespace-scoped consts/values.** The `functy` CLI/REPL and the `symbols`
  library now resolve a namespaced file's top-level `const`/`var` within its own
  namespace, instead of the single flat unit-global table used before. Two namespaces
  may each declare the same bare name without colliding, and a function resolves a
  bare name the way it resolves a bare call — its own namespace first, then the global
  (unnamespaced) names, local winning — so an author writing `const greeting` in
  `namespace foo` gets `symbols.foo.greeting`, independent of `namespace bar`'s. There
  is still no qualified *spelling* for a value (`foo::bar::x` is a parse error), so a
  namespace's consts are reachable only from within that namespace or through a
  per-namespace host projection.

  This is a **host policy, not a language change.** functy attaches each declaration's
  namespace as metadata but evaluates nothing and buckets nothing. Embedders get a new
  `Compiled.Vars`: a map, handed back empty, that each namespace's function bodies read
  their bare variables from (`Vars[ns]`) — the variable-scope partner of
  `Compiled.Units` for functions. A host fills it by whatever policy it wants; the new
  `EvalNamespacedDecls` helper implements the own-plus-global resolution above, while a
  plain `EvalDecls` into one table keeps the flat behavior. See
  [doc/language.md](doc/language.md#namespace-scoped-consts-and-values).

- **Defaulted optional object attributes — `optional(T, default)`.** An optional
  object attribute may now carry a default value:
  `object({ retries = optional(number, 3) })`. When the attribute is absent (or
  explicitly null), coercing the value fills it with the default — recursively, from
  the inside out for nested objects — matching Terraform/OpenTofu optional-attribute
  defaults. The default is a literal expression and must be convertible to the
  attribute's type. Previously only the no-default `optional(T)` form was accepted.
  functy builds the defaults tree in its own resolver but reuses
  `hcl/v2/ext/typeexpr`'s `Defaults.Apply` at runtime, so the semantics match
  Terraform's exactly. Type signatures round-trip the default
  (`optional(T, <literal>)`). See
  [doc/language.md](doc/language.md#types).

- **Execution limits (Tier 1) — a per-invocation step budget.** functy is a
  tree-walking interpreter, so an unbounded `for` / `while` could wedge the host.
  `Parser.MaxSteps(n)` now caps the number of steps any single function invocation
  may take (one per statement executed, plus one per loop iteration); `0` (the
  default) means unbounded, so existing embeddings are unchanged. The ceiling is
  captured immutably at compile time into each function's `Impl`, so the counter is
  per-frame and needs no shared state. See
  [doc/language.md](doc/language.md#execution-limits).

  - A breach raises an **uncatchable** `*LimitError`: `try` / `catch` and
    `val, err = …` re-propagate it rather than handle it (otherwise
    `try { while true {} }` would fire the guard, unwind into the catch, and loop
    again), and `defer`s / enclosing `finally` blocks do not run as it unwinds. The
    breach terminates the whole evaluation, surfacing to the host as a Go
    `*LimitError` (which also renders as source-underlined diagnostics).
  - The budget is **per-frame**, so it bounds a single function's runaway loop but
    **not** recursion (each nested call starts a fresh count) nor work aggregated
    across many small calls — and it is cooperative, so a single long-running host or
    cty-stdlib call runs to completion regardless. Those are Tier 2.
  - The `functy` CLI exposes it as a persistent `--max-steps` flag (generous
    default; `0` disables). `functy test` bodies are bounded too, so a runaway loop in
    a test fails with a `*LimitError` rather than hanging.
  - API change: `BuildFunction` takes an additional `maxSteps int` argument.

### Documentation

- **Documented the open-type host footgun.** An open type (`RegisterOpenType`)
  validates only its predicate and passes the value through unchanged — it asserts
  nothing about the value's Go representation. A host that later type-asserts such a
  value while trusting a predicate looser than its consuming code assumes can be
  handed an unexpected representation and panic. `RegisterOpenType`,
  `predicateConstraint`, and the language guide's open-types section now spell this
  out and point to `RegisterType` (identity) for when a specific capsule type is
  required.

### Fixed

- **Const/var and type-alias resolution are no longer quadratic.** Both resolvers
  used a rescan-until-fixpoint that re-walked every declaration's referenced-name
  set on every pass, so a reverse-ordered dependency chain (`const c0 = c1+1; …; cN
  = 0`, or `type T0 = list(T1); …`) resolved one item per pass — O(n²), tens of
  seconds on a few-hundred-KB file. They now share one worklist topological sort
  (Kahn's, O(n+e)), computing each dependency set once; a 16 K-declaration chain
  drops from ~9 s to ~0.1 s. Ordering, duplicate/cycle reporting, self-reference
  handling, and error-cascade behavior are unchanged.

- **The unterminated-string lexer resync is no longer quadratic.** Recovering from
  an unterminated single-line string re-lexes the entire remaining suffix (HCL stays
  in string mode to end-of-file), so a file of K such strings was Θ(K²) — tens of
  seconds from a ~50 KB buffer, and an editor re-lexing on every keystroke would
  wedge. The number of resyncs is now capped (100); past it the remainder is lexed
  once and appended, making `lexAll` linear. A legitimate mid-edit buffer has only a
  handful of half-typed strings, far below the cap, so normal recovery is unchanged.

- **Parse diagnostics are capped so a malformed file can't wedge the host.** A file
  with an error per token produced one diagnostic per error, and rendering each
  re-scans the source (hcl's diagnostic writer is ~O(n) per diagnostic), so an
  uncapped flood cost ~O(n²) wall-clock — tens of seconds from a small file,
  reachable from flat (depth-0) input that the nesting-depth cap does not touch.
  `Parse`/`ParseAll` now return at most 200 diagnostics plus a "Too many
  diagnostics" summary of how many were suppressed. The cap lives in the library
  (not the CLI's diagnostic writer), so every embedding host that renders
  diagnostics itself is protected.

- **Parse-time nesting-depth caps prevent an uncatchable stack-overflow crash.**
  Deeply nested input — a file of a million `{`, or an expression/type/alias with
  ~100 K nested parens or `list(list(…))` — recursed until Go's goroutine stack
  overflowed, which `recover` cannot catch, killing the host (reachable via `check`,
  `fmt`, `run`, and any library `Parse`/`Format`). The recursive-descent statement
  parser now caps `{ … }` nesting (2000) with a single diagnostic and a clean
  unwind, and every expression/type span is checked for bracket depth (500) before
  being handed to HCL's own unbounded-recursive parser — including the alias-RHS
  pre-parse pass. A too-deep input is now a "Nesting too deep" diagnostic instead of
  a crash; realistic nesting is unaffected. (Runtime recursion — a self-recursive
  functy function — is a separate, still-open item.)

- **`for … in` no longer double-copies the collection before iterating.** Ranging
  over a collection materialized the entire thing into a second slice of key/value
  pairs *before* the loop began, so the per-element step-limit checkpoint could not
  fire until after that full copy — a large collection (e.g. one returned by a host
  function) was allocated twice before any budget check. The loop now iterates the
  collection's element iterator directly, ticking per element, so nothing is
  pre-materialized and the step limit applies as the loop proceeds. Range semantics
  (list/tuple index key, set running-counter key, map/object key) are unchanged.

- **Running tests no longer mutates the caller's eval context.** `RunTests` /
  `RunTestsMatching` injected the test-only `skip` builtin by writing it into the
  caller's shared `Functions` map (and restoring it afterward). Because compiled
  functions late-bind to that same context, the write raced with any concurrent
  evaluation using it — a potential `concurrent map read and map write` crash. The
  `skip` builtin is now layered into a private child context that parents each test
  body, so it still resolves throughout the call graph while the caller's map is
  never touched.

- **Panic backstops on the parse, format, and reflection paths.** A Go panic on a
  code path outside a compiled function (which cty's `Function.Call` recovers)
  would previously propagate straight into the host — killing an editor/LSP
  re-parsing a buffer. Three defense-in-depth recoveries were added: `Parse` /
  `ParseAll` convert an unexpected panic into an "Internal parser error"
  diagnostic and a usable Result; `Format` returns the source unchanged (never
  corrupting the file) plus an "Internal formatter error"; and `help()` reflecting
  over a host function whose return-type callback panics now renders the signature
  without a return type instead of crashing. These do **not** catch a stack
  overflow (`recover` cannot) — guarding parser recursion depth remains separate
  work.

- **"Unreachable code" now underlines the whole statement.** An unreachable
  assignment or expression statement carried a source range covering only its first
  token — just the target name for `x = compute()`, just the leading token for a
  bare call — so the "Unreachable code" diagnostic underlined a fragment instead of
  the statement. Both node kinds now span the full statement.

- **A negative execution-step limit no longer silently means "unbounded".** The
  interpreter treats any non-positive `MaxSteps` / `--max-steps` ceiling as
  unbounded (0 is the documented "unbounded"), so a negative value — a host
  intending a tight bound, or a `--max-steps=-1` typo — silently disabled the
  primary runtime safety control. Parsing now emits a "Negative execution-step
  limit" warning and falls back to unbounded, so the mistake is surfaced instead
  of hidden.

- **Extern overload collisions are no longer bypassed by whitespace.** The
  "Duplicate extern signature" check keys each overload form on its parameter
  types' rendered form. An unregistered (opaque) named type — the common case in
  an extern, which names its *host's* types — was rendered verbatim from source, so
  two forms differing only in insignificant whitespace (`func f(x: list(ctx))` vs
  `func f(x: list( ctx ))`) got different keys and both were accepted. Opaque type
  names are now canonicalized (via `hclwrite`, which preserves whitespace inside
  string literals) before they key the check and before `help()` renders them, so
  a whitespace variant of a copy-paste duplicate is caught.

- **Duplicate attribute names in an object type are now an error.** An annotation
  like `object({ a = string, a = number })` was silently accepted: the resolver
  overwrote the map entry so the later attribute won and the earlier one was
  dropped, turning a simple typo into a quiet change of the declared shape.
  `resolveObjectType` now rejects a repeated attribute name with an "Invalid object
  type" diagnostic (`duplicate attribute "a".`) subjected to the offending key.

- **Non-boolean and unknown conditions no longer panic.** Every condition site (`if`,
  `while`, three-clause `for`, an expression-less `switch` case, a `catch … if` guard,
  and the stdlib `cond()` / `assert()` builtins) called `cty.Value.True()` after only a
  null check. That panics ("not bool") on any non-Bool value and on an unknown Bool, so
  an ordinary type mistake like `if 5 { }` — or a condition referencing a host-provided
  unknown value — leaked a Go stack trace ("panic in function implementation: not bool")
  instead of a diagnostic (and would be a real crash on any path that drives the
  interpreter outside `cty.Function.Call`, which today recovers it). A shared `condBool`
  helper now converts the value to Bool and returns a clean diagnostic: null → "Null
  condition", non-boolean → "Non-boolean condition", unknown → "Unknown condition"
  (pointing at the offending expression). The switch subject-match test is guarded the
  same way, so an unknown operand yields a clean error rather than a panic. (An unknown
  condition currently errors; propagating it as an unknown result is left as a possible
  future refinement.)

- **`functy fmt` no longer corrupts heredoc string values.** The formatter re-indents
  the continuation lines of every reformatted expression, but did not exclude heredoc
  *body* lines — whose bytes are literal string content. A body-level `<<EOT` heredoc
  had the current indent silently prepended to every line of its value, and the damage
  compounded on each run (so `fmt` was neither meaning-preserving nor idempotent, and
  `fmt -w` — or format-on-save — wrote the drift to disk). The formatter now lexes each
  formatted expression fragment and emits every heredoc body line and its closing marker
  verbatim; only the opener line (`<<EOT` / `<<-EOT`) is re-indented. This holds for the
  indent-stripping `<<-EOT` form too (where the churn broke idempotency without changing
  the value), for multiple and nested heredocs, and for heredocs inside a larger list,
  object, or call-argument expression.

## [0.10.0] - 2026-07-15

### Added

- **Namespaces and `_` visibility — unit-scoped names.** A `.cty` file may open
  with a `namespace acme::math` declaration; its functions are registered with the
  host under their qualified names (`acme::math::double`), while the namespace's
  own functions go on calling each other by their bare names. A declaration whose
  name begins with an underscore is **namespace-local**: compiled and callable from
  within its namespace, but never handed to the host — so a file can finally keep a
  helper to itself instead of publishing it into the host's shared function
  namespace. See [doc/language.md](doc/language.md#namespaces-and-visibility).

  - A namespace name is one or more `::`-separated identifiers (`namespace foo` is
    as valid as `namespace foo::bar`). Nesting is a **naming convention, not
    containment**: `foo::bar` is not "inside" `foo`, gets no special visibility into
    it, and must call `foo::helper()` in full.
  - A namespace **spans files**: two files declaring the same namespace share one
    unit and see each other's functions, private ones included. This is why `_` means
    *namespace*-local rather than *file*-local.
  - **Imports are deliberately not part of this.** A namespace is usable the moment
    it exists, because a caller can always spell the fully-qualified name.
  - `namespace` is a *contextual* keyword, like `test` and `type`, so
    `func namespace(...)` keeps working.
  - Because `::` is a function-call selector in HCL, only functions can be
    qualified. Top-level `const`/`var` carry their namespace as metadata for the host
    to interpret; type aliases remain project-scoped.
  - New API: `(*Result).CompileUnits` returns a `Compiled{Funcs, Units}` — the host
    map (exported, qualified) and the per-namespace resolution layers (bare, privates
    included). `functy.Qualify` joins a namespace and a bare name.
  - CLI: `run --func` resolves a bare name declared in exactly one namespace (so
    `functy run file.cty` keeps working when a file gains a namespace, and
    `--func _helper` can exercise a private one), and reports an ambiguous name
    rather than guessing. `symbols` gains a `kind: "namespace"` symbol for the
    declaration plus additive `namespace` / `qualified` / `private` fields on each
    declaration — all omitted in the global namespace, so a file without a
    `namespace` declaration produces byte-identical output to before.

- **Shadowing warning.** A namespaced function whose bare name matches a baseline
  built-in shadows it *inside that namespace* (local wins). Namespacing otherwise
  disarms the reserved-name check — `acme::text::upper` collides with nothing in the
  host's map — so `run` and `check` now emit a warning where the host's function set
  is known. A library host can do the same by comparing `Compiled.Units` against its
  own registry. The equivalent clash in the global namespace remains a hard error.

- **Extern declarations — real signatures for host-provided functions.** A `.cty`
  file whose leading comment block carries `//functy:extern` holds bodiless `func`
  declarations describing functions the *host* provides. They are never compiled and
  never callable; they exist so `help()`, `functy symbols`, and editor tooling can
  show a host function's true signature — which its cty metadata generally cannot.
  See [doc/language.md](doc/language.md#extern-declarations).

  A cty function fakes optional and defaulted arguments with a trailing `VarParam`,
  erasing their names and defaults; and since a cty optional parameter may only sit
  at the *tail*, the common `f([ctx,] x)` convention (a leading context sniffed out
  of `args[0]`) cannot be expressed at all. Such a function reflects as
  `f(x, ...args)`. An extern says what it actually is.

  - New parameter syntax **`name?`** — optional with *no* default, the only way to
    spell an optional *leading* parameter. Legal only in an extern file (where
    nothing is compiled), mutually exclusive with `= default`, and it relaxes the
    required-before-optional ordering rule there.
  - `//functy:extern` is a **file-level directive rather than a keyword**,
    deliberately: it keeps a bodiless `func` a hard error in every other file, so a
    half-typed declaration stays a syntax error instead of silently becoming a valid
    one.
  - An extern file may hold a `namespace` declaration and nothing else but externs:
    `var`, `const`, `test`, `type`, `_`-prefixed names, and bodies are all rejected.
  - A named type an extern mentions that the reader has not registered (`ctx`,
    `time`, …) resolves to an **opaque** name — carried for rendering, unenforced,
    and warned about once per file. An extern names its *host's* types, and whoever
    reads it (the CLI, an editor) usually is not that host, so failing there would
    make extern files unusable exactly where they are most useful.
  - Declaring one name twice (an overload set) is not supported yet and is an error.
    `Result.Externs` is a **slice**, not a map, precisely so that allowing it later
    is a relaxation of that check rather than an API change.
  - New API: `Result.Externs []*FuncDecl`, `FuncDecl.Extern`, `FuncDecl.SigRange`
    (an extern has no body, so this is its only end position), `Param.Optional`.
  - CLI: `symbols` gains `kind: "extern"`; `help()` prefers an extern over the
    cty-metadata fallback for the same name; `fmt` round-trips extern files.

- **`Parser.RegisterExterns` — a host loads a package's extern declarations.** A leaf
  package `go:embed`s its `//functy:extern` file and exposes it as opaque bytes, never
  importing functy; the host calls
  `parser.RegisterExterns(pkg.Externs(), pkg.ExternsFilename)`. Zero coupling in both
  directions, and because the registered bytes are a real `.cty` file, `functy fmt`,
  `functy symbols`, and an editor can open it standalone. Registration *verifies* the
  `//functy:extern` directive rather than forcing the mode, so one byte string means one
  thing however it is loaded.

  - Host externs land on **`Result.HostExterns`**, deliberately separate from
    `Result.Externs`. A tool that renders *a source* — `fmt`, `symbols`, an outline —
    iterates `Externs`, and gets the right answer by default because it cannot reach
    `HostExterns` without naming it. Merging the two would let `fmt` on a user's file emit
    another package's declarations into it, and (worse) splice the host file's comments at
    byte offsets belonging to the user's source.
  - `checkExternNames` now covers both sets, so a host extern colliding with another host's,
    with a file extern, or with a user's function is reported by name.
  - `Parser.ExternSources` lets a host seed its diagnostic file map, so a diagnostic pointing
    into an embedded extern still renders with a source snippet.

- **Overload sets — one extern name, several signatures.** Some host functions are not one
  function with optional parameters; they are several functions sharing a name, and no single
  signature describes them honestly. `parsetime(s)` parses a timestamp while
  `parsetime(format, s)` parses a *format* and then a timestamp — written as
  `parsetime(format?, s)` it would be a lie. Declare each form instead:

  ```functy
  // Parse a timestamp.
  func parsetime(s: string) -> time
  func parsetime(format: string, s: string) -> time
  func parsetime(format: string, s: string, tz: string) -> time
  ```

  Each form carries its own return type, which is what makes a function whose result type
  depends on its arguments (`timeadd`, `timesub`) sayable at all — cty cannot express that
  even in principle.

  - This needed **no new syntax and no API change**: `Result.Externs` was made a slice for
    exactly this, so two declarations of one name were already two elements.
  - The forms of one name must live in **one file**. The same name across two files is a
    collision — two packages both claiming `get` — not an overload set. Two forms of the same
    *shape* (arity, parameter types, optionality) are a copy-paste, and an error; names and
    docs do not distinguish a call, so they do not distinguish a form.
  - `help()` lists every form, then the documentation, then one `Parameters:` section unioned
    across the forms.
  - Extern-only: a real functy `func` still declares one signature, and a duplicate name is
    still a duplicate function.

- **An extern may name a host type nested inside a collection.** A bare unregistered type in
  an extern already stands in as an opaque name (`ctx`, `bytes`), so the reader need not have
  registered it; the same now holds one level down — `list(geopoint)`,
  `object({ at = geopoint })` — where a nested position previously demanded a concrete cty
  type an open or unregistered name has none of, and failed to parse. An extern documents
  rather than enforces, so the annotation is taken opaquely from its source text: it renders
  as written and enforces nothing. A genuinely malformed constructor is still an error, and
  a nested name the host *did* register degrades silently, with no spurious warning. (Full
  nested-open-type *enforcement* remains future work; this is the extern-only slice of it.)

### Fixed

- **A single-line function's parameters no longer take the function's doc comment.**
  `docComment` looks for a block directly above a declaration, and on a single-line
  signature every parameter shares the `func` line — so each parameter was documented with
  the *function's* doc, repeating the whole comment once per argument in `help()`. Parameter
  docs are a feature of the multi-line layout, and now say so: a parameter takes
  documentation only when it starts its own line, which is the rule trailing param comments
  already followed.

- **`help()` renders a host function's return type.** It asks the cty function what it
  returns when called with its declared parameter types, so a Go builtin with complete
  metadata now renders as fully as a declared one (`durationround(d: duration, m: duration) ->
  duration`). Best-effort by nature: a function whose return type is computed from its
  *arguments* cannot answer without them — and that is exactly the kind that needs an extern,
  where each form states its own return type.

- **The REPL renders a multi-line string as a heredoc** rather than one
  backslash-escaped line. `help()` returns a multi-line block, so it was previously
  unreadable in the REPL — the reflection built-in and the REPL disagreed about what a
  string is for. Nested strings keep quoting, since a heredoc is not grammatical
  mid-expression.

- **Default-parameter expressions are now evaluated against the same context as the
  function body.** They were evaluated against the host context directly, bypassing
  the interpreter's context construction; a default like `= _helper()` could not see
  the function's own namespace. Fixed as a consequence of the namespace work.

- **A comment after a type annotation is no longer captured into it.** The source
  text recorded for a type ran to the *stopping token*, and comments are not tokens,
  so a comment between the annotation and the end of the line was swallowed into the
  rendered annotation — and then emitted a second time by `fmt` as a trailing
  comment, growing by one copy per run. Latent for a function with a body (whose
  signature stops at `{` on the same line); found by the extern work, whose
  signatures end at the newline.

- **`fmt` no longer duplicates a comment written *inside* a type annotation.** A comment
  in an object type — `object({ a = number, // count\n})` — is part of the type's source
  text and rendered verbatim, but the comment cursor did not know that and emitted it a
  second time as a dangling comment before the `)`, so the file grew by one copy on every
  run. A separate defect from the one above (comments *after* the annotation); this one is
  *inside* it. Both are now covered by the formatter's idempotency check.

- **`help()` renders type constraints in functy's own grammar**, not cty's prose
  `FriendlyName`. A parameter or return type now shows as `list(string)` and
  `object({ a = string })` (with `optional()` markers where present) rather than
  `list of string` and a bare `object` that hid its attributes — the same round-trippable
  syntax `typeof()` emits. Applies to declared functions and to the cty-metadata fallback
  for host functions.

- **`typeof`/`typekind` no longer hide their return type, and every stdlib builtin is
  documented.** The two take a value of any type and return a string, but a dynamic
  argument poisoned the reflected return type to dynamic, so `help()` showed no return at
  all; the parameter now opts into `AllowDynamicType` so the string return stays visible.
  Every standard-library function — including the lazy `cond`/`switch`/`try`/`error`/
  `assert` and the `help`/`doc` reflection builtins — now carries a parameter description,
  so `help()`/`doc()` describe them in full.

### Changed

- **BREAKING (library):** `_`-prefixed top-level functions are no longer included in
  the map returned by `(*Result).Compile`. They were previously exported like any
  other function; they are now namespace-local. A host that relied on calling one
  should rename it.
- **BREAKING (library):** keys in `(*Result).Compile`'s map may now contain `::`, for
  functions declared in a namespace. HCL resolves such a name as a single flat map
  key, so a host that merges the map into an eval context needs no change.
- **BREAKING (library):** `HelpFunc` takes the whole `*Result` rather than
  `[]*FuncDecl`, so that it sees `Result.Externs` as well as `Result.Funcs` — and so
  that any future set added to `Result` needs no further signature change. Update
  `functy.HelpFunc(res.Funcs, evalCtxFn)` to `functy.HelpFunc(res, evalCtxFn)`;
  passing `nil` remains valid.

## [0.9.0] - 2026-07-11

Promotes `0.9.0-rc.3` to the final **0.9.0** release, adding the parser/lexer
recovery fix below on top of the editor-facing CLI work landed across the rc
series (`symbols`, `eval`, machine-readable `--json` diagnostics, stdin
type-checking, `version --json`). See the `0.9.0-rc.*` sections for the full set
of changes since 0.8.1.

### Fixed

- **Parser/lexer error recovery no longer lets one mid-edit typo swallow the
  rest of the file** — so `symbols`/`check`-backed editor tooling (outline, test
  discovery, on-type diagnostics) keeps working through transient breakage:
  - An **unterminated `{`** (`func f() {` with no closing brace) used to absorb
    every following declaration into the runaway body. `parseStatements` now
    treats a `func` keyword at statement position as an unambiguous "a block was
    left open" signal (closures are a non-goal, so `func` can't appear there) and
    stops, letting the following functions parse.
  - An **unterminated quoted string** (`return "oops` with no closing quote) used
    to turn the entire remainder of the file into string content. `lexAll` now
    resynchronizes at the offending newline (HCL's `TokenQuotedNewline`) and
    re-lexes the rest, so later declarations tokenize normally.

## [0.9.0-rc.3] - 2026-07-10

### Changed

- **`functy symbols` now defaults to a human-readable listing**, with `--json`
  for the machine-readable object — consistent with `check` / `test` / `run` /
  `version` / `eval` (default human, `--json` opt-in). The default prints one
  `file:line: kind name` line per symbol; `--json` emits the same object as
  before. (In rc.2 `symbols` was JSON-only with no flag.)

## [0.9.0-rc.2] - 2026-07-09

### Added

- **`functy symbols`.** Emit every top-level declaration (`func`, `const`, `var`,
  `type`) and `test` block, in source order, as a JSON object on stdout — kind,
  name, a function's rendered signature, any doc comment, and the 1-based
  definition range (the full block for `func`/`test`). Input is handled like
  `check` (files/dirs, the current directory, or stdin via `-` + `--filename`),
  and parse errors are tolerated so an editor can outline a file mid-edit. Backs
  the VSCode extension's outline and test discovery. See `doc/cli.md#symbols`.

## [0.9.0-rc.1] - 2026-07-09

A 0.9.0 release candidate for testing the VSCode extension, bundling the
editor-facing CLI additions since 0.8.1.

### Added

- **`functy eval EXPR [FILE...]`.** Evaluate a single HCL expression against the
  context built from the given files (functions, consts, types); zero files is
  allowed (baseline context only). The result is printed to stdout in the
  `--output` format (`hcl` by default, or `json` / `raw`); with `--json`, any
  diagnostics go to stderr and the command exits non-zero on error. Unlike using
  the REPL as an eval backend, `eval` is a one-shot with a reliable exit status
  and no banner on stdout — the clean backend for an editor's "evaluate
  selection". See `doc/cli.md#eval`.
- **`functy version --json`.** Emit version, commit, date, and Go toolchain
  version as a single machine-readable object for editor tooling. The text
  output is unchanged. See `doc/cli.md#version`.

### Changed

- **`check` now discovers directories and the current directory, and can read
  stdin.** A directory argument is walked recursively for `.cty` files, and with
  no arguments the current directory tree is checked — consistent with `test`
  and `fmt`, so `functy check` checks a whole project. A single `-` reads one
  buffer from stdin; pair it with `--filename NAME` so diagnostics carry a real
  path, letting an editor type-check an unsaved buffer without writing to disk.
  See `doc/cli.md#check`.

## [0.8.1] - 2026-07-08

### Changed

- **`check --json` and `test --json` now emit their report to stderr** (not
  stdout), matching `run --json`. This makes the rule uniform across every verb —
  the machine-readable JSON report goes to stderr; stdout is reserved for program
  output. It also fixes a real bug: a `test` block calling `print` / `println`
  wrote to stdout and corrupted the JSON report there. Editor tooling that consumed
  `check`/`test --json` from stdout must read stderr instead. (The reports
  introduced in 0.8.0 had not yet been consumed.)

## [0.8.0] - 2026-07-07

A developer-experience release focused on editor tooling and the interactive
REPL: machine-readable diagnostics from `check` and `run`, zero-argument test
discovery, and function introspection (`help`/`doc`) in the standalone CLI.

### Added

- **`functy check --json`.** The `check` command gains a `--json` flag that
  emits diagnostics as a machine-readable report to stdout —
  `{ diagnostics: [ { severity, summary, detail?, location? } ] }`, each
  `location` a 1-based `file`/`line`/`column`/`end_line`/`end_column` range —
  instead of the human-readable text writer. A clean file yields an empty array,
  and the exit status is unchanged, so editor tooling (the VSCode extension's
  Problems panel) can map diagnostics to precise ranges without scraping text.
  See `doc/cli.md#check`.
- **`functy run --json`.** `run` gains the same `--json` diagnostics report
  (identical shape to `check --json`), covering compile, argument, and runtime
  (`throw` / failed `assert`) errors. The report goes to **stderr** — `run`'s
  stdout is reserved for the program's own `print`/`println` output and the
  return value, both left untouched by `--json` — so editor **Run** commands can
  surface runtime failures at precise ranges without disturbing program output.
  See `doc/cli.md#run`.
- **`functy test` no-argument discovery.** Run with no `FILE` arguments, `test`
  now discovers `.cty` files in the current directory (recursively, skipping
  dot-directories) — equivalent to `functy test .` — instead of erroring. Makes
  the terminal loop and a VSCode Test Explorer "run all" cheaper. See
  `doc/cli.md#test`.
- **`help()` / `doc()` in the standalone CLI & REPL.** The reflection builtins are
  now wired into the `run` / `repl` / `run -i` context, so a `.cty` author (and the
  interactive REPL) can introspect available functions without a host: `help(name)`
  renders a function's signature and per-parameter docs, `doc(name)` its
  description, and — new — `help()` with no argument returns the sorted names of
  every available function. `help` and `doc` are reserved names in the CLI. See
  `doc/stdlib.md` and `doc/cli.md#repl`.

## [0.7.3] - 2026-07-07

### Added

- **Homebrew cask.** macOS users can now install the CLI with
  `brew install tsarna/tap/functy`. The release publishes a cask to the
  `tsarna/homebrew-tap` tap; it strips the Gatekeeper quarantine attribute on
  install so the unsigned binary runs without prompting.

## [0.7.2] - 2026-07-07

### Fixed

- **Release workflow populates the GitHub Release body.** The 0.7.1 pipeline
  left the release body empty because GoReleaser's `--release-notes` is ignored
  when changelog generation is disabled. The workflow now sets the body from the
  changelog section via `gh release edit` after GoReleaser publishes.

### Changed

- Bumped `goreleaser-action` to v7 (runs on Node 24, clearing the Node 20
  deprecation warning).

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

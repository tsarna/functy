# Changelog

All notable changes to functy are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and the project will follow [Semantic Versioning](https://semver.org/) once
tagged. Until then, everything lives under **Unreleased**.

## [Unreleased]

### Added

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

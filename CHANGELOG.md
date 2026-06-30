# Changelog

All notable changes to functy are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and the project will follow [Semantic Versioning](https://semver.org/) once
tagged. Until then, everything lives under **Unreleased**.

## [Unreleased]

### Added

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

### Notes

- Nesting a named type inside a collection or structural type (`list(bus)`,
  `object({ b = bus })`) is recognized as designed but **not yet implemented**;
  it is reported as an error in nested position.

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

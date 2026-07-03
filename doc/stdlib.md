# functy standard library

functy ships a small, dependency-free standard library: host-agnostic builtins that
make **HCL expressions** more capable than raw HCL. They are ordinary
`cty.Function` values, so a host merges them into its eval context alongside the cty
stdlib and its own functions — which means they are available **anywhere the host
evaluates expressions**, not only inside functy function bodies. (Inside a functy
function most overlap with statements like `if`/`switch`; that's fine — their home is
expression position, e.g. a host config attribute or a `return`.)

```go
import "github.com/tsarna/functy"

ctx.Functions = merge(
    ctyStdlib,          // the cty standard library
    functy.Stdlib(),    // typeof, typekind, cond, switch, error, assert
    functy.StdlibExtras(), // try, can   (opt-in)
    hostFunctions,      // your own
    compiledFuncs,      // functions compiled from .cty sources
)
```

## `Stdlib()` — the default set

### `typeof(v) -> string`

A value's type in functy's own **annotation grammar** — the same syntax you write in
declarations, so it round-trips: `"string"`, `"number"`, `"list(string)"`,
`"map(bool)"`, `"object({ a = string, b = number })"`, `"tuple([string, number])"`. A
capsule renders as its type name; a bare `null` (no concrete type) is `"any"`. A
**rich object** — a `cty.Object` that carries its capsule under a `_capsule` (or a
context's `_ctx`) attribute — is named by that capsule (`"bytes"`, `"ctx"`) rather
than by its internal fields.

### `typekind(v) -> string`

Just the top-level **kind** — `"string"`, `"number"`, `"bool"`, `"list"`, `"set"`,
`"map"`, `"object"`, `"tuple"`, `"any"`, or a capsule's name — dropping element and
attribute detail. Use it for dispatch where `typeof`'s full form is too specific:

```functy
switch(typekind(x), "list", …, "object", …, "…")   // "is it a list?" not "list(string)?"
```

### `cond(c1, r1, c2, r2, …, else)`

A **lazy**, multi-branch conditional. Conditions are evaluated in order; only the
result paired with the first true condition is evaluated, and if none is true, the
trailing `else`. Unlike HCL's `c ? a : b` — which evaluates **both** arms — `cond`
evaluates only the selected branch, exactly once, so side effects and errors in
unselected branches never happen:

```functy
cond(n > 0, "positive", n < 0, "negative", "zero")
cond(user == null, error("login required"), user.name)   // error() only if null
```

Requires an odd number of arguments (≥ 3).

### `switch(on, v1, r1, v2, r2, …, default?)`

Lazy value dispatch. `on` is evaluated once; each `vN` is compared to it for exact
equality (`RawEquals`), and the first match's `rN` is returned. An optional trailing
`default` handles no-match; without one, no match is an error. Only the values up to
the match, and the single selected result, are evaluated.

```functy
switch(status, 200, "ok", 404, "missing", "other")
```

> Because `switch` is also a statement keyword, the `switch(...)` builtin is only
> usable in **expression position** — `x = switch(...)`, `return switch(...)`, or as
> an argument — not as a bare statement (which parses as a `switch` statement).

### `error(v)`

Raise an error from expression position — the expression form of the `throw`
statement. Accepts a string or an object (like `throw`), composes with `try`/`catch`
and `val, err =`, and carries a source `range`:

```functy
coalesce(config.host, error("host is required"))
cond(code >= 500, error({ message = "server error", code = code }), body)
```

### `assert(cond, message?)`

Raise a catchable error when `cond` is false — a runtime check in expression
position. The condition is received unevaluated, so the error carries the
**condition's** source `range` (a surfaced diagnostic underlines exactly what
failed); on success `assert` returns `true`. The optional `message` — a string or an
object, exactly like `error()`/`throw` — is itself lazy, evaluated **only on
failure**; without one the message is `"assertion failed"`. It composes with
`try`/`catch` and `val, err =`, and a condition that itself fails to evaluate
propagates that error (a structured throw survives) rather than reporting an
assertion failure.

```functy
assert(n > 0)                                   // -> error { message = "assertion failed", range, … }
assert(port > 0, "port must be positive")       // string message
assert(ok, { message = "denied", code = 403 })  // object message, like error()

var _ok
var err: error
_ok, err = assert(valid(x), "invalid x")        // capture instead of unwinding
```

On failure, `assert` also reports **why** by capturing the values of the variables the
condition references (pytest-style). The error gains two attributes:

- `detail` — a rendered string, e.g. `"n = -3"` or `"a = 1, b = 5"`.
- `operands` — a list of `{ name, value }` with the **raw** values (so a `catch`
  clause can inspect them programmatically), deduped by name.

```functy
try {
    assert(n > 0 && n < limit, "out of range")
} catch e {
    log(e.message)              // "out of range"      (message stays the headline)
    log(e.detail)               // "n = 42, limit = 10"
    log(e.operands[0].value)    // 42                   (a real number)
}
```

Only the referenced **variables** are captured — via `expr.Variables()`, which reads
already-bound values and **never re-runs function calls**, so gathering operands is
side-effect-free. Consequently `assert(len(xs) > 3)` reports `xs`, not `len(xs)`; a
condition that references no variables (`assert(1 > 2)`) attaches no `operands` /
`detail`.

When an assertion (or any error) is **uncaught** and reaches the host, it can be
rendered with source context — the failing line plus the operand `detail` — through
`functy.ErrorDiagnostics(value)` / `(*functy.ThrownError).Diagnostics()`, which feed
the standard `hcl.NewDiagnosticTextWriter`. The `functy run` CLI does this:

```console
Error: must be positive

  on prog.cty line 2:
   2:     assert(n > 0, "must be positive")

n = -3
```

## `StdlibExtras()` — opt-in

Kept separate because their names collide with HCL's stock `tryfunc`; a host opts in
explicitly so it doesn't silently override an existing `try`/`can`.

### `try(e1, e2, …)`

Returns the first argument that evaluates without error; errors only if all fail.
Each argument is evaluated **at most once** (unlike stock HCL `try()`, whose type
inference evaluates the winning branch twice — unsafe for side effects). Handy for
defaults:

```functy
try(env.PORT, "8080")
try(jsondecode(s), {})
```

### `can(e) -> bool`

Whether an expression evaluates without error (from `hcl/v2/ext/tryfunc`).

## `DocFunc(evalCtxFn)` — context-aware

### `doc(name) -> string`

Returns a function's documentation by name: `doc("add")`. It looks the name up in
the assembled eval context's function table and returns that function's
**description** — the doc comment captured on a functy declaration
(`FuncDecl.Doc`, wired into the compiled function's cty `Description`), or whatever
`Description` a host function carries. It is **tri-state**:

- `null` — no such function (absent from the context);
- `""` — the function exists but is undocumented;
- `"text"` — the function's description.

Distinguishing absent (`null`) from undocumented (`""`) lets a caller catch a
mistyped name without `doc` having to throw — absence is a normal reflection
answer, so strictness is opt-in (`assert(doc(x) != null)`), and the two states
compose with `coalesce` / `cond`.

Unlike the functions above it is **not** in `Stdlib()`, because it needs a handle
to the context. `functy.DocFunc(evalCtxFn)` takes the same late-binding closure
passed to `Result.Compile`; at call time that yields the merged context whose flat
`Functions` map holds every function (host- and functy-defined). A host wires it in
under the name `doc`:

```go
ctx.Functions["doc"] = functy.DocFunc(evalCtxFn)
```

The argument is a plain string (no laziness / `customdecode`): `doc` needs the
*merged* context, which `evalCtxFn` already provides, not the call-site expression.
A richer `help(name)` — assembling a function's full calling convention and
per-argument docs — is left for later; `doc` is the primitive it will build on.

## Notes

- **No new dependencies.** The lazy builtins use `hcl/v2/ext/customdecode` and
  `hcl/v2/ext/tryfunc`, both part of the already-required `hcl/v2` module.
- **Rich objects.** functy recognizes the `_capsule` / `_ctx` marker only to *name* a
  type (`typeof` / `typekind`) — a cheap read of the capsule's name. Behavior that
  *dispatches* on the capsule (`tostring` / `length` via `Stringable` / `Lengthable`)
  is intentionally left to [`rich-cty-types`](https://github.com/tsarna/rich-cty-types),
  which rich-object users already depend on.

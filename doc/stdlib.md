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
assert(n > 0)                                   // -> error { message = "assertion failed", range }
assert(port > 0, "port must be positive")       // string message
assert(ok, { message = "denied", code = 403 })  // object message, like error()

var _ok
var err: error
_ok, err = assert(valid(x), "invalid x")        // capture instead of unwinding
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

## Notes

- **No new dependencies.** The lazy builtins use `hcl/v2/ext/customdecode` and
  `hcl/v2/ext/tryfunc`, both part of the already-required `hcl/v2` module.
- **Rich objects.** functy recognizes the `_capsule` / `_ctx` marker only to *name* a
  type (`typeof` / `typekind`) — a cheap read of the capsule's name. Behavior that
  *dispatches* on the capsule (`tostring` / `length` via `Stringable` / `Lengthable`)
  is intentionally left to [`rich-cty-types`](https://github.com/tsarna/rich-cty-types),
  which rich-object users already depend on.

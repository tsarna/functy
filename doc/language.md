# functy Language Reference

functy is an imperative language whose values are cty values and whose
expressions are HCL. A `.cty` source file is a sequence of function declarations.
Compiling it produces cty `function.Function` values that can be added to an
`*hcl.EvalContext` and called from any HCL expression.

- [Lexical structure](#lexical-structure)
- [Types](#types)
- [Expressions](#expressions)
- [Functions](#functions)
- [Statements](#statements)
- [Control flow](#control-flow)
- [Error handling](#error-handling)
- [Scoping](#scoping)
- [Top-level const and var](#top-level-const-and-var)
- [Grammar](#grammar)

## Lexical structure

### Comments

```functy
// line comment
# line comment
/* block comment */
```

Both Go-style `//` and shell/HCL-style `#` introduce line comments; `/* */`
introduces a block comment.

### Statement termination

functy uses Go-style implicit termination:

- A newline ends the current statement, **unless** it occurs inside brackets
  (`()`, `[]`, `{}`) or immediately after a token that cannot end a statement (a
  binary or unary operator, `,` `.` `?` `:` `=`, or an opening bracket). In those
  cases the newline is a line continuation.
- A `;` explicitly ends a statement, so several statements may share a line.
- Blank lines and comment-only lines are ignored.

### Identifiers and keywords

Identifiers match `[A-Za-z_][A-Za-z0-9_]*`.

Reserved keywords: `func`, `var`, `const`, `return`, `if`, `else`, `for`,
`while`, `in`, `break`, `continue`, `switch`, `case`, `default`, `try`, `catch`,
`finally`, `defer`, `throw`, `true`, `false`, `null`.

Type names (`string`, `number`, `bool`, `any`, `list`, `set`, `map`, `object`,
`tuple`, `optional`) are contextual — reserved only in a type annotation.

## Types

functy types **are** cty types. Type annotations use the familiar cty
type-constraint grammar (the same shape Terraform `variable` blocks use):

```
string   bool   number   any
list(<type>)        set(<type>)        map(<type>)
tuple([<type>, …])  object({ name = <type>, … })
object({ name = optional(<type>), … })   // optional object attributes
```

`any` denotes the absence of a constraint (cty's dynamic type).

functy resolves these annotations with its **own** resolver rather than delegating
to `ext/typeexpr`. The built-in grammar above behaves identically, but the
resolver also supports host-registered **named types** (see below), which a closed
grammar cannot express.

### Type aliases

`type Name = <type>` declares a reusable alias for a type:

```functy
type HttpResult = object({ status = number, body = string })

func fetch(url: string) -> HttpResult { … }
```

- Aliases are **order-independent** — an alias may be used before it is declared,
  and one alias may reference another (cycles are an error).
- Aliases are **project-scoped**: every alias from the `.cty` files loaded together
  is visible to all of them, exactly like the function namespace — so a function in
  one file may use a type declared in another, with no import needed. A duplicate
  alias name (within or across files) is an error.
- An alias over a **concrete** type (`type Id = string`) may be used in nested
  position too (`list(Id)`). An alias over a host capsule/open type may only be used
  as a whole-annotation leaf.
- Aliasing a built-in (`type string = …`) or a host-registered type name is an
  error.

`type` is recognized as a declaration keyword only at file top level, so it remains
usable as an ordinary variable/parameter name inside function bodies.

### Gradual typing

- An **unannotated** variable, parameter, or return is **dynamic**: it holds any
  value, and the value's type may change on reassignment.
- An **annotated** declaration is **pinned**: every value assigned to it is
  converted to the declared type. A successful conversion stores the converted
  value; a failed conversion is an error (catchable — see
  [Error handling](#error-handling)).

```functy
var n             = 0       // dynamic
var count: number = 0       // pinned to number
count = count + 1           // ok
count = "12"                // ok — converts "12" -> 12
count = "nope"              // error: cannot convert "nope" to number
n = "now a string"          // ok — n is dynamic
```

### Default of a typed declaration

A typed `var` with no initializer defaults to **null of the declared type**;
reading it before assignment yields that typed null.

```functy
var x: number        // x == null
var l: list(string)  // l == null
```

### Named and open types (host-registered)

A host embedding functy can register named types, which then become usable in any
annotation as a whole-annotation leaf (`w: widget`, `e: ctx`). Two flavors:

- **Named (capsule) types** — registered with `Parser.RegisterType(name, ty)`.
  Enforced by **type identity**: a value must already be of that type (or `null`).
  This is how host cty capsule and rich-object types (a message bus, a URL, a byte
  string) are named without converting/copying them.
- **Open types** — registered with `Parser.RegisterOpenType(name, pred)`. Enforced
  by a **predicate**; a satisfying value is passed through **untouched**, so extra
  attributes survive (a plain structural conversion would strip them). Open types
  describe "an object that carries *at least* these attributes," and come in two
  usual shapes:
  - **marker-capsule** — keyed on a marker attribute, e.g. a context object whose
    `_ctx` attribute is the context capsule type; any other attributes (`args`,
    `fields`, …) are permitted and preserved.
  - **required-attributes** — assert some attributes exist with the right types and
    ignore the rest, e.g. the built-in `error` (an object with at least a string
    `message`, which may also carry other fields).

functy ships one built-in open type, **`error`** — an object with at least a string
`message` (the shape `throw` raises and `catch` binds). It is usable in any
annotation (`var err: error`, `func f(e: error)`), accepts a caught error or
`null`, and rejects anything else.

A named **capsule** type (and any alias over one) **composes** inside collections
and structural types — `list(widget)`, `object({ w = widget })` — enforced
element-wise by identity. An **open** type does not: it has no single concrete cty
type and is heterogeneous (an open object can carry different extra attributes), so
`error` and host open types may only be used as a whole annotation, not nested
(`list(error)` is an error).

Standalone (e.g. the `functy` CLI) registers no other named types, so only the
built-in grammar plus `error` is available there.

### `null` as a type

`null` is valid **only** as a function return type, where it declares a void
(null-returning) function — see [Return type and returning](#return-type-and-returning).
It is **not** a `var`/`const`/parameter type; `var x: null` is an error. (This is
distinct from the typed-null *default value* a typed declaration takes above.)

### Strict typing

By default functy is gradually typed — annotations are optional. A host (or a
file) can make them **mandatory**, so signatures and declarations are
self-documenting and an unintended `any` is visible. The type may still be `any`,
but it must be written.

A host enables requirements on the parser:

```go
p := functy.NewParser().
    RequireParamTypes(true).     // every parameter needs `: T`
    RequireReturnType(true).     // every function needs `-> T`
    RequireDeclaredTypes(true)   // every var/const needs `: T`
```

A file can request the same from within its **leading comment block** using a
directive (see below):

```functy
//functy:strict                            // all three
//functy:require param_types return_type   // or specific ones
```

Requirements are **tighten-only**: a file directive may *add* a requirement the
host did not set, but cannot relax one the host mandates (effective =
host OR file). A violation says whether the rule came from the host or a file
directive. The explicit escapes — `: any`, `-> any`, `-> null` — satisfy a
requirement.

### Directive comments

A directive comment is a line comment with **no space** after `//`, of the form
`//<namespace>:<name> [args]` (a space, `// functy: …`, is ordinary prose). functy
acts only on its own `functy:` namespace (`strict`, `require`); every other
namespace is collected into `Result.Directives` and passed through untouched for
the host to interpret:

```functy
//vinculum:cache 5m
//myapp:route /x
```

(Currently only file-scope directives — those in a file's leading comment block —
are collected; per-function directives are planned.)

## Expressions

Every expression is a real HCL expression, parsed by HCL and evaluated lazily.
Consequently operators, function calls, indexing, conditionals, string and
directive templates (`${}`, `%{}`), splat, and `for`-expressions all work
exactly as in HCL, and lazy constructs like `cond()` and short-circuit
`&&`/`||` evaluate only what they need.

```functy
if n > 0 { … }
while i < len(items) { … }
return "hello ${name}"
```

The names available inside an expression are: all functions in the eval context
(host built-ins plus the functy functions being compiled, enabling recursion and
mutual recursion); host globals (constants, environment, etc.); and the current
function's parameters and in-scope locals.

There is no implicit context object. A function that needs one (for example to
pass to a host `send(ctx, …)` function) takes it as an ordinary parameter.

## Functions

A `.cty` file's top level is a sequence of function declarations:

```functy
func name(<params>) -> rettype? {
    <statements>
}
```

Each `func` compiles to one cty function registered under `name`.

### Parameters

```
name                 // dynamic, required
name: T              // typed, required
name = default       // dynamic, optional
name: T = default    // typed, optional
*rest                // variadic, collects extras into a tuple
*rest: T             // variadic, collects extras into list(T)
```

- Required parameters must precede optional ones.
- An optional parameter's default is an HCL expression evaluated in a minimal
  context (host functions and globals, no other parameters).
- A typed parameter converts its argument (or default) to `T`.
- At most one variadic parameter, which must be last. Without it, extra
  arguments are an error.

### Return type and returning

```functy
func f(...) -> string { … }   // every `return e` converts e to string
func g(...)             { … }  // dynamic return
func h(...) -> null     { … }  // void: returns only null
```

`return expr` exits with a value; `return` (or falling off the end) returns
`null` — typed null if a return type was declared. Statements after an
unconditional `return` in the same block are a compile-time "unreachable code"
error.

A **void** function is declared `-> null`: it returns `null`, and a `return`
inside it may only be bare (`return`) or `return null` — any other
`return <expr>` is a compile-time error. Omitting the return type leaves the
return dynamic (may return a value *or* null); use `-> null` to enforce that
nothing meaningful is returned.

A cty function returns exactly one value; express multiple results with an
object or tuple and destructure at the call site:

```functy
func divmod(a: number, b: number) -> object({ q = number, r = number }) {
    return { q = a / b, r = a % b }
}
// caller:  var result = divmod(17, 5); result.q   // 3
```

## Statements

### Variable declaration — `var`

```functy
var x = expr        // dynamic local, initialized
var x: T = expr     // typed local, initialized + converted
var x: T            // typed local, defaults to null of T
var x               // dynamic local, defaults to null
```

`var` introduces a binding in the current block scope. Re-declaring a name in
the same scope is a compile-time error; declaring a name that exists in an
enclosing scope shadows it.

#### `:=` shorthand

```functy
x := expr           // shorthand for: var x = expr   (always untyped)
```

`x := expr` is sugar for an untyped `var x = expr`. It declares a new binding
with the same rules as `var` (re-declaring in the same scope is an error;
shadowing an outer scope is fine), so unlike Go's `:=` it cannot reuse an
existing variable. Because the shorthand has no place for a type annotation, it
is rejected under strict declared-types — use `var x: T = expr` there. The
two-target form `val, err := expr` declares *and* captures an error — the value
target dynamic, the error target pinned to `error` (see
[Error capture](#error-capture--val-err--expr)).

### Assignment — `=`

```functy
x = expr
```

`=` reassigns an existing binding, searching outward through the scope chain and
updating the nearest one found. Assigning to a name that is not declared is an
error. The target is a bare identifier only — `a.b = x` and `a[i] = x` are not
supported, because cty values are immutable. To "update" a collection, rebuild
it: `x = merge(x, { k = v })`, `x = setunion(x, [v])`, etc.

### Expression statement

Any expression may stand alone as a statement; its value is discarded. This is
how side-effecting host functions are called:

```functy
log_info("starting", { id = id })
send(ctx, bus.main, "topic", payload)
```

### Block scope

A bare `{ … }` introduces a nested scope:

```functy
{
    var tmp = expensive()
    use(tmp)
}   // tmp is out of scope here
```

A `{` at the start of a statement always opens a block, so an object-literal
expression statement must be parenthesized (`({ a = 1 })`).

## Control flow

### if / else if / else

```functy
if n > 0 {
    return "positive"
} else if n < 0 {
    return "negative"
} else {
    return "zero"
}
```

### for and while

`while` is a readability synonym for the condition form of `for`.

```functy
for cond { … }                        // condition loop
while cond { … }                      // synonym
for { … }                             // infinite loop (needs break)
for var i = 0; i < n; i = i + 1 { … } // three-clause
for v in coll { … }                   // range: value
for k, v in coll { … }                // range: key/value
```

Range semantics:

- **list / tuple**: `for v in xs` binds each element; `for i, v in xs` binds
  index and element.
- **set**: `for v in xs` binds each element; in the two-variable form the first
  variable is a stable enumeration index (sets are unordered).
- **map / object**: `for k, v in m` binds key and value; `for v in m` binds the
  value only.

A loop body's scope is fresh each iteration.

### break / continue

Bare `break` and `continue` affect the innermost enclosing loop. Using either
outside a loop is a compile-time error.

### switch

```functy
switch http_status(resp) {
case 200, 201, 204:
    return "ok"
case 404:
    return "missing"
default:
    return "error"
}
```

- The subject is evaluated once; the first `case` with a value equal to the
  subject runs, then the switch exits (no fallthrough).
- An expression-less `switch { case cond: … }` acts as an if/else chain (cases
  are boolean expressions).
- At most one `default`.

## Error handling

### throw

```functy
throw "message"
throw { message = "bad input", code = 422 }
```

`throw` raises an error and unwinds the function until caught. Throwing a string
produces the error value `{ message = <string>, value = null }`; throwing an
object uses that object directly (it should carry a `message`). An uncaught error
surfaces at the call site as an error.

### try / catch / finally

```functy
try {
    var r = http_get(url)
    process(r)
} catch err {
    log_error("fetch failed", { url = url, err = err.message })
    return null
} finally {
    send(ctx, bus.audit, "done", url)
}
```

- A `try` block runs its statements. If any statement raises an error — an
  explicit `throw` **or** an ordinary expression-evaluation failure (a failing
  function, a type-conversion failure, etc.) — control transfers to `catch`.
- `catch err { … }` binds the error value to `err` (an object with at least
  `.message` and `.value`). The name is optional: `catch { … }` ignores it.
- `finally { … }` runs unconditionally after the try (and catch, if it ran),
  including while a return/break/continue or an uncaught error unwinds through.
- A `try` must have a `catch`, a `finally`, or both.
- An error raised inside `catch` or `finally` propagates outward; a `finally`
  that itself returns or raises replaces whatever was in flight.

This complements HCL's expression-level `try()`/`can()`, which remain available
inside any expression.

### Error capture — `val, err = expr`

A two-target assignment captures an evaluation failure into a variable instead of
unwinding the function — the statement-level analog of Go's `v, err := f()`:

```functy
var val: SomeType
var err: error
val, err = risky(x)        // on success: val = <result>, err = null
                           // on failure: val = null, err = <the error>
```

It is exactly sugar for a `try`/`catch`:

```functy
try {
    val = risky(x)
    err = null
} catch e {
    val = null
    err = e
}
```

- The right-hand side is evaluated **once**. Any failure — a `throw` unwinding out
  of a called function, a type-conversion failure, a failing host function — is
  caught and bound to the error target (the same object shape `catch` binds, with
  at least `.message` and `.value`); it does not propagate past the statement.
- Both non-blank targets must **already be declared**, like a plain `=`. The
  `:=` form declares them instead: `val, err := risky(x)` is the capture above
  preceded by `var val` (dynamic) and `var err: error` — the value target is
  untyped, the error target is pinned to the built-in `error` type (it only ever
  holds an error or null) — with the same duplicate-declaration rules as `var`.
- Either target may be the blank identifier `_` to discard it:
  - `_, err = expr` — evaluate for side effects / error only (no value target).
  - `val, _ = expr` — best-effort assign; on failure `val` is null and the error is
    swallowed (Go's `v, _ = f()`).
  - `_, _ = expr` is a compile-time error — use a plain expression statement to
    evaluate something only for its side effects.
- This is statement-level error capture, **not** a multi-return: the function still
  produces a single value; the second target merely receives the caught `error` or
  `null`.

### defer

```functy
func handle(ctx, id) {
    var conn = open(id)
    defer close(conn)
    defer send(ctx, bus.events, "closed", id)
    // … work …
}   // on exit: send(...) runs, then close(conn) runs (LIFO)
```

- `defer <expr>` schedules `<expr>` to run when the enclosing **function** exits
  — by return, fall-off, or an error unwinding past it — in LIFO order.
- A deferred expression is evaluated at function-exit time, in the scope as it
  then exists (not captured at `defer` time). Capture any values you need into
  locals before deferring and reference those locals.
- Deferred expressions run after any `finally` blocks the exit unwinds through;
  because functy has no named returns, a defer cannot change the return value. A
  deferred expression that raises aborts the remaining defers and propagates.

## Scoping

functy uses lexically nested scopes:

- Lookup walks outward through the chain; the nearest binding wins.
- `var` creates a binding in the innermost scope.
- `=` updates the nearest existing binding.
- Each function body, control-flow branch, loop iteration, case body,
  try/catch/finally body, and bare block introduces a new child scope.
- Parameters are bound in the function's top scope.

## Top-level const and var

By default a `.cty` file contains only function declarations. A host may opt in
(via the parser's `AllowTopLevelConst` / `AllowTopLevelVar` options) to also
collect top-level declarations:

```functy
const pi   = 3.14159
const tau: number = pi * 2     // may reference a const declared elsewhere
var   counter: number = 0
```

These are **collected** as unevaluated declarations (`Result.Consts` /
`Result.Vars`), each exposing its initializer expression so the host can fold
them into its own dependency-sorting and evaluation, then place the results into
the shared eval context. The `functy` CLI enables this and evaluates the
declarations (resolving cross-references in any order) into the run context.

## Grammar

```ebnf
File        = { FuncDecl } .
FuncDecl    = "func" ident "(" [ ParamList ] ")" [ "->" Type ] Block .
ParamList   = Param { "," Param } [ "," Variadic ] | Variadic .
Param       = ident [ ":" Type ] [ "=" Expr ] .
Variadic    = "*" ident [ ":" Type ] .

Block       = "{" { Stmt } "}" .
Stmt        = VarDecl | Assign | ExprStmt | Return
            | If | For | While | Switch | Break | Continue
            | Try | Defer | Throw | Block .

VarDecl     = "var" ident [ ":" Type ] [ "=" Expr ] Term .
Assign      = ident "=" Expr Term .
ExprStmt    = Expr Term .
Return      = "return" [ Expr ] Term .
Break       = "break" Term .
Continue    = "continue" Term .
Throw       = "throw" Expr Term .
Defer       = "defer" Expr Term .

If          = "if" Expr Block { "else" "if" Expr Block } [ "else" Block ] .
While       = "while" Expr Block .
For         = "for" [ ForHeader ] Block .
ForHeader   = Expr                                   (* condition *)
            | SimpleStmt ";" Expr ";" SimpleStmt     (* three-clause *)
            | ident [ "," ident ] "in" Expr .        (* range *)
SimpleStmt  = VarDecl | Assign | ExprStmt .
Switch      = "switch" [ Expr ] "{" { Case } [ Default ] "}" .
Case        = "case" Expr { "," Expr } ":" { Stmt } .
Default     = "default" ":" { Stmt } .

Try         = "try" Block [ "catch" [ ident ] Block ] [ "finally" Block ] .

Type        = (* the cty type-constraint grammar *) .
Expr        = (* any HCL expression *) .
Term        = newline | ";" | (* implicit at block close *) .
```

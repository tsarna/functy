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

Comments never affect evaluation. They are, however, **retained** (with position)
and exposed to a host on `Result.Comments`, so tooling — doc-comment metadata
(below), and a future formatter — can see them even though the parser itself does
not.

### Doc comments

A **doc comment** is the run of whole-line `//` or `#` comments on consecutive
lines **directly above** a declaration, with no blank line in between. It is
surfaced to a host as the declaration's documentation — `FuncDecl.Doc` for a
`func`, and `Decl.Doc` for a top-level `var` / `const`:

```functy
// Adds two numbers.
// The second is optional.
func add(a: number, b: number = 0) -> number {
    return a + b
}
```

Rules:

- The block must be **immediately** above the declaration; a blank line ends it,
  and a comment trailing code on the same line is not a doc comment.
- Each line's marker (`//`, `///`, `#`, …) and one following space are stripped;
  lines are joined with a newline.
- **Block comments** (`/* */`) never form documentation.
- **Directive lines** (`//<ns>:<name>`, see below) inside the block are excluded
  from the prose but do not break it — prose above a directive is still collected.
  (Because a directive requires the `//` form, a `#` block is never ambiguous.)

A host uses `Doc` for generated documentation and editor hovers — and, more
generally, anywhere it wants a function's description available at runtime.

#### Parameter docs

Individual parameters can be documented too (surfaced on `Param.Doc`, and — for
required parameters — on the compiled function's cty parameter description). In a
multi-line parameter list, use a **trailing** comment on the parameter's line, or
a **leading** `//` / `#` block directly above it for a longer description (the
leading block wins if both are present):

```functy
func request(
    url: string,       // the endpoint to call
    // Seconds to wait before giving up.
    // Applies to the whole request, not per-redirect.
    timeout: number = 30,
) -> http_response { … }
```

Parameter docs are a feature of the multi-line layout: a parameter only takes a
trailing comment when it starts its own line, so a single-line list
(`func f(a, b) // …`) attributes nothing.

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
`while`, `in`, `break`, `continue`, `switch`, `case`, `default`, `fallthrough`,
`try`, `catch`, `finally`, `defer`, `throw`, `true`, `false`, `null`.

Type names (`string`, `number`, `bool`, `any`, `list`, `set`, `map`, `object`,
`tuple`, `optional`) are contextual — reserved only in a type annotation.

`namespace`, `type`, and `test` are likewise contextual — special only at
top-level declaration position, so each remains usable as an ordinary identifier
(`func namespace(...)`, `var test = 1`).

A leading underscore marks a declaration as private to its namespace (see
[Namespaces and visibility](#namespaces-and-visibility)); `_` alone is the blank
identifier and cannot name a declaration.

## Types

functy types **are** cty types. Type annotations use the familiar cty
type-constraint grammar (the same shape Terraform `variable` blocks use):

```text
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
acts only on its own `functy:` namespace (`strict`, `require`,
[`extern`](#extern-declarations)); every other namespace is collected into
`Result.Directives` and passed through untouched for the host to interpret:

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

```text
name                 // dynamic, required
name: T              // typed, required
name = default       // dynamic, optional
name: T = default    // typed, optional
name?                // optional, no default — extern files only
name?: T             // typed, optional, no default — extern files only
*rest                // variadic, collects extras into a tuple
*rest: T             // variadic, collects extras into list(T)
```

- Required parameters must precede optional ones — except in an
  [extern file](#extern-declarations), where the declaration transcribes a host
  function that may take optional arguments at the head as well as the tail.
- `name?` (optional with no default) may only be written in an extern file, where
  it is never compiled. It exists because a *leading* optional parameter has no
  spellable default, and is mutually exclusive with `= default`.
- An optional parameter's default is an HCL expression evaluated in a minimal
  context (host functions and globals, no other parameters).
- A typed parameter converts its argument (or default) to `T`.
- At most one variadic parameter, which must be last. Without it, extra
  arguments are an error.
- The whole signature may span multiple lines — newlines inside the parameter
  list (after `(`, between parameters, before `)`) and across the rest of the
  signature (before `->`, its type, and the `{`) are insignificant, and a trailing
  comma is allowed:

  ```functy
  func f(
      a: string,
      b: bool,
  ) -> http_response { … }
  ```

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

## Extern declarations

A function the **host** provides has no functy source, so `help()`, generated docs,
and editor hovers have nothing to show for it — and its cty metadata usually cannot
even describe it honestly. A cty function fakes optional and defaulted arguments
with a trailing `VarParam`, which erases their names and defaults; and because a cty
optional parameter may only sit at the *tail*, the widespread `f([ctx,] x)`
convention (a leading context sniffed out of `args[0]`) is not expressible at all.
Such a function reflects as `f(x, ...args)` — true, and useless.

An **extern file** states the real signature. It is an ordinary `.cty` file whose
leading comment block carries the `//functy:extern` directive, holding `func`
declarations with **no bodies**:

```functy
//functy:extern

// Read a value from a thing.
func get(
    ctx?: ctx,        // the context, when the call is scoped to one
    thing,            // the gettable thing
    fallback = null,  // returned when the value is absent
) -> any

// Parse a time from a string.
func parsetime(format: string, s: string) -> time
```

Externs are **never compiled and never callable** — they are declarations, not code.
They surface on `Result.Externs`, in `functy symbols` (as `kind: "extern"`), and in
`help()`, which prefers an extern over the cty-metadata fallback for the same name:
that preference is the entire point, since the host function *is* registered and its
cty shape is exactly what the extern exists to replace.

Rules:

- **`//functy:extern` is a file-level directive, not a keyword.** Extern-ness is a
  property of the file, and spelling it there is what keeps a bodiless `func` a hard
  error *everywhere else*: a half-typed declaration (signature written, brace not yet
  opened) must stay a syntax error rather than silently becoming a valid one.
- An extern file holds **only** bodiless functions. `var`, `const`, `test`, and
  `type` are rejected — an extern file documents someone else's Go functions, so it
  carries no functy code of its own. A leading `namespace` declaration *is* allowed,
  and qualifies the externs like any other declaration.
- A `_`-prefixed name is rejected: an extern is global-and-defined-elsewhere by
  definition, so a namespace-local extern is a contradiction.
- `name?` marks a parameter optional with no default, and only here. It is the only
  way to spell a *leading* optional, and the required-before-optional ordering rule is
  relaxed accordingly.
- A name may be declared more than once: see **Overload sets** below.
- A named type an extern mentions that the reader has not registered — `ctx` and
  `time` above — resolves to an **opaque** name: carried for rendering, enforced not
  at all, and reported once per file as a warning. An extern names *its host's* types,
  and whoever reads it (the `functy` CLI, an editor) generally is not that host, so
  failing there would make extern files unusable exactly where they are most useful.
  The cost is that a misspelled type resolves instead of failing; hence the warning.

### Overload sets

Some host functions are not one function with optional parameters — they are several
functions sharing a name, and no single signature describes them honestly. Declare each
form:

```functy
//functy:extern

// Parse a timestamp.
func parsetime(s: string) -> time
func parsetime(format: string, s: string) -> time
func parsetime(format: string, s: string, tz: string) -> time
```

`parsetime(s)` parses a timestamp; `parsetime(format, s)` parses a *format* and then a
timestamp. Written as `parsetime(format?, s)` it would be a lie, because the sole
argument of the one-argument form is not a format.

Each form carries its own return type, which is what makes a function whose result type
depends on its arguments sayable at all:

```functy
func timeadd(ts: string, dur: string) -> string   // the stdlib-compatible path
func timeadd(ts: time, dur: duration) -> time
```

Rules:

- The forms of one name must live in **one file**. The same name in two files is a
  collision — two packages both claiming `get` — not an overload set, and is an error.
- Two forms must differ in their **parameters**: arity, types, or optionality. Two forms
  of the same shape are a copy-paste, and an error; names and docs do not distinguish a
  call, so they do not distinguish a form.
- `help()` lists every form, then the documentation, then one `Parameters:` section
  unioned across the forms. Document the family once, above the first form.

Overload sets are an extern-only construct: a real functy `func` still declares one
signature, and a duplicate name is still a duplicate function.

### Shipping externs from a package

A package that provides cty functions ships their declarations alongside them. It
`go:embed`s the `.cty` file and exposes it as opaque bytes — it never imports functy,
and nothing on its side parses them:

```go
//go:embed externs.cty
var externsCty []byte

const ExternsFilename = "mypkg/externs.cty"

func Externs() []byte { return externsCty }
```

The host registers them on its parser:

```go
parser.RegisterExterns(mypkg.Externs(), mypkg.ExternsFilename)
```

Registered declarations arrive on `Result.HostExterns` (as opposed to `Result.Externs`,
which holds the externs declared by the *parsed sources*), and `help()` reads both. The
split is what keeps `fmt` honest: a tool that renders a source iterates `Externs`, so it
can never emit a host package's declarations into a user's file.

Registration **verifies** the `//functy:extern` directive rather than forcing it, so the
embedded file is a real, standalone `.cty` — `functy fmt`, `functy symbols`, and an editor
open it directly, and one byte string means the same thing however it is loaded.

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

A loop may be **labeled** (`label:` immediately before a `for`/`while`), and
`break label` / `continue label` then target that loop instead of the innermost
one — the idiomatic way to exit or advance an outer loop from within a nested one:

```functy
search:
for i in rows {
    for j in cols {
        if match(i, j) { break search }     // exits both loops
        if skip(i)     { continue search }  // next i, abandoning the j loop
    }
}
```

- A label may only precede a `for`/`while`, and must be unique among the loops
  enclosing its use. The label after `break`/`continue` must name an enclosing
  labeled loop (a sibling loop's label is out of scope).
- `continue label` advances the labeled loop normally, including running a
  three-clause loop's post statement.
- A labeled break/continue unwinds through any `try`/`finally` blocks between it
  and the target loop, running their `finally` clauses, just like `return`.

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
  subject runs, then the switch exits (no implicit fallthrough).
- An expression-less `switch { case cond: … }` acts as an if/else chain (cases
  are boolean expressions).
- At most one `default`. It runs when no case matches, regardless of where it
  appears among the clauses.
- `fallthrough` as the **final** statement of a clause transfers control to the
  **next clause in source order**, running its body without testing its values
  (it may fall into a `default`, and a `default` that is not last may fall into
  the following clause):

  ```functy
  switch n {
  case 1:
      log("one")
      fallthrough   // continue into case 2
  case 2:
      log("one or two")
  }
  ```

  It is rejected anywhere but a clause's last statement — it cannot be nested in
  an `if`/loop, and the last clause cannot fall through (there is no next clause).

## Error handling

### throw

```functy
throw "message"
throw { message = "bad input", code = 422 }
```

`throw` raises an error and unwinds the function until caught. Throwing a string
produces the error value `{ message = <string> }`; throwing an object uses that
object directly (it should carry a `message`); throwing any other value — a number,
bool, list, … — wraps it as `{ message = "error", value = <the value> }` so the raw
payload is recoverable via `.value` (the only case an error carries a `value`). An
uncaught error surfaces at the call site as an error.

Every caught error also carries a **`range`** — where it was raised — shaped
`{ filename, start = { line, column, byte }, end = { … } }` (so
`e.range.start.line`, `e.range.filename`). It is stamped at the `throw` (or at the
failing expression, for an evaluation error), preserved across function-call
boundaries and through a rethrow (`throw e` keeps the original site, not the
rethrow site), and is `null` only when no source location is available. Throwing an
object that already has a `range` keeps it.

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
  function, a type-conversion failure, etc.) — control transfers to the catch
  clauses.
- `finally { … }` runs unconditionally after the try (and catch, if it ran),
  including while a return/break/continue or an uncaught error unwinds through.
- A `try` must have at least one `catch`, a `finally`, or both.
- An error raised inside a `catch` or `finally` propagates outward; a `finally`
  that itself returns or raises replaces whatever was in flight.

#### Selective catch — `catch [name] [: type] [if guard]`

A `try` may have **multiple catch clauses**, tried in order; the **first match
wins**. Each clause may filter by type and/or a guard:

```functy
try {
    process(req)
} catch e: object({ code = number }) if e.code >= 500 {
    retry()                       // errors carrying a numeric code >= 500
} catch e: timeout {              // a host-registered open type, as a category
    backoff()
} catch e if e.kind == "user" {   // arbitrary predicate over the error
    reject(e)
} catch e {                       // catch-all — must be last
    log_error("unhandled", { err = e.message })
}
```

- **Binding** — `catch e { … }` binds the error to `e` (an object with at least
  `.message`); the name is optional (`catch { … }`, `catch if cond { … }`). The
  bound value is always the **raw** error — a `: type` filter is a *gate*, not a
  cast, so all of the error's attributes survive.
- **Type filter** — `catch e: T` runs the clause only if the error satisfies the
  type `T`: a host-registered open type (an error *category*), a structural object
  type (`object({ code = number })` — duck-typed on attributes), or `error`
  (matches any error). A non-match falls through to the next clause.
- **Guard** — `catch e if cond` runs the clause only if `cond` is true. A guard is
  evaluated with the binding in scope; a guard that *itself* errors (e.g. reading
  an attribute the error lacks) propagates — gate the shape with a `: type` filter
  first when needed. Guards of earlier, non-matching clauses are evaluated (their
  side effects happen), in order, until one matches.
- **Catch-all** — a clause with no type and no guard matches every error and must
  be the **last** clause (a later clause would be unreachable — a compile error).
- **Unmatched** — if no clause matches, the error re-raises and propagates outward
  after `finally`, exactly as if there were no catch. To handle some and rethrow
  the rest, `throw e` inside a clause re-raises the caught error.
- **Across function calls** — a thrown error keeps its full structure (`code`,
  `kind`, custom attributes) even when caught from a *called* function, e.g.
  `catch e: object({ code = number })` on `return callee(x)`. Only genuine
  evaluation failures (a type conversion, a failing host function) arrive as the
  bare `{ message, value }` shape. (A Go host embedding functy can recover the same
  structured value from a call error with
  `errors.As(err, &functy.ThrownError{})` — `.Value` is the error object.)
- **Uncaught, at the host boundary** — an error that no `catch` handles unwinds out
  of the function as a `*functy.ThrownError`. A host can render it with source
  context — the failing line and (for a failed `assert`) the operand `detail` — via
  `te.Diagnostics()` (or `functy.ErrorDiagnostics(value)` for an error caught as a
  value), which returns `hcl.Diagnostics` for the standard `hcl.NewDiagnosticTextWriter`.
  The `functy run` CLI does this, so an uncaught error prints its source location and
  detail rather than a flat message.

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
  at least `.message`); it does not propagate past the statement.
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

## Execution limits

functy is a tree-walking interpreter, so an unbounded `for` / `while` would
otherwise let a single function wedge the host. A host (or the CLI's `--max-steps`
flag) may set a **step budget**: the maximum number of steps any one function
invocation may take.

- A *step* is one statement executed, plus one per loop iteration. The count is
  **per invocation** — each function call starts fresh at zero.
- Exceeding the budget aborts the whole evaluation with an execution-limit error.
  This error is **uncatchable**: `try` / `catch` and `val, err = …` re-propagate it
  rather than handle it (otherwise `try { while true {} }` would fire the guard,
  unwind into the catch, and loop again). `defer`s and enclosing `finally` blocks do
  **not** run as it unwinds — a defer could itself loop.
- The budget is **opt-in**: unset (or `0`) means unbounded, so an embedding that
  configures no limit is unaffected. The `functy` CLI applies a generous default
  (`--max-steps`, `0` to disable).

Because the counter is per invocation, this bounds a *single* function's runaway
loop but **not** recursion (each nested call starts a fresh count) nor work spread
across many small calls — and it is cooperative, so a single long-running host or
stdlib call (a huge `range()`, a catastrophic regex) runs to completion regardless.

## Scoping

functy uses lexically nested scopes:

- Lookup walks outward through the chain; the nearest binding wins.
- `var` creates a binding in the innermost scope.
- `=` updates the nearest existing binding.
- Each function body, control-flow branch, loop iteration, case body,
  try/catch/finally body, and bare block introduces a new child scope.
- Parameters are bound in the function's top scope.

## Namespaces and visibility

A file may open with a `namespace` declaration. Its functions are then registered
with the host under a **qualified name**, while the file's own functions go on
calling each other by their bare names:

```functy
namespace acme::math

// Registered with the host as acme::math::double.
func double(n: number) -> number {
    return _twice(n)          // a sibling, by its bare name
}

// Namespace-local: acme::math can call it; the host never sees it.
func _twice(n: number) -> number {
    return n * 2
}
```

A namespace name is one or more `::`-separated identifiers. `namespace foo` is as
valid as `namespace foo::bar` — `::` *subdivides* a name, but no depth is implied.

The declaration must be the **first** declaration in the file, and a file may have
at most one. A file without one is in the **global namespace**, which is a
namespace like any other — its functions simply reach the host under their bare
names, exactly as they always have.

### Private declarations: a leading `_`

A declaration whose name begins with an underscore is **namespace-local**: it is
compiled and callable from within its namespace, but is never handed to the host.
This is how a file keeps a helper to itself instead of publishing it into the
host's shared function namespace, where it might collide with a built-in.

Privacy is spelled as a naming convention rather than a keyword for two reasons.
It is visible at the **call site** — `_helper(x)` tells you what `helper(x)` cannot
— and it makes a collision with a host function *structurally impossible*, since
no host function, cty builtin, or add-on package function is `_`-prefixed.

The convention applies to `var`, `const`, and `type` too. Note `_` alone is the
blank identifier and cannot name a declaration.

### A namespace spans files

Two files declaring the same namespace share one unit: each can call the other's
functions — private ones included — by their bare names. This is the Go package
model, and it is why `_` means *namespace*-local rather than *file*-local.

### Nesting is a naming convention, not containment

`foo::bar` is **not** "inside" `foo` in any sense functy enforces. There is no
parent-namespace fallback, no partial qualification, and no hierarchy to walk:
code in `foo::bar` gets no special visibility into `foo`, and must spell
`foo::helper()` in full — exactly as an unrelated namespace would. A name either
matches exactly or is not found.

### Calling across namespaces

Use the fully-qualified name. There is no `import`; a namespace is usable the
moment it exists:

```functy
namespace acme::report

func summary() -> string {
    return "total: ${acme::math::double(21)}"
}
```

`::` is a **function-call selector** in HCL, so only functions can be qualified —
there is no `foo::bar::x` for a variable or a type. Namespacing therefore applies
to functions, which is the namespace that actually gets crowded. A namespaced
file's top-level `const`/`var` are still collected for the host (with their
namespace attached), and type aliases remain project-scoped across all sources.

### Shadowing: local wins

Because a namespace's own names are resolved before the host's, a function whose
bare name matches a host function shadows it **inside that namespace**:

```functy
namespace acme::text

func upper(s: string) -> string {   // warning: shadows the built-in upper
    return "SHOUT: ${s}"
}
```

Elsewhere the built-in is unaffected, and `acme::text::upper` remains available.
This is legal and occasionally deliberate, so it is a *warning*, not an error —
the `functy` CLI reports it, and a library host can do the same by comparing
`Compiled.Units` against its own registry. In the global namespace the same clash
is still a hard error, because there the function really would replace the
built-in.

Note that a private name can never trigger this. The same applies to the
test-only `skip()` builtin: a namespace that declares its own `skip` shadows it
for that namespace's tests.

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

## Tests

A `test "description" { … }` block declares a co-located test. Its body is ordinary
functy statements, so it can call the functions under test, declare locals, and use
`assert` — the natural way to express expectations:

```functy
func add(a: number, b: number) -> number { return a + b }

test "add sums two numbers" {
    assert(add(2, 3) == 5)
}

test "add is commutative" {
    var x = add(2, 3)
    var y = add(3, 2)
    assert(x == y, "add should be commutative")
}
```

- **Pass/fail/skip.** A test **passes** if its body runs to completion, is **skipped**
  if it calls `skip(...)`, and **fails** if any other error unwinds out of it — a
  failed `assert`, an explicit `throw`, or an evaluation error. Because `assert` raises
  a catchable error carrying the condition's source range and operand values, a failing
  test reports *where* and *why* (e.g. `n = -3`). Like Go/pytest, a test stops at its
  first failure.
- **`skip("reason")`.** A test-only builtin that stops the current test and marks it
  skipped (neither passed nor failed) — for work-in-progress or environment-gated
  tests. The reason is optional (`skip()`), and `skip` may be called from a helper the
  test invokes, not only directly in the body. `skip` exists only while running tests;
  it is not available to `functy run`.
- **Setup / teardown.** No special construct is needed: a test's setup is just the
  leading statements of its body, and `defer` gives per-test teardown that runs even
  when the test fails — `test "x" { var r = open(); defer close(r); … }`.
- **Scope.** A test body sees everything the host's eval context provides: all
  functions defined in the sources (and any host/baseline functions), plus top-level
  `const`/`var`. It is compiled and run just like a niladic function.
- **Not callable.** `test` blocks are collected separately (`Result.Tests`) and are
  **not** registered in the function namespace, so `functy run` ignores them and a
  test description never collides with a function name. `test` is a *contextual*
  keyword — special only at top-level declaration position — so it remains usable as
  an ordinary identifier (`func test(...) { … }` still works).
- **Running.** A host runs tests with `(*Result).RunTests(evalCtxFn)` (or
  `RunTestsMatching` with a name filter), which returns a `TestOutcome` per test (with
  `Passed`/`Failed`/`Skipped`, a `Duration`, and a `Diagnostics()` for rendering
  failures). The `functy` CLI exposes this as [`functy test`](cli.md#test), including a
  `--json` report for CI and editor tooling.

## Grammar

```ebnf
File        = [ NamespaceDecl ] ( { FuncDecl | TestDecl } | ExternFile ) .
ExternFile  = (* a file whose leading block has //functy:extern *)
              { ExternDecl } .
NamespaceDecl = "namespace" ident { "::" ident } Term .
FuncDecl    = "func" ident "(" [ ParamList ] ")" [ "->" Type ] Block .
ExternDecl  = "func" ident "(" [ ParamList ] ")" [ "->" Type ] Term .
TestDecl    = "test" string Block .
ParamList   = Param { "," Param } [ "," Variadic ] | Variadic .
Param       = ident [ "?" ] [ ":" Type ] [ "=" Expr ] .   (* "?" only in an extern file *)
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

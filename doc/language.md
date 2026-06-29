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

functy types **are** cty types. Type annotations use the standard cty
type-constraint grammar (the same one Terraform `variable` blocks use):

```
string   bool   number   any
list(<type>)        set(<type>)        map(<type>)
tuple([<type>, …])  object({ name = <type>, … })
object({ name = optional(<type>), … })   // optional object attributes
```

`any` denotes the absence of a constraint (cty's dynamic type).

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
```

`return expr` exits with a value; `return` (or falling off the end) returns
`null` — typed null if a return type was declared. Statements after an
unconditional `return` in the same block are a compile-time "unreachable code"
error.

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

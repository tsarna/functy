# Contributing to functy

functy is an imperative language whose values are [cty](https://github.com/zclconf/go-cty)
values and whose *expressions* are HCL. A functy source file (`.cty`) is a sequence
of function declarations; compiling one yields ordinary `cty` `function.Function`
values that can be added to an `*hcl.EvalContext` and called from any HCL
expression. functy parses the *statement* grammar (`func`, `var`, `if`/`else`,
`for`/`while`, `switch`, `try`/`catch`, ...) itself, but hands every embedded
expression and type annotation to HCL, so operators, templates, function calls,
and the type-constraint grammar behave exactly as they do elsewhere in HCL.

This guide is for people working on functy's internals. It describes how the
package is laid out, how the pipeline fits together, and — most importantly — the
expression-boundary algorithm that is the trickiest part of the parser.

## Building and testing

functy is a standalone module (`github.com/tsarna/functy`) and is **not** part of
any shared Go workspace. If you have a `go.work` on your `GOPATH`/checkout that
references sibling repos, it will interfere with builds here. Always disable the
workspace explicitly:

```sh
GOWORK=off go build ./...
GOWORK=off go vet ./...
GOWORK=off go test -timeout 30s ./...
```

The `-timeout 30s` flag is recommended: the interpreter runs user-authored loops,
so a buggy change (or a pathological test fixture) can spin. A short timeout turns
a hang into a fast, legible failure.

Tests live alongside the code as `*_test.go` files in the same package. Run the
whole suite with the command above before sending a change.

### Dependency policy

Keep the core library dependency-light. The library proper (the root package)
depends only on:

- `github.com/hashicorp/hcl/v2` (and `.../hcl/v2/hclsyntax`) — lexing and
  expression parsing.
- `github.com/zclconf/go-cty` (and `.../cty/function`, `.../cty/convert`) — the
  value and type system.

`github.com/spf13/cobra` is used **only** by the `cmd/functy` CLI, never by the
library. Do not introduce new dependencies into the root package without a strong
reason; a host embedding functy should be able to take it on with no surprises.

## Architecture overview

Compilation is a pipeline. Given one or more sources (collected by `ParseSources`
from paths, directories, `embed.FS`, raw bytes, or `Source` values), a `Parser`
turns them into a `Result`, and `Result.Compile` turns that into callable cty
functions:

1. **Lex** every source into a functy token stream (`lex`). This wraps HCL's
   native-syntax lexer, then adapts the stream: block comments are dropped, line
   comments become newlines (so statement termination still happens), and the
   spurious diagnostic HCL emits for `;` is suppressed (functy uses `;` as an
   explicit statement terminator).
2. **Collect type aliases project-wide.** Before parsing any source, a linear scan
   gathers every top-level `type Name = <type>` declaration from *all* co-loaded
   sources and resolves them together into one shared type environment. Aliases are
   therefore project-scoped and order-independent: a function in one file may name a
   type declared in another.
3. **Per-file directives and strictness.** Each file's leading directive comments
   (`//functy:strict`, `//functy:require ...`, and any host namespaces) are
   collected. functy acts on its own `functy:` namespace to compute that file's
   strict-typing requirements; all directives are also passed through to the host
   in `Result.Directives`. Strictness is tighten-only: a file may request stricter
   typing, but can never relax a requirement the host set.
4. **Recursive-descent parse.** Each file is parsed against the shared type
   environment. The parser walks the token stream for statement structure, and for
   every embedded expression or type annotation it recovers the exact byte span and
   calls `hclsyntax.ParseExpression` (see below). Parsed declarations accumulate
   into the `Result` (`Funcs`, plus optionally `Consts`/`Vars`/`Types`).
5. **Compile.** `Result.Compile(evalCtxFn)` turns each `FuncDecl` into a cty
   `function.Function`. The eval context is **late-bound**: each function captures
   `evalCtxFn` and calls it at *invocation* time, not at compile time. This is what
   lets a function call its siblings and reference host globals that are finalized
   after compilation — recursion and mutual recursion fall out for free.

## Source files

Root package (`github.com/tsarna/functy`):

| File | Responsibility |
| --- | --- |
| `functy.go` | Public `Parser` API: chained option setters, `Parse`/`ParseAll`, the two-stage `parseSources` core, and the `Result`/`TypeAlias`/`Decl` result types. |
| `source.go` | `ParseSources` — discovers `.cty` sources from paths, directories, `embed.FS`, `[]byte`, and `Source` values; defines the `.cty` `Extension`. |
| `lexer.go` | `lex` and the `token` type: wraps `hclsyntax.LexConfig`, adapts comments/semicolons, and provides the token classifiers (bracket, terminator, line-continuation) the parser depends on. |
| `parser.go` | The recursive-descent statement parser, including `scanSpan` (the expression-boundary algorithm) and the per-context stop functions. |
| `ast.go` | AST node types: `FuncDecl`, `Param`, and every `Statement` (`VarDecl`, `Assign`, `ExprStmt`, `Return`, `Block`, `IfChain`, `For`, `Switch`, `Try`, `Throw`, `Defer`, `Break`, `Continue`). |
| `aliases.go` | `collectTypeAliases` / `resolveTypeAliases` — the project-wide, order-independent `type Name = <type>` pass that runs before the main parse. |
| `directives.go` | `Directive`, `collectLeadingDirectives`, and Go-style `//namespace:name args` directive-comment parsing. |
| `strict.go` | Strict-typing model: `reqSource` (off/host/file), `combineReq` (tighten-only folding), and `interpretFunctyDirectives` for the `functy:strict`/`functy:require` directives. |
| `types.go` | The `TypeConstraint` interface and its implementations, plus the `TypeResolver`/`typeEnv` that resolves an HCL type expression (including named capsule and open predicate types) into a constraint. |
| `scope.go` | `Scope` — the chained lexical scope, `binding` slots with optional pinned type constraints, and the `dirty` flag that drives eval-context caching. |
| `signal.go` | `Signal` / `SignalKind` — the non-local control-flow values (`return`/`break`/`continue`/`error`) that propagate out of statement execution. |
| `interp.go` | The tree-walking interpreter (`interp`, `execBlock`, per-statement executors) and `scopeEvalContext`, which merges a scope into the host eval context. |
| `builder.go` | `Compile` and `BuildFunction` — turn a `FuncDecl` into a cty `function.Function`, handling required/optional/variadic parameters, defaults, defers, and return-type coercion. |
| `errors.go` | functy error values: `errorValue`/`errValueFromDiags` construction, the open `error` type predicate, and `errorMessage` extraction at the function boundary. |

The CLI lives under `cmd/functy/` (`main.go`, `check.go`, `run.go`, `load.go`,
`baseline.go`, `output.go`) and is the only consumer of cobra.

## The expression-boundary algorithm

This is what you most need to understand before touching the parser.

functy parses statements itself, but each embedded expression must be handed to
`hclsyntax.ParseExpression`, which requires a byte slice containing **exactly one**
expression — it errors on trailing tokens. So for every expression position the
parser must find precisely where that expression ends in the source, then slice
`src[start:end]` and parse it. That work is `scanSpan` in `parser.go`.

`scanSpan` scans forward from the current token, tracking state, until a
context-specific `stop` function reports a boundary **at bracket depth 0**:

1. Record the start byte offset and source position of the current token.
2. Advance token by token. Increment depth on `(` `[` `{` and decrement on
   `)` `]` `}`. (Only these three bracket pairs count — template braces inside
   strings and heredocs are distinct HCL token types and are deliberately *not*
   tracked, because HCL's lexer already balanced them inside the string token.)
3. At depth 0, ask the `stop` function whether this token ends the expression in
   this context. The contexts and their terminators are:
   - **statement** (`stopStmt`): a `;`, a significant newline, or a block-opening
     `{`;
   - **condition** (`stopCond`, used by `if`/`for`/`while`/`switch` headers): a
     block-opening `{`, or `;`;
   - **argument/element** (`stopArg`): a `,` or a closing `)` `]`;
   - **type annotation** (`stopType`): `=`, `,`, `;`, a newline, or a `{`.
4. The expression is `src[start:end]`, parsed with
   `hclsyntax.ParseExpression(src[start:end], filename, startPos)`. Passing the
   original `startPos` keeps diagnostics pointed at the real source location.

Two subtleties make the depth-0 rule actually work:

**Balanced brackets disambiguate the block opener.** An expression's own brackets
and object literals are balanced *within* its span, so the first unbalanced `{` at
depth 0 is unambiguously the block opener, never part of the expression. This is
why `for k, v in { a = 1 } { … }` parses correctly: the object literal opens and
closes at depth 1, and the loop body's `{` is the first `{` seen at depth 0, which
terminates the collection expression.

**Operand vs. complete position (`prevCompletes`).** A newline at depth 0 is
ambiguous: it can terminate a statement, or it can be a line continuation inside a
multi-line expression. `scanSpan` tracks whether the *previous* token could
legally complete an expression (`completesExpr`: an identifier, a number, a closing
bracket, a closing quote/heredoc). A statement- or type-context stop only treats a
newline (and, for statements, a `{`) as a terminator when the previous token
completes an expression — otherwise it is a continuation, because an operator,
comma, opening bracket, or `${` introducer cannot end an expression. This lets
expressions wrap across lines after a binary operator without an explicit
continuation marker.

**Ternary depth (`tern`).** A `:` is overloaded: it separates the arms of an HCL
ternary (`c ? a : b`) but is *not* a structural token functy itself uses inside an
expression. `scanSpan` counts `?` (entering a ternary) and matching `:` (leaving
one) at depth 0 so a ternary's `:` is never mistaken for anything else and the
expression's two arms stay inside one span.

One small span fixup: when the stopping token is the terminating newline,
`scanSpan` includes that newline in the returned span. A heredoc close marker must
be followed by a newline for HCL to recognize it, so the trailing newline has to be
inside the slice; for any other expression it is harmless trailing whitespace. The
newline token is still left at the parser position for the caller to consume as the
statement terminator.

## Conventions

**Significant-newline handling.** functy is newline-sensitive: a newline at the end
of a statement terminates it, but a newline mid-expression is a continuation. The
distinction is made entirely by `prevCompletes`/`completesExpr` in `scanSpan` (see
above) and by `continuesLine` in the lexer. When you add a new operator or token,
make sure both classifiers agree on whether a newline after it should continue the
line.

**Scope dirty-flag eval-context caching.** Building the merged `*hcl.EvalContext`
for an expression (flattening the scope chain into a variable map and overlaying it
on the host context) is not free. The interpreter caches that context per block and
rebuilds it only when a binding visible to the scope may have changed. `Scope.Set`,
`Scope.Declare`, and `NewScope` mark the scope `dirty`; `execBlock` rebuilds the
context when it sees `dirty`, then clears the flag. Statements that *cannot* mutate
a binding — a bare expression statement, `return`, `break`, `continue`, `throw`,
`defer` — leave the flag clear, so a run of them reuses one context. If you add a
statement kind that can change a binding, make sure it is **not** in the
"cannot-mutate" set in `execBlock`, or stale values will leak.

**Signal-based control flow.** Non-local control flow is value-based, not
exception-based. `execBlock` and the per-statement executors return a `*Signal`
(`nil` means normal fall-through). `SignalReturn` carries a value and unwinds to
the function boundary; `SignalBreak`/`SignalContinue` unwind to the innermost loop;
`SignalError` unwinds a raised error until a `try`/`catch` handles it or it leaves
the function (where `BuildFunction` turns it into a Go `error`). Deferred
expressions run at function exit in LIFO order, after the body's outcome is
determined; a defer that raises replaces the in-flight outcome.

**Diagnostics.** Report problems as `hcl.Diagnostics` with a meaningful `Summary`,
`Detail`, and `Subject` range. The parser keeps recovering after an error
(best-effort), so a `Result` may be returned alongside error diagnostics — callers
must check `diags` before using it.

## Lineage

functy's runtime — the chained `Scope`, the `Signal` control-flow model, and the
tree-walking interpreter that evaluates lazy HCL expressions against a late-bound
eval context — was adapted from the `procedure` block of the
[Vinculum](https://github.com/tsarna/vinculum) project, which pioneered the idea of
an imperative front-end compiling down to a cty `function.Function`. The design was
**copied and then evolved independently**; functy is a standalone module with no
dependency on Vinculum, and the two have diverged. The shared ancestry is historical
context, not a live coupling. Vinculum will be adopting functy and deprecating its
`procedure` block at any rate.

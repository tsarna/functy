# functy

An imperative language whose values are [cty](https://github.com/zclconf/go-cty)
values and whose expressions are [HCL](https://github.com/hashicorp/hcl).

*Pronounced **funk-tie** — the `-ty` rhymes with
[cty](https://github.com/zclconf/go-cty) ("see-tie"). But I won't be mad if you
pronounce it **funk-tee**. I sometimes forget too.*

functy is **not** aiming to compete with [Starlark](https://github.com/google/starlark-go), [Tengo](https://github.com/d5/tengo), or any of the various JavaScript or Lua implementations for Go. It is intended specifically for use in software that is already relying on HCL / Cty but which needs more expressive power than eg the [HCL user function add-on](https://github.com/hashicorp/hcl/tree/main/ext/userfunc).

[go-cty](https://github.com/zclconf/go-cty) describes itself this way:

> One could think of cty as being the reflection API for a language that doesn't
> exist, or that doesn't exist yet.

functy aims to be that language.

[![CI](https://github.com/tsarna/functy/actions/workflows/ci.yml/badge.svg)](https://github.com/tsarna/functy/actions/workflows/ci.yml)

## Overview

functy is a small Go-inspired imperative language that compiles to ordinary cty
`function.Function` values. You write functions with familiar control flow —
`if`/`else`, `for`/`while`, `switch`, `try`/`catch` — and **every expression is a
real HCL expression**: operators, string templates (`"hello ${name}"`), function
calls, conditionals, and the cty type-constraint grammar all behave exactly as
they do in HCL/Terraform.

The result is a thin, honest imperative skin over cty + HCL: its values are cty
values, its types are cty types, and the functions it produces are callable from
any HCL evaluation context — alongside the host application's own functions.

```functy
func classify(n: number) -> string {
    if n > 0 {
        return "positive"
    } else if n < 0 {
        return "negative"
    } else {
        return "zero"
    }
}

func greet(name: string = "world") -> string {
    return "hello ${name}"
}
```

functy source files use the `.cty` extension.

## Installation

As a library:

```sh
go get github.com/tsarna/functy
```

The library depends only on `github.com/hashicorp/hcl/v2` and
`github.com/zclconf/go-cty`.

As a CLI:

```sh
go install github.com/tsarna/functy/cmd/functy@latest
```

## CLI quick start

```console
$ cat > math.cty <<'EOF'
func add(a: number, b: number) -> number {
    return a + b
}
func main() -> number {
    return add(2, 3)
}
EOF

$ functy run math.cty
5

$ functy run math.cty --func add -- 2 3
5

$ functy check math.cty
ok

$ functy test math.cty        # run co-located test "..." { ... } blocks
ok   add sums two numbers
1 passed, 0 failed
```

Sources can carry co-located tests — `test "description" { ... }` blocks whose bodies
use `assert` — run with `functy test` (or a host's `(*Result).RunTests`); a failing
test reports the source line and operand values.

See [doc/cli.md](doc/cli.md) for the full CLI reference,
[doc/language.md](doc/language.md) for the language reference, and
[doc/language.md#tests](doc/language.md#tests) for inline tests.

## Library usage

Parse a source, compile it against a late-bound eval context, and call the
resulting functions:

```go
package main

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/tsarna/functy"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

func main() {
	src := []byte(`func add(a: number, b: number) -> number { return a + b }`)

	res, diags := functy.NewParser().Parse(src, "add.cty")
	if diags.HasErrors() {
		panic(diags.Error())
	}

	// The eval context is late-bound so functions can call one another (and
	// reference host globals finalized later).
	var ctx *hcl.EvalContext
	funcs, diags := res.Compile(func() *hcl.EvalContext { return ctx })
	if diags.HasErrors() {
		panic(diags.Error())
	}
	ctx = &hcl.EvalContext{
		Functions: funcs,
		Variables: map[string]cty.Value{},
	}

	out, err := funcs["add"].Call([]cty.Value{cty.NumberIntVal(2), cty.NumberIntVal(3)})
	if err != nil {
		panic(err)
	}
	fmt.Println(out.AsBigFloat()) // 5
}
```

A host typically merges the compiled functions with its own function library and
ambient values into one eval context. `functy.ParseSources` collects `.cty`
sources from file paths, directories, or an `embed.FS`; `Parser.ParseAll`
parses several sources into one `Result`.

When a call returns an error, an uncaught functy `throw`/`assert` surfaces as a
`*functy.ThrownError`; `errors.As(err, &te)` recovers it, and `te.Diagnostics()`
(or `functy.ErrorDiagnostics(value)`) yields `hcl.Diagnostics` you can hand to
`hcl.NewDiagnosticTextWriter` to print the failing source line — and, for a failed
`assert`, the operand values — with source context instead of a flat message.

functy also ships a small standard library of expression builtins —
`functy.Stdlib()` (`typeof`, `typekind`, `cond`, `switch`, `error`, `assert`) and
the opt-in `functy.StdlibExtras()` (`try`, `can`) — for a host to merge into that same
context. See [doc/stdlib.md](doc/stdlib.md).

A host can also register its own named types so they can be used in annotations:

```go
p := functy.NewParser().
    RegisterType("bus", busCapsuleType).            // identity-enforced capsule type
    RegisterOpenType("ctx", isContextObject)        // predicate-backed open type
```

Identity types must match by type; open types must satisfy a predicate and pass
through untouched (extra attributes preserved). See
[doc/language.md](doc/language.md#named-and-open-types-host-registered).

### Type system as a reusable component

functy's type system is usable on its own, independent of parsing `.cty`
programs — as a richer alternative to `ext/typeexpr` (the result is a
`TypeConstraint` that can *enforce* values via `Coerce`, not just a `cty.Type`),
or for a host to type-check its own configuration:

```go
r := functy.NewTypeResolver().RegisterType("bus", busCapsuleType)

// Resolve a type annotation (from a string, or an hcl.Expression):
tc, diags := r.ParseType([]byte("list(string)"), "config")
// tc.Cty()  -> cty.List(cty.String)
// tc.Coerce(value) -> the value converted/validated, or an error

// ResolveType(expr) takes an already-parsed hcl.Expression (e.g. a decoded HCL
// attribute) — the typeexpr.TypeConstraint analog.
```

A `Parser` holds a `TypeResolver` (`Parser.Types()`) and registers named types on
it, so a host registers its capsule/open types once and uses them both for parsing
`.cty` files and for resolving standalone annotations.

## Status

functy implements a complete core language: typed and dynamic variables,
reassignment, all control-flow forms, variadic and optional parameters,
structured error handling (`try`/`catch`/`finally`, `throw`, `defer`, and
`val, err = expr` error capture), a `null` (void) return type, and the
`functy run` / `functy check` CLI commands.

Types are resolved by functy's own resolver (not `ext/typeexpr`), with a
host-pluggable named-type environment for capsule and open types, `type` aliases,
and opt-in strict typing.

Designed-but-not-yet-implemented features (a REPL, a formatter, module imports,
closures, nested *open* types, ...) are recorded in
[FUTURE.md](FUTURE.md). The design rationale ("why a language, not an embedded
one") is in [DESIGN.md](DESIGN.md), and the internal architecture is in
[CONTRIBUTING.md](CONTRIBUTING.md).

## License

MIT, like cty itself — see [LICENSE](LICENSE).

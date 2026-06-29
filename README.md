# functy

An imperative language whose values are [cty](https://github.com/zclconf/go-cty)
values and whose expressions are [HCL](https://github.com/hashicorp/hcl).

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

```
go get github.com/tsarna/functy
```

The library depends only on `github.com/hashicorp/hcl/v2` and
`github.com/zclconf/go-cty`.

As a CLI:

```
go install github.com/tsarna/functy/cmd/functy@latest
```

## CLI quick start

```
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
```

See [doc/cli.md](doc/cli.md) for the full CLI reference and
[doc/language.md](doc/language.md) for the language reference.

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

## Status

functy implements a complete core language: typed and dynamic variables,
reassignment, all control-flow forms, variadic and optional parameters,
structured error handling (`try`/`catch`/`finally`, `throw`, `defer`), and the
`functy run` / `functy check` CLI commands.

Designed-but-not-yet-implemented features (a REPL, a formatter, `type` aliases,
module imports, closures, ...) are noted where relevant in the
documentation.

## License

MIT — see [LICENSE](LICENSE), like cty itself.

# functy CLI

The `functy` binary loads and runs `.cty` source files. It exists for
development and experimentation, not production: a host application links the
functy library directly and supplies its own richer eval context. The CLI's
baseline context is the cty standard library plus `print`/`println`, and
deliberately lacks host-specific functions or ambient values.

```
go install github.com/tsarna/functy/cmd/functy@latest
```

```
functy run [--func NAME] [--output json|hcl|raw] FILE... [-- ARG...]
functy check FILE...
functy test FILE...
```

## run

Load the given source files into one eval context and invoke an entry function.

```
$ functy run examples/math.cty
5

$ functy run examples/math.cty --func add -- 2 3
5

$ functy run examples/greet.cty --func greet --output raw -- '"alice"'
hello alice
```

- **Entry point.** `main` by default; `--func NAME` selects another. It is an
  error if the chosen function is absent.
- **Files vs. arguments.** Positional arguments before `--` are source files;
  everything after `--` is passed to the entry function. With no `--`, all
  positionals are files and the entry function is called with no arguments.
- **Argument evaluation.** Each `ARG` is evaluated as an HCL expression, so `42`,
  `'"text"'`, `'[1, 2, 3]'`, and `true` all work (mind shell quoting for
  strings). The resulting value is converted to the corresponding parameter's
  declared type.
- **Output.** The return value is printed — JSON by default; `--output` selects
  `json`, `hcl`, or `raw`. A `null` return prints nothing. `raw` prints a string
  result unquoted and falls back to JSON for other types.

### Top-level constants

`functy run` enables top-level `const` and `var` declarations and evaluates them
into the run context, so functions can reference constants declared near their
point of use. Declarations may reference one another in any order:

```functy
const tau: number = pi * 2
const pi = 3.14159

func circumference(r: number) -> number {
    return tau * r
}
```

Because there is no way to update global variables, they are functionally equivalent to constants in the CLI.

### Baseline functions

The run context provides a subset of the cty standard library (string,
collection, number, and encoding functions such as `upper`, `merge`, `keys`,
`length`, `jsonencode`, `jsondecode`, …) plus two conveniences:

- `print(...)` — write arguments to stdout (strings unquoted, others as JSON).
- `println(...)` — like `print`, with a trailing newline.

Both return `null`.

## check

Parse and type-check the source files without running them. Diagnostics are
printed with source context; the command exits non-zero if any are errors.

```
$ functy check examples/greet.cty
ok

$ functy check broken.cty
Error: break outside loop

  on broken.cty line 2:
   2:   break

break may only be used inside a for or while loop.
```

## test

Run every `test "..." { ... }` block defined in the source files (see
[language.md](language.md#tests) for the block itself). A test **passes** if its body
runs to completion and **fails** if an error — a failed `assert`, a `throw`, or an
evaluation error — unwinds out of it. A failure is printed with source context (the
failing line and, for a failed `assert`, the operand values); the command exits
non-zero if any test fails.

```
$ functy test examples/math.cty
ok   add sums positives
FAIL add handles negatives
Error: sum should be positive

  on examples/math.cty line 9:
   9:     assert(sum > 0, "sum should be positive")

sum = -3

2 passed, 1 failed
```

## Not yet implemented

A REPL (`functy repl`), a formatter (`functy fmt`), and additional output and
diagnostic options are planned but not part of this build.

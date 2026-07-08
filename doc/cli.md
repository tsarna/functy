# functy CLI

The `functy` binary loads and runs `.cty` source files. It exists for
development and experimentation, not production: a host application links the
functy library directly and supplies its own richer eval context. The CLI's
baseline context is the cty standard library plus `print`/`println`, and
deliberately lacks host-specific functions or ambient values.

```sh
go install github.com/tsarna/functy/cmd/functy@latest
```

```text
functy run [--func NAME] [--output json|hcl|raw] [--json] FILE... [-- ARG...]
functy repl [--func NAME] [FILE...] [-- ARG...]
functy check [--json] FILE...
functy test [--run PATTERN] [-v] [--json] [FILE...]
functy fmt [-w] [-l] [FILE|DIR ...]
```

## run

Load the given source files into one eval context and invoke an entry function.

```console
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
- **`--json` (diagnostics).** Emits any diagnostics — a compile error, a bad
  argument, or a runtime `throw` / failed `assert` — to **stderr** as a
  machine-readable report instead of the human-readable text, for editor tooling.
  Every verb's `--json` report goes to stderr, so `run`'s **stdout** stays reserved
  for the program's own output (`print` / `println`) and the return value, both
  left untouched by `--json` (`--output` still controls the value's format). On
  failure stderr is a single well-formed object and the exit status is non-zero; on
  success stderr is empty. The report shape matches `check --json`:

  ```console
  $ functy run --json broken.cty -- -3
  {
    "diagnostics": [
      {
        "severity": "error",
        "summary": "must be positive",
        "detail": "n = -3",
        "location": { "file": "broken.cty", "line": 3, "column": 12, "end_line": 3, "end_column": 21 }
      }
    ]
  }
  ```

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
`length`, `jsonencode`, `jsondecode`, …), functy's own stdlib (`typeof`, `cond`,
`switch`, `try`, …), plus a few conveniences:

- `print(...)` — write arguments to stdout (strings unquoted, others as JSON).
  Returns `null`.
- `println(...)` — like `print`, with a trailing newline. Returns `null`.
- `help(name)` / `help()` — a function's signature and docs by name, or the sorted
  names of all available functions when called with no argument (see
  [stdlib.md](stdlib.md#helpfuncfuncs-evalctxfn--context-aware)). Handy in the REPL.
- `doc(name)` — a function's one-line description by name (`null` if no such
  function).

## check

Parse and type-check the source files without running them. Diagnostics are
printed with source context; the command exits non-zero if any are errors.

```console
$ functy check examples/greet.cty
ok

$ functy check broken.cty
Error: break outside loop

  on broken.cty line 2:
   2:   break

break may only be used inside a for or while loop.
```

- **`--json`** emits a single machine-readable JSON report of the diagnostics to
  **stderr** instead of the human-readable output — for editor tooling (e.g. the
  VSCode extension's Problems panel). The exit status is unchanged. The report is one
  object with a `diagnostics` array; a clean file yields an empty array, so a
  consumer can always parse stderr. (Every verb's `--json` report goes to stderr, so
  stdout is uniformly free for program output.)

  ```console
  $ functy check --json broken.cty
  {
    "diagnostics": [
      {
        "severity": "error",
        "summary": "break outside loop",
        "detail": "break may only be used inside a for or while loop.",
        "location": { "file": "broken.cty", "line": 2, "column": 3, "end_line": 2, "end_column": 8 }
      }
    ]
  }
  ```

  Each entry carries a `severity` (`"error"` or `"warning"`), a `summary`, an optional
  `detail`, and (when the diagnostic has a source range) a 1-based `location` with
  `file`/`line`/`column`/`end_line`/`end_column` — enough to map onto a precise editor
  diagnostic range without scraping the text output.

## test

Run every `test "..." { ... }` block defined in the source files (see
[language.md](language.md#tests) for the block itself). A test **passes** if its body
runs to completion, is **skipped** if it calls `skip(...)`, and **fails** if any other
error — a failed `assert`, a `throw`, or an evaluation error — unwinds out of it. A
failure is printed with source context (the failing line and, for a failed `assert`,
the operand values); the command exits non-zero only if a test **fails** (a skip is not
a failure).

With no `FILE` arguments, `test` discovers `.cty` files in the current directory
(recursively, skipping dot-directories) — equivalent to `functy test .`.

By default the output is **quiet** — only failures are printed, followed by a summary:

```console
$ functy test examples/math.cty
FAIL add handles negatives (117µs)
Error: sum should be positive

  on examples/math.cty line 9:
   9:     assert(sum > 0, "sum should be positive")

sum = -3

2 passed, 1 failed, 0 skipped
```

- **`-v` / `--verbose`** lists every test with its duration — `ok`, `SKIP` (with its
  reason), and `FAIL`:

  ```console
  $ functy test -v examples/math.cty
  ok   add sums positives (49µs)
  FAIL add handles negatives (55µs)
  …
  SKIP work in progress: not implemented yet (2µs)

  1 passed, 1 failed, 1 skipped
  ```

- **`--run PATTERN`** runs only the tests whose description matches the regular
  expression `PATTERN`; the summary notes how many were deselected:

  ```console
  $ functy test --run sums examples/math.cty
  1 passed, 0 failed, 0 skipped (2 deselected by --run)
  ```

- **`--json`** emits a single machine-readable JSON report to **stderr** instead of
  the human-readable output — for CI and editor tooling (e.g. a Test Explorer). The
  exit status is unchanged. The report goes to stderr so that a test's own
  `print` / `println` output (which stays on stdout) never corrupts it. The report is
  one object with a `tests` array (every test that ran, regardless of `-v`) and a
  `summary`:

  ```console
  $ functy test --json examples/math.cty
  {
    "tests": [
      {
        "name": "add sums positives",
        "status": "passed",
        "duration_ms": 0.049,
        "location": { "file": "examples/math.cty", "line": 4, "column": 1, "end_line": 6, "end_column": 2 }
      },
      {
        "name": "add handles negatives",
        "status": "failed",
        "duration_ms": 0.055,
        "location": { "file": "examples/math.cty", "line": 8, "column": 1, "end_line": 10, "end_column": 2 },
        "failure": {
          "message": "sum should be positive",
          "detail": "sum = -3",
          "location": { "file": "examples/math.cty", "line": 9, "column": 12, "end_line": 9, "end_column": 21 }
        }
      },
      {
        "name": "work in progress",
        "status": "skipped",
        "duration_ms": 0.002,
        "location": { "file": "examples/math.cty", "line": 12, "column": 1, "end_line": 12, "end_column": 40 },
        "skip_reason": "not implemented yet"
      }
    ],
    "summary": { "passed": 1, "failed": 1, "skipped": 1, "deselected": 0 }
  }
  ```

  Each test entry carries a `status` (`"passed"`, `"failed"`, or `"skipped"`), a
  `duration_ms`, and the test block's `location`. A failed test adds a `failure` with
  the assert/throw `message`, its operand `detail` (when present), and the `location`
  to underline; a skipped test adds its `skip_reason` (when given). Ranges are
  1-based. A compilation failure still emits a well-formed report (with an empty
  `tests` array) and exits non-zero, so consumers can always parse stderr.

## fmt

Format `.cty` source into a canonical style. With no paths (or `-`) it reads
stdin and writes the result to stdout:

```sh
functy fmt < messy.cty
cat messy.cty | functy fmt -
```

Given files or directories (directories are walked for `.cty` files, skipping
dot-directories), the default prints the formatted source to stdout; the flags
change that:

- **`-w`** — rewrite each file in place (only files whose formatting changes).
- **`-l`** — list the files whose formatting differs; do not print or rewrite.

```sh
functy fmt -w examples/   # reformat every .cty under examples/
functy fmt -l .           # which files are not canonically formatted?
```

The formatter reindents to four spaces, normalizes spacing (expressions via
`hclwrite`, so they match `terraform fmt`), collapses runs of blank lines to one,
and preserves comments and doc comments. It reformats a file only if it parses
cleanly — a file with syntax (or, in the standalone CLI, unresolved-type) errors is
reported and left untouched, so fmt never drops or reorders code. A host that
registers named types can format its own files via `(*functy.Parser).Format`.

## repl

Start an interactive REPL over the loaded context: `functy repl [FILE...]` (or
`functy run -i [FILE...]`). Files are optional — with none, the REPL still exposes
the baseline context. Given files, it loads them into one eval context, runs the
entry function if present, then drops into the prompt. `:help` lists the REPL's
meta-commands; the `help()` / `doc()` [baseline functions](#baseline-functions)
introspect the available functions from inside the session.

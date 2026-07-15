# functy as an OpenTofu symbol library

OpenTofu has an in-draft RFC, **Symbol Libraries**
([opentofu/opentofu@rfc_symbol_libraries](https://github.com/opentofu/opentofu/blob/rfc_symbol_libraries/rfc/20260424-symbol-libraries.md)),
that adds reusable **types**, **values**, and **functions** shared across modules. This
file is a design record for a single, blunt proposal: **make the symbol library format
functy `.cty`, not the RFC's `.sym.hcl` block dialect.** It deliberately **mirrors the
RFC's section structure and reuses its example identifiers** (`simple_type`, `add`,
`divide`, `greeting`, `vec3_length`, `non_empty`, `default_items`, `main.tofu`) so the two
can be laid side by side.

This is a *different* proposal from the provider path in
[`OPENTOFU-PROVIDER.md`](OPENTOFU-PROVIDER.md). The provider carries **functions only**
through the RPC layer and is buildable today; this carries **all three symbol kinds** by
replacing the library's authoring language outright.

---

## Symbol Libraries

The RFC identifies three reuse problems the module system cannot solve without heavy
workarounds. Each maps directly onto a functy top-level declaration kind that already
exists — so where the RFC invents a block form, functy already has a language form:

| RFC symbol | RFC syntax | functy construct |
| --- | --- | --- |
| **type** | `typedef "t" { type = … }` | `type T = …` |
| **value** | `values { k = expr }` | `const k = expr` |
| **function** | `function "add" { parameter "a" {type=number} … return = param.a + param.b }` | `func add(a: number, b: number) -> number { return a + b }` |

**A `.cty` file is already a symbol library — authored in a language instead of a schema.**

### Types

*The RFC problem:* engineers cannot reuse a custom type definition across modules without
copy-paste or codegen. *functy:* `type` aliases, already project-scoped across every `.cty`
loaded together, producing a `TypeConstraint` that can *enforce* a value via `Coerce`, not
just name a `cty.Type`.

### Logic

*The RFC problem:* a reusable expression requires standing up a whole module of
`variables` + `locals` + `outputs`. *functy:* `func`, compiling to an ordinary
`cty.Function` with a real static signature — type errors caught at *validate*, and with
genuine control flow and recursion, not a single `return =` expression.

### Values

*The RFC problem:* sharing a constant needs an external tool or orchestration layer.
*functy:* `const`, collected with cross-references resolved in any order.

---

## Proposed Solution

### User Documentation

#### Symbol File Contents

The RFC's `.sym.hcl` becomes an ordinary `.cty` file. Every RFC example below is
reproduced with the same names.

**Types.** Where the RFC writes `symbols::types(simple_type)` to reference another typedef,
functy uses the **bare type name** — because it owns its type resolver instead of
delegating to `ext/typeexpr`, a qualified `types()` wrapper is unnecessary within .cty files:

```functy
type simple_type = number

type complex_type = object({
    ncpus       = number
    memory_size = simple_type            // RFC: symbols::types(simple_type)
})

type defaults_type = object({
    complex = optional(complex_type, { ncpus = 1, memory_size = 1024 })
})
```

**Values.** The RFC's `values {}` block with its `value.custom_regex` self-reference
becomes plain `const`s referring to each other by bare name:

```functy
const simple       = 10
const custom_regex = "<some complex regex>"
const upper_regex  = upper(custom_regex)  // RFC: upper(value.custom_regex)
```

**Functions.** The RFC's `function "add" {}` — a `description`, a `type`, two
`parameter` blocks, and a `return` — is one signature line; the descriptions become doc
comments, which functy attaches to the resulting `cty.Function`:

```functy
// add two numbers together
func add(a: number, b: number) -> number {
    return a + b
}
```

The RFC's per-parameter `validation {}` block (`divide`) is an ordinary body statement:

```functy
func divide(a: number, b: number) -> number {
    assert(b != 0, "Divide by zero")     // RFC: parameter "b" { validation { … } }
    return a / b
}
```

Variadic parameters (`greeting`) are `*name`, and the RFC's `locals {}` intermediate is a
real local — or, here, inlined:

```functy
func greeting(prefix: string, *name: string) -> list(string) {
    return [for x in name : "${prefix} ${x}!"]
}
```

And a function using both a type and a sibling function (`vec3_length`) calls that sibling
by its **bare name** — no `symbols::` prefix is needed inside the unit, and functy's real
locals replace `locals {}`:

```functy
type vec3 = object({ x = number, y = number, z = number })

func vec3_length(vec: vec3) -> number {
    var xx = vec.x * vec.x
    var yy = vec.y * vec.y
    var zz = vec.z * vec.z
    return sqrt(add(add(xx, yy), zz))    // RFC: symbols::add(symbols::add(…))
}
```

(The RFC's `vec3_length` reads `param.x`/`param.y`/`param.z` from a parameter named `vec`;
functy's `vec.x` is unambiguous, so the slip does not arise.)

#### How do I have "unexported" symbols?

The RFC answers this with a Go-style `internal/` **directory** and a re-export dance: an
`./internal/types.sym.hcl` defines `custom`, and `./contents.sym.hcl` imports it and
re-exports it under a public name (`typedef "custom" { type = symbols::internal::types(custom) }`).

functy's visibility unit is the **name**, not the file, so the whole two-file dance would
collapse to a leading underscore — reusing the same `_` convention that already makes
functions namespace-local:

```functy
// lib.cty
type _spec = object({ id = string, size = number })   // intended: withheld on export
type items = list(_spec)                                // exported; inlines _spec's structure
```

The **inlining is real today**: `items` resolves to a concrete `list(object(...))`, exposing
the structure and not the private name. The **export filter is not yet** — unlike functions,
`_`-privacy is currently unwired for type aliases (`TypeAlias` has no `IsPrivate()`, and
there is no type-export path to withhold an alias from), so skipping `_`-prefixed types on
export is part of the same unbuilt projection work as namespaced type export (see the
*Namespace-scoped type aliases* item in [`FUTURE.md`](FUTURE.md), and *Open Questions*
below). The point stands regardless: no `internal/` directory, no import, no re-export —
visibility is a name prefix, not a file layout.

#### Symbol file usage

The RFC's `exported.sym.hcl` becomes `local-lib.cty` (Note, `in` is a for-expression keyword in
functy, so the parameter is spelled `xs` here):

```functy
// local-lib.cty
type items = list(string)

func non_empty(xs: items) -> bool {
    return alltrue([for x in xs : length(x) != 0])
}

func assert_non_empty(xs: items) -> items {
    assert(non_empty(xs), "One or more of the elements in ${jsonencode(xs)} is empty")
    return xs
}

const default_items: items = ["foo", "bar", "baz"]
```

The consumer's `main.tofu` is where the payoff shows: **the reference forms are OpenTofu's
own grammar, inherited unchanged, so the consumer side is nearly the same as the RFC's.**
Only the binding block differs:

```hcl
# main.tofu
symbols "lib" {
  source = "./local-lib"          # a .cty unit
}

variable "my_items" {
  type = symbols::lib::types(items)
  validation {
    condition     = symbols::lib::non_empty(var.my_items)
    error_message = "One or more elements is empty"
  }
  default = symbols.lib.default_items
}

variable "my_items_unchecked" {
  type = symbols::lib::types(items)
}

locals {
  my_items_checked = symbols::lib::assert_non_empty(var.my_items_unchecked)
}

resource "provider_type" "ident" {
  value = local.my_items_checked
}
```

The binding block itself carries at most three attributes:

```hcl
symbols "lib" {
  source    = "./local-lib"       # module-style source; distribution is inherited
  namespace = "acme::net"         # optional: which functy namespace to expose; omit → global
}
```

- **`"lib"`** — the consumer's label, one identifier, unique in this config. It is a
  consumer-scope name, chosen at import exactly as the RFC's `symbols "…"` label already is
  — we don't try to derive it from functy's namespace (see *OpenTofu Integration*).
- **`source`** — reuses OpenTofu's module source addressing, so **distribution, versioning,
  and locking come for free**; only the parse step differs from a `.sym.hcl` library.
- **`namespace`** — selects which functy surface to bind; omitted, it binds the global
  (unnamespaced) export. **One block per surface.**

### Technical Approach

#### Symbol Library Implementation

**functy is the parser.** functy is a small, MIT-licensed Go library whose only
dependencies are `hashicorp/hcl/v2` and `zclconf/go-cty` — the two OpenTofu already builds
on. OpenTofu embeds it and parses each `.cty` symbol library into its symbol table
directly. There is no serialization step and no intermediate format: functy compiles `.cty`
straight into the three kinds of Go object a symbol library *is* —

- `type` → `cty.Type` (a `TypeConstraint`)
- `const` → `cty.Value`
- `func` → `cty.Function`

— which are exactly what the symbol table holds. The compiled objects go in as-is.

Because the library is evaluated *through functy's interpreter* rather than read as static
HCL, functy's functions keep their full power: local variables, `if`/`for`/`while`,
`try`/`catch`, and recursion — none of which the RFC's expression-only `function {}` block
(a single `return =`, recursion forbidden) can express. Replacing the authoring language is
what buys real functions, not just a terser spelling of the same limited ones.

**Projection.** functy namespaces are **flat siblings, not a containment tree** — `foo::bar`
is not "inside" `foo`. So a namespace's `::` structure must not be projected onto object
`.` traversal: given `namespace foo::bar { const answer }` and `namespace foo { const bar }`,
a nested `symbols.foo.bar` would have to be both an object and a string, which cty rejects.
Instead, **each namespace crosses as one flat object under one label** — the whole namespace
name collapses to one opaque segment; `::` never becomes `.`. This matches the RFC's own
flat, one-level `values {}` shape, so it is the native fit rather than a compromise.

#### OpenTofu Integration

The three reference forms are OpenTofu's parser, inherited unchanged, and functy's three
kinds land on them cleanly:

- **Values** — `symbols.lib.default_items` (dotted, flat). Only `const` crosses; `var` is
  runtime-mutable and OpenTofu evaluates once. Consts are pre-evaluated in functy's own
  context into self-contained `cty.Value`s. Values live in the `.` space and functions in
  the `::` space, so a `const x` and a `func x` never collide.
- **Functions** — `symbols::lib::non_empty(…)`, with a real static signature, so
  `symbols::lib::divide(1, "0")` fails at *validate*.
- **Types** — `symbols::lib::types(items)`, the RFC's pseudo-call. functy authors types as
  **bare names**; the `types()` wrapper is inherited only at the *reference* site, where it
  exists because HCL's `::` is a function-call selector a bare qualified type-name cannot
  use. functy never asks an author to write it.

**The label is consumer-declared, not functy-derived** — a consumer-scope local name, like
a module instance name. functy cannot derive it: a namespace name lives in a richer space
than one HCL identifier (last-segment is non-injective, the full name contains `::`, any
underscore-flattening is ambiguous), and uniqueness is a property of the consumer's whole
import set, which functy cannot see.

### Open Questions

- **Execution safety at plan time.** functy's imperative power is exactly why it needs the
  **execution limits** from [`FUTURE.md`](FUTURE.md) before it is safe as a plan-time
  source — a runaway loop or unbounded recursion would wedge `plan`. The RFC's
  expression-only, non-recursive functions get termination for free; functy must bring a
  step/time budget.
- **Namespaced type export depends on unfinished functy work.** Type aliases are currently
  project-scoped (one flat space across the unit), because `::` cannot appear in a type
  annotation — the *Namespace-scoped type aliases* item in `FUTURE.md`. Until it lands,
  exported type names must be unique across the unit, a reason to prefer the global export
  for types in the interim.
- **Aliased-surface discovery.** Whether OpenTofu would parse the library per aliased
  binding (so several `namespace =` blocks each get their own table) versus once per source
  is the same discovery-vs-routing question flagged for the provider path, and wants a spike.
- **Type identity.** The RFC's open question about "identical definitions under different
  names" largely **dissolves** under functy, because cty types are structural:
  `symbols::a::types(Spec)` and `symbols::b::types(Spec)` with identical structure are
  already interchangeable.

### Future Considerations

**What is taken from the RFC, and what is left behind:**

| Taken (harmonizes with functy) | Left behind (needless conformance) |
| --- | --- |
| The three-kind decomposition — it *is* functy's | `.sym.hcl` files and the `typedef`/`values{}`/`function{}` blocks |
| The `symbols.lib.*` / `symbols::lib::*` reference surface (OpenTofu's grammar) | Hand-authored `types()` and per-parameter `validation {}` |
| Import-time aliasing — the consumer-chosen local label | File-naming imports (`file =`), flat-within-a-library namespacing |
| Module-style distribution (source addressing, versioning, locking) | Nested `symbols.a.b.c` projection of `::` names |
| Exported-vs-internal visibility — done with `_`, not an `internal/` directory | The `internal/` re-export dance |

**The proposal, stated plainly:** the symbol library format is functy `.cty`. OpenTofu
embeds functy (a small `hcl`+`cty`-only library) as the parser, and a symbol library is a
`.cty` unit rather than a set of `.sym.hcl` block files. Everything else in the RFC — the
`symbols "…"` binding block, the `symbols.lib.*` / `symbols::lib::*` reference grammar,
module-style distribution — is kept as-is.

**The prototype**, if pursued, is small and reuses existing machinery: `.cty`
libraries parsed with `functy.ParseSources`, each namespace projected to a flat labeled
symbol set, consts pre-evaluated, and the `cty.Type` / `cty.Value` / `cty.Function` table
handed to OpenTofu.
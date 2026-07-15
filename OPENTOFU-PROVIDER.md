# A functy provider for OpenTofu

**This is the one Terraform-adjacent idea for functy that is buildable today.** It needs
no core change, no fork, and nobody's permission — and unlike every other route (see
`TERRAFORM.md` for those), it preserves functy's central value: the no-build edit→run
loop. Drop a `.cty` file, and its functions are live.

> **OpenTofu 1.7 added "an OpenTofu-only feature to let providers dynamically define
> custom functions based on your configuration."** A provider handed `file("./lib.cty")`
> can therefore mint one real, statically-typed OpenTofu function per functy function.
> Two proof-of-concept providers already do exactly this with other languages.

Nothing here is planned, and no code has been written. This file is a **design record**:
what the mechanism is, why functy fits it better than the incumbents, the two design
questions that needed answers (namespaces, and the ambient environment), and what a
prototype would have to settle. It is kept separate from `TERRAFORM.md` because the two
have diverged: that file analyzes a question that is *blocked*, while this one describes
something that could be started tomorrow.

**Terraform cannot do this.** Its framework deliberately withholds provider configuration
from function implementations, so the whole mechanism below is unavailable there. See
`TERRAFORM.md` for what Terraform allows (a stringly-typed `eval("<source>", {args})`) and
for the governance history behind both tools' positions.

## What already exists: the language providers in the wild

The single most useful thing learned about this space is that **other people have already
built it**, and their choices map the design space exactly. Everything below was verified
from source and docs, not from memory.

**The static-schema constraint, and the one place it does not apply.** Terraform requests
a provider's *function list* as static schema, before and independently of provider
configuration — HashiCorp's framework docs state that functions are resolved before
provider configuration, *"which the framework enforces by not exposing provider
configuration data to function implementations."* A Terraform provider therefore cannot be
told "load `./funcs/*.cty` and expose whatever they define."

**OpenTofu split the protocol and lifted exactly that restriction.** `GetProviderSchema()`
returns the initial function list, but a configured provider may supply *additional*
functions via `GetFunctions()`, which is called **after** `ConfigureProvider`. The 1.7.0
release notes: *"we added an **OpenTofu-only feature** to let providers dynamically define
custom functions based on your configuration."* This is the hinge on which this entire
document turns.

Four projects, and what each one chose:

- **[opentofu/terraform-provider-go](https://github.com/opentofu/terraform-provider-go)**
  — the one to study; it is functy's design, executed with Go. Uses Yaegi (Traefik's Go
  interpreter), takes `provider "go" { go = file("./lib.go") }`, and mints one OpenTofu
  function per exported Go function — **with real derived signatures**: *"all of this is
  type-safe and mistakes will be caught by tofu. So passing a number to the function will
  fail with `object required`."* Type errors at validate time, not apply time. Call site:
  `provider::go::hello("papaya")`. OpenTofu 1.7+ only.
- **[opentofu/terraform-provider-lua](https://github.com/opentofu/terraform-provider-lua)**
  — same mechanism, cruder: it discovers function *names* by running
  `regexp.MustCompile("function (.*)\\(")` over the Lua source. It knows the names but not
  the arities or types, so everything is variadic-dynamic. Also ships a portable
  `provider::lua::exec(code, args)` for stock Terraform.
- **[ms-henglu/terraform-provider-starlark](https://github.com/ms-henglu/terraform-provider-starlark)**
  — the portable shape: a single `eval(script, inputs) → dynamic`, script passed as a
  string. Works on Terraform *and* OpenTofu. There is no return *type*, only a convention
  (the script must assign to a global named `result`).
- **[northwood-labs/terraform-provider-corefunc](https://github.com/northwood-labs/terraform-provider-corefunc)**
  — the contrast case: no embedded language at all, just a fixed catalog of hand-written
  Go functions with real static types.

**The gap nobody has filled — and functy already designed for it.** Not one of these
projects bounds execution. The Starlark provider's entire pitch is a deterministic
sandbox, and it then *disables Starlark's termination guarantees*
(`syntax.FileOptions{Recursion: true, While: true}`) and never calls
`thread.SetMaxExecutionSteps` — its docs advertise this as a feature. So an infinite loop
wedges `plan`. Lua runs `lua.DoString()` with no instruction budget and an unrestricted
stdlib; Go/Yaegi has the entire Go standard library available. **An execution-step limit
(see *Execution limits* in `FUTURE.md`) is the single unclaimed safety property in this
space** — the one thing functy would bring that none of the incumbents have.

**The sobering counterweight: adoption inverts sophistication.** The two dynamic-function
providers are OpenTofu's own experiments — ~40 stars each, untouched for roughly two
years. The Starlark provider has ~356 registry downloads. Meanwhile the *boring* options
dominate: `corefunc` (a plain Go catalog) has ~1.2M downloads, and a plain jsonnet **data
source** has ~3.3M. Whatever the technical merits, the revealed demand for
"embedded scripting language, evaluated at plan time" is thin. Note also that the
best-known JS provider is
[apparentlymart's own](https://github.com/apparentlymart/terraform-provider-javascript)
(goja + `go-cty-goja`, a `data` block predating provider functions), whose docs carry the
warning worth pinning above this entire file: *"just because you can, it doesn't mean you
should… Use this provider only for the rare problems where some complex computation is
required."*

## The shape of it

OpenTofu's `GetFunctions()`-after-configure mechanism means a single, generic, published
`functy` provider can read the user's local `.cty` files and expose their functions as
real, statically-typed OpenTofu functions:

```hcl
provider "functy" {
  source = file("./lib.cty")     # or a directory of .cty sources
}

output "example" {
  value = provider::functy::arn_build("aws", "s3", "", "123456789012", "my-bucket")
}
```

Why this is a better fit for functy than for any of the incumbents: **the hard part is
already done.** A compiled functy function *is* an ordinary `cty.Function` with declared
parameter types and a static return type — precisely the object the protocol wants.
`terraform-provider-go` has to reverse-engineer signatures out of Go's AST via Yaegi;
`terraform-provider-lua` gives up and regex-scrapes names. functy hands the signature over
directly. The provider is a thin marshalling shim: parse `.cty` → compile → for each
exported function, emit a `tfprotov6.Function` from its already-known signature → dispatch
`CallFunction` to the compiled `cty.Function`.

And it lands functy in the space as the only entrant that is **both** statically typed
(like `terraform-provider-go`, unlike Starlark/Lua) **and** termination-safe (unlike all
of them), while being *purpose-built* for cty and HCL rather than an interpreter for a
general-purpose language bolted onto them.

**The costs, stated honestly.** It is **OpenTofu-only** — Terraform's framework
deliberately withholds provider configuration from function implementations, so this shape
cannot work there (see `TERRAFORM.md` for what Terraform allows). The call form
`provider::functy::name(...)` is verbose and cannot be aliased, since Terraform/OpenTofu
functions are not first-class values (`locals { f = provider::functy::f }` is impossible).
And the mechanism is an experimental corner of OpenTofu that its own reference
implementations have left dormant for two years — a dependency on machinery nobody is
actively exercising.

### Namespaces: the main namespace is re-exported; everything else is a library

The obvious worry is that functy's namespaces (`namespace acme::math`) and OpenTofu's
`provider::functy::` prefix are two namespace systems needing reconciliation. They are
not — they are different layers, and seeing that dissolves the problem. The rule:

> **Anything defined in the main (global) namespace is re-exported from
> `provider::functy::`. Every other namespace is a library, internal to the unit.**

**Why nothing else is possible.** OpenTofu's `addrs.ParseFunction` splits a call name on
`::` and then accepts exactly two shapes (`internal/addrs/provider_function.go`):

```go
if len(f.Namespaces) == 2 {        // provider::<name>::<function>
    pf.ProviderName = f.Namespaces[1]
} else if len(f.Namespaces) == 3 { // provider::<name>::<alias>::<function>
    pf.ProviderName = f.Namespaces[1]
    pf.ProviderAlias = f.Namespaces[2]
} else {
    return pf, fmt.Errorf("invalid provider function %q: expected …")
}
```

The exported name is a **single trailing segment**. (HCL itself would parse
`provider::functy::acme::math::foo` — its parser consumes `::` in a loop — but OpenTofu
rejects it.) A functy namespace cannot ride inside the exported name; the *only* spare slot
is the provider alias.

**And it doesn't need to, because intra-`.cty` calls never touch OpenTofu.** The provider
compiles the whole unit, and `acme::math::foo()` resolves against functy's own
per-namespace function table. The host is not in that path and cannot be. So the
consistency requirement — *a name means the same thing whether the call site is in a `.cty`
or a `.tf` file* — holds unconditionally on the functy side, on every host. What differs
between hosts is only which names are visible from outside.

```functy
// lib/math.cty
namespace acme::math
func foo(x: number) -> number { return x + 1 }

// api.cty            ← no namespace declaration: the re-exported surface
func compute(x: number) -> number { return acme::math::foo(x) * 2 }
```

OpenTofu sees `provider::functy::compute` and nothing else. `acme::math` is not *hidden* —
it is simply not re-exported. A library shared with another host (Vinculum) is consumed
unchanged; the Terraform-facing API is a thin facade file.

This needs **no functy change**: `FuncDecl.Namespace` already tags every declaration, so
the export set is a provider-side filter (`Namespace == ""`). It also composes with the
existing `_` convention to give three visibility tiers with no new syntax: `_helper` is
namespace-private, a namespaced function is host-private, a bare global function is public.

Two escape hatches, both provider-config-only, neither touching the language:

- **A global namespace you don't want fully re-exported** — an allowlist:
  `export = ["compute", "arn_parse"]`.
- **A namespace you genuinely do want to export** — the alias slot, the only place a second
  segment can legally go:
  `provider "functy" { alias = "math", namespace = "acme::math", source = … }` →
  `provider::functy::math::foo`. This is a **rename into OpenTofu's world**, not a claim
  that the name is unchanged, because OpenTofu's grammar cannot express `acme::math::foo`
  as a call under any encoding. Flattening (`acme_math_foo`) is rejected: collision-prone,
  and it breaks the one-consistent-view property far worse than declining to export.

A functy-side `@export` marker (cf. the `@standalone` marker tier in `FUTURE.md`) is
deliberately **not** proposed: it would make a library's source aware of Terraform, exactly
the coupling functy avoids with every other host. The facade file achieves the same
selection with no annotation and no host knowledge.

**Open question requiring a spike:** whether OpenTofu calls `GetFunctions()` per *aliased*
provider instance after configuring each. The address layer plainly routes alias-qualified
calls, but routing is not discovery. If aliases prove discovery-blind, the fallback is one
namespace per unaliased provider block, or global-only export.

### What functy code can see: the provider defines the whole environment

A provider-defined function runs **in the provider process**, reached by a
`CallFunction(name, args)` RPC: core marshals the arguments over the wire and marshals a
result back. That is the entire contract, and there is no reverse channel. So:

- **Other providers' functions** (`provider::aws::arn_parse`) — **no.** Not merely
  unimplemented: there is no RPC by which a provider calls back into the evaluator. A
  provider is a leaf.
- **OpenTofu's core stdlib** (`jsonencode`, `cidrsubnet`, `tolist`, …) — **not for free.**
  Those live in the tool's evaluator (`internal/lang/funcs`, an `internal/` package, so not
  importable even in principle).
- **Variables, locals, `path.module`, resource attributes** — **no.** These are not
  functions; they are evaluated core-side and reach a function only as *arguments*. This is
  the args-only constraint noted elsewhere in this file, arriving from a different
  direction.
- **Other functy functions in the loaded unit, across every namespace** — **yes, all of
  them.** That path is internal to functy and never touches the RPC.

So the eval context is **whatever the provider builds** — a design decision, not a given,
and it should be treated as a published contract of the provider ("these are the functions
available inside `.cty` when loaded this way").

functy is unusually well-placed to make that contract a good one. Most of what people mean
by "Terraform's stdlib" is reconstructible from public packages — `go-cty`'s
`function/stdlib` plus `hashicorp/go-cty-funcs` (cidr, crypto, encoding, uuid, collection)
— which is precisely the base functy and Vinculum already build on. The provider can
therefore offer a near-Terraform-alike environment, and should **document the deltas**
rather than claim parity.

Two traps to avoid:

- **Do not naively offer `file()` / `templatefile()`.** In OpenTofu they resolve relative to
  the *module*; inside a provider process they would resolve relative to the *provider's*
  working directory — wrong at best, a sandbox escape at worst. Omit them, or resolve them
  against the configured source path.
- **Nothing impure, whatever is linkable.** These functions run during `plan`.

There is an upside hiding in this constraint: because *functy* supplies the environment
rather than the host, a `.cty` library behaves identically under Vinculum, the standalone
CLI, and the provider. The environment is stable precisely because it does not depend on
OpenTofu's internals.

**The credible next step, if pursued:** a working provider prototype. It is a small,
self-contained piece of work — the two reference implementations are the template — and,
unlike everything else in this file, it needs nobody's approval and no fork. It would also
be the end-to-end demonstration that the abstract UDF requests in #27696 / #793 never had.


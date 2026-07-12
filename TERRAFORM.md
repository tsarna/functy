# functy on Terraform / OpenTofu

Terraform and OpenTofu are the most consequential potential hosts for functy that
functy does not currently serve. They are also the hardest: unlike an ordinary host,
which links functy as a library and is free to shape its own configuration language,
these tools are governed projects whose extension points are deliberately narrow, and
whose maintainers have already ruled on most of what functy would want to ask for.

This file is the analysis of that question — kept separate from both `FUTURE.md` and
`REJECTED.md` because it is neither a planned direction nor a rejected one: nothing here
has been decided. It is a map of the terrain, written down so the
question can be re-opened without re-deriving it, and so that a "wouldn't this be great
in Terraform?" impulse meets the constraints before it meets a keyboard.

**Nothing in this file is planned.** Two routes are analyzed — a provider binding that
works on stock Terraform, and a core change that does not — along with a third variant
that may be the only one able to thread the needle.

## The prior art, and why it matters

Everything here is downstream of one long-standing request: **user-defined functions in
the configuration language.** It has been asked in both projects, and the two answers are
the boundary conditions for anything functy might propose.

- **[hashicorp/terraform#27696](https://github.com/hashicorp/terraform/issues/27696)** —
  the origin request. **Open, but untriaged**, and has been for years. Not a rejection;
  not encouragement either.
- **[opentofu/opentofu#793](https://github.com/opentofu/opentofu/issues/793)** —
  **closed as "not planned."** The proposed syntax
  (`locals { func myfunc(a: string) => map(string) { … } }`) is, notably, nearly functy
  already. The maintainer's stated reasons were that they *"are not the owner of the HCL
  language"* and that it *"would be a significant change to maintain in a fork,"* with
  provider-defined functions offered as the sanctioned alternative.

Read those two before reading anything below. They establish that the *feature* is
wanted, the *governance door* is closed on the OpenTofu side and unmoving on the
HashiCorp side, and that provider-defined functions are the only channel either project
currently blesses — which is exactly why Route 1 exists despite negating much of functy's
value, and why Route 3 (a *built-in* provider, extending a mechanism they already own
rather than adding language syntax they have refused) is the most viable of the three.

A third rejection, from HCL itself rather than the tools, bounds the inline-embedding
variant of Route 2: [hashicorp/hcl#207](https://github.com/hashicorp/hcl/issues/207).
See `REJECTED.md`.

## Route 1: a provider binding (speculative — low priority)

Works on stock Terraform; asks nothing of anyone. Expose functy-authored functions
to Terraform as provider-defined functions, callable as `provider::functy::<name>(...)`.
Since Terraform 1.8 / OpenTofu 1.7 this is the *only* sanctioned way to add custom
functions, delivered through terraform-plugin-framework's `function` package (the
provider declares typed parameters + a typed return per function and implements `Run`).
The binding is conceptually thin — a compiled functy function is already a `cty.Function`
with a static return type, and the framework's `Run` bridges to cty over the wire — so
the adapter mainly translates functy/cty types into framework type definitions and
marshals args. functy's typed signatures + strict typing are what make this clean:
Terraform *requires* declared parameter/return types, which functy can now guarantee.

**Worth it? Mostly no — recorded for the analysis, not as a roadmap item.** The core
value of functy as a scripting extension is the no-build edit→run loop (drop a `.cty`,
it's live — what Vinculum and the standalone CLI give you). Terraform's provider model
structurally forbids exactly that: static schema, registry-distributed binaries pinned
via `.terraform.lock.hcl`. So the only *robust* integration (option 1 below) reintroduces
the build-and-publish loop functy exists to eliminate, and the paths that keep the
no-build feel (options 2–3) are the unsafe ones. The robust option negates the value;
the value-preserving options aren't robust — an intrinsic tension, not a gap to engineer
around. Once a build step is mandatory the marginal saving over writing the functions in
Go shrinks to "bodies in functy vs. Go" (the annoying framework boilerplate is mechanical
and code-generatable *without* functy), and off-the-shelf providers (corefunc, validatefx,
the AWS/time/google functions) already cover the common cases.

**The one framing where it's not a waste** is narrow and worth stating precisely: not
"functy as a Terraform scripting extension" (Terraform kills that), but *functy as a
portable function-authoring format that can also compile to a TF provider*. If a `.cty`
function library already exists for other reasons (Vinculum, a shared library), a
generator that *additionally* emits a Terraform provider from it is a cheap bonus —
reuse, not authoring-for-Terraform. It is never a reason to adopt functy in the first
place; if the library doesn't already exist, doing this just for Terraform isn't worth it.

The design analysis below is kept because it is correct and self-contained, should the
portability framing ever justify the work.

Two constraints shape the design:

- **Pure-only.** Terraform functions must be deterministic and side-effect-free
  (evaluatable during plan), so only functy's pure core + pure stdlib are in scope —
  `send` / `log_*` / store style ops are excluded. This maps exactly onto the pure-vs-
  host-coupled split under *Standard library* in `FUTURE.md`; the three embeddings line up as
  Vinculum (full, incl. side effects) / standalone CLI (pure core) / Terraform (pure
  core re-exposed). The natural fit is exactly what existing provider functions do:
  pure data transforms — parse↔build pairs, encoding/formatting, validation, CIDR /
  semver / time helpers — things awkward to express in raw HCL.
- **Static function schema vs. file-loaded functions (the crux).** Terraform fetches a
  provider's function list as *static schema*, before and independently of provider
  configuration — so a provider cannot be told "load `./funcs/*.cty` and expose
  whatever they define." Options, increasing in cleverness and decreasing in honesty:
  (1) **build-your-own-provider** — `.cty` compiled into a custom provider binary at
  build time (cleanest typing, heaviest ergonomics; the honest MVP); (2) **one generic
  dynamic function** — `provider::functy::eval("expr", {args})` returning a dynamic
  type (sidesteps the static set but discards static typing and is *more* verbose —
  rejected as defeating the point); (3) **scan-at-schema-time** — enumerate `*.cty` in
  the working dir when Terraform requests the schema (most "magic", but makes the
  schema depend on local files, against Terraform's stability assumptions).

The call-site verbosity (`provider::functy::add(2, 3)`) is inherent and inescapable:
Terraform namespaces provider functions deliberately and offers no aliasing or import,
and functions are not first-class values (so `locals { add = provider::functy::add }`
is impossible). It is, however, idiomatic Terraform. This item is gated on choosing an
answer to the static-schema question; option (1) is the recommended starting point.

**Motivating examples.** The killer demo is not `add(2, 3)` — it is the two categories
that dominate real provider functions, both of which are tedious in Go and painful in
raw HCL. First, the **parse↔build round-trip** (HashiCorp's own `aws::arn_parse` /
`aws::arn_build` and `terraform::decode_tfvars` / `encode_tfvars` are exactly this):

```functy
// "arn:partition:service:region:account:resource…" -> structured object
func arn_parse(arn: string) -> object({
    partition = string, service = string, region = string,
    account_id = string, resource = string,
}) {
    var p = split(":", arn)
    if length(p) < 6 { throw error("malformed ARN: ${arn}") }
    return {
        partition  = p[1],
        service    = p[2],
        region     = p[3],
        account_id = p[4],
        resource   = join(":", slice(p, 5, length(p))),   // resource may contain ':'
    }
}

func arn_build(partition: string, service: string, region: string,
               account_id: string, resource: string) -> string {
    return "arn:${partition}:${service}:${region}:${account_id}:${resource}"
}
```

Second, **validation** — the single largest community category (the actively-maintained
`validatefx` ships ~70 such functions; the archived `hashicorp/assert` had 36), almost
all of them "check a string/number against a rule, return `bool`," which functy expresses
directly with `try` / `catch`:

```functy
func valid_json(s: string) -> bool {
    try { jsondecode(s); return true } catch e { return false }
}
```

Re-exposed, these are called `provider::functy::arn_parse(...)` /
`provider::functy::valid_json(...)` — i.e. a `validatefx` or `corefunc` you author in
`.cty` instead of maintaining a Go provider. All such functions are pure (no I/O, no
state), so they sit cleanly inside the pure-only constraint.

## Route 2: embedded functy (a core change — explored, not planned)

Route 1 works on stock Terraform but reintroduces the build/publish loop and cannot load
local `.cty` (static schema). The opposite trade is to author functions *in the
configuration* — as sibling `.cty` files, or as inline `functy { }` regions (see
*Embedding functy inside a host's HCL files* in `REJECTED.md` for both mechanisms, and
for why inline regions carry a cost that sibling files do not) — and have the tool
compile and expose them.
This is the long-requested "user-defined functions in the language" feature, with
functy as the implementation.

**It requires a core change; it cannot be a plugin.** To call a config-defined function
elsewhere in the same config, the compiled function must be injected into the eval
context the tool builds during config load (`lang.Scope.Functions()`, a
`map[string]function.Function`). A third-party provider exposes only a *static* gRPC
schema in its own `provider::name::` namespace and cannot inject into the config's
table; a preprocessor can't help either, since Terraform functions aren't first-class
values, so a call site whose arguments derive from resource attributes can't be
macro-expanded ahead of eval. Only core can do it.

**The fit, if core could change, is excellent** — better than the provider path. A
compiled functy function *is* a `cty.Function` with a static return type, exactly what
the function table holds, so integration is "discover → compile → merge." Purity and
determinism map cleanly (pure core only; no `send` / `log_*`), with one guardrail — an
execution limit (see *Execution limits* in `FUTURE.md`) so an unbounded `for` cannot wedge `plan`.
Keep functions **args-only** (no `var.` / `resource.` references) so they are pure
transforms compiled once per module and stay entirely out of the dependency graph,
which keeps the change small. Namespace via the `::` token (`fn::f` / `local::f`),
mirroring `provider::`. Mental model: "`locals`, but for functions."

**Why "not planned": the governance door is (currently) closed on both forks.** The two
requests above (#793 closed as "not planned"; #27696 open but untriaged) are the whole
story on whether this route may be walked at all. What is worth adding here is that
several *technical* objections raised in those threads are ones functy's design
**answers** — so if the governance door ever reopens, the design case is unusually
strong. Specifically: that
HCL attributes are unordered (bad for a function body) — functy owns an **ordered**
statement grammar, so bodies are never HCL attribute maps; that embedding bodies as
**strings** kills tooling and makes functions "third-class citizens" with no
inspectable purity/cycle guarantees — clean embedding keeps them real source (typed,
statically checkable, pure-by-construction, bounded, with existing check/fmt/symbols +
a VSCode extension); and that the tool "doesn't own HCL and can't change it" — the
embedding shim (`REJECTED.md`) requires **zero** HCL changes (it reuses HCL's own lexer
and expression parser and preprocesses at the byte level), and functy is an external
MIT library, so the ask is "link a library + a small shim," not "fork HCL."

What functy does *not* remove are the governance objections proper: language-surface
growth, an added eval phase, and long-term maintenance. And one further objection has
since surfaced from HCL itself, which weighs against inline regions specifically: a file
containing one is no longer parseable by generic HCL tooling, and HCL's maintainers have
already declined (hashicorp/hcl#207) the one change that would fix that — on the
reasoning that a foreign language embedded in HCL belongs in its own file. See
*Embedding functy inside a host's HCL files* in `REJECTED.md`, where the same conclusion
is reached independently. For Terraform the objection bites less hard than it does for an
ordinary host — core owns the parser, and to a first approximation the tool *is* the
ecosystem — but not zero: `tflint`, `terraform-ls`, `tfsec` and every other third-party
consumer of `.tf` files are exactly the tooling that would break. **Sibling `.cty` files
avoid the question entirely, and are the recommended shape for this route.**

## Route 3: a *built-in* `functy` provider — the one variant that might thread the needle

Distinct from the third-party provider of Route 1: core code that loads local `.cty` at
config-load time and exposes them as `provider::functy::name`. It reuses machinery both
projects already accepted — dynamic provider-defined functions, and the "built-in
functions as sugar for a built-in provider" direction (opentofu/opentofu#1707) — so it
asks them to extend a mechanism they *own* rather than add language syntax they have
rejected. The static-schema fragility that sank the third-party path does not apply to
a built-in provider (core controls schema timing, so enumerating local files is
legitimate). It also cleanly answers a maintainer's own thread sketch —
`providers::custom_fn::exec(body, …)` with the body as a string — by replacing the
string with real `.cty` source. Cost: the verbose-but-idiomatic `provider::functy::`
call form. This is the most viable path if the idea is ever pursued; the credible next
step (not taken) would be a working OpenTofu fork/prototype plus a written RFC, since
functy's differentiator is an end-to-end demonstration that abstract UDF requests never
had. Recorded as analysis; nothing here is planned.

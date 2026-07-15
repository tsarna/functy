# functy on Terraform: the blocked routes

Terraform and OpenTofu are the most consequential potential hosts for functy that functy
does not currently serve. They are also the hardest: unlike an ordinary host, which links
functy as a library and is free to shape its own configuration language, these tools are
governed projects whose extension points are deliberately narrow, and whose maintainers
have already ruled on most of what functy would want to ask for.

> **Read [`OPENTOFU-PROVIDER.md`](OPENTOFU-PROVIDER.md) first.** There *is* a route that works — a provider
> for OpenTofu, buildable today with no core change and nobody's permission. It has its own
> file because it is a live design, while everything here is settled and blocked.
>
> **This file is the rest:** what Terraform allows (much less), what would require a core
> change (a lot), and the governance history that explains why. Nothing in it is planned.

Three routes are analyzed below, in decreasing order of plausibility. They are recorded so
the question can be re-opened without re-deriving it, and so that a "wouldn't this be great
in Terraform?" impulse meets the constraints before it meets a keyboard.

## The governance picture: the two feature requests

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
wanted, that the *governance door* is closed on the OpenTofu side and unmoving on the
HashiCorp side, and that **provider-defined functions are the only channel either project
currently blesses**. Terraform's maintainer (@jbardin) put the boundary about as plainly
as it can be put, closing #27696:

> *"HCL is not a general purpose programming language… **The RPC layer is where we've
> chosen to extend the function interface, and will be what's available for the
> foreseeable future.**"*

That sentence is why Routes 2 and 3 below (which ask for language surface) are recorded
rather than pursued — and why the OpenTofu provider (`OPENTOFU-PROVIDER.md`), which asks for nothing
and lives entirely inside the RPC layer they endorse, is the live one. The maintainers did
not merely tolerate the provider channel; they named it as the *only* one.

A third rejection, from HCL itself rather than the tools, bounds the inline-embedding
variant of Route 2: [hashicorp/hcl#207](https://github.com/hashicorp/hcl/issues/207).
See `REJECTED.md`.

## Route 1: stock Terraform — a provider binding (speculative — low priority)

The OpenTofu provider (`OPENTOFU-PROVIDER.md`) depends on an OpenTofu-only protocol feature. On
**Terraform**, the function list is fixed before the provider is configured, so a provider
cannot learn about your `.cty` files at all. That leaves two shapes, and neither is good:

- **A compiled catalog.** `.cty` sources compiled into a custom provider binary at build
  time, exposed as `provider::functy::<name>(...)` via terraform-plugin-framework's
  `function` package. Real static types (functy's typed signatures are exactly what the
  framework wants), but the user must build and publish a provider. This is what
  `corefunc` is, minus the embedded language.
- **A generic `eval`.** One function taking the source as a string:
  `provider::functy::eval("<source>", {args})`, returning `dynamic`. This is the shape
  `terraform-provider-starlark` chose and the one `terraform-provider-lua` falls back to
  on Terraform. It works everywhere, and it throws away everything functy is for: no
  static types (the return is `dynamic`, deferring all type errors to apply), the function
  body is a quoted string your editor cannot help you with, and the call site is *more*
  verbose than the HCL it replaces.

**Worth it? Mostly no — recorded for the analysis, not as a roadmap item.** The core value
of functy as a scripting extension is the no-build edit→run loop (drop a `.cty`, it's live
— what Vinculum and the standalone CLI give you). Terraform's provider model structurally
forbids exactly that: static schema, registry-distributed binaries pinned via
`.terraform.lock.hcl`. So the only *robust* integration (the compiled catalog) reintroduces
the build-and-publish loop functy exists to eliminate, and the path that keeps the no-build
feel (generic `eval`) discards the typing that makes functy worth having. The robust option
negates the value; the value-preserving option isn't robust — an intrinsic tension, not a
gap to engineer around. Once a build step is mandatory, the marginal saving over writing
the functions in Go shrinks to "bodies in functy vs. Go" (the framework boilerplate is
mechanical and code-generatable *without* functy), and off-the-shelf providers (corefunc,
validatefx, the AWS/time/google functions) already cover the common cases.

**Note what this asymmetry means.** The same feature is a clean plugin on OpenTofu and an
unsatisfying compromise on Terraform, purely because of one protocol decision. If a
Terraform-shaped answer is ever wanted, the honest ask is not "let us add language syntax"
(refused) but "call `GetFunctions()` after `ConfigureProvider`, as OpenTofu does" — a
request confined to the RPC layer that @jbardin explicitly named as *the* place Terraform
extends the function interface.

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
- **The static function schema (the crux — and it is a Terraform-only crux).** Terraform
  fetches a provider's function list as *static schema*, before and independently of
  provider configuration, so a provider cannot be told "load `./funcs/*.cty` and expose
  whatever they define." A tempting third option — **scan-at-schema-time**, enumerating
  `*.cty` in the working directory when the schema is requested — is not merely
  inadvisable but *unavailable*: the schema RPC carries no knowledge of the configuration
  directory, and no project in the wild attempts it. That leaves the compiled catalog and
  the generic `eval` described above. OpenTofu removes the crux entirely (`OPENTOFU-PROVIDER.md`); on
  Terraform it is load-bearing and unfixable from outside.

The call-site verbosity (`provider::functy::add(2, 3)`) is inherent and inescapable on
both tools: they namespace provider functions deliberately and offer no aliasing or
import, and functions are not first-class values (so `locals { add = provider::functy::add }`
is impossible). It is, however, idiomatic.

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

## Route 2: functions in the language itself (a core change — explored, not planned)

Route 1 and the OpenTofu provider both put functions behind a `provider::functy::`
namespace. The ambitious alternative is to make config-defined functions **first-class in
the language**, callable
as plain `f(x)` — authored as sibling `.cty` files, or as inline `functy { }` regions (see
*Embedding functy inside a host's HCL files* in `REJECTED.md` for both mechanisms, and for
why inline regions carry a cost that sibling files do not) — with the tool compiling and
exposing them. This is the long-requested "user-defined functions in the language" feature
of #27696 / #793, with functy as the implementation.

**This is the route that requires a core change and cannot be a plugin** — and note that
the claim is narrower than it used to look. It is *unqualified* namespacing that is out of
reach: to call a config-defined function as bare `f(x)` elsewhere in the same config, the
compiled function must be injected into the eval context the tool builds during config
load (`lang.Scope.Functions()`, a `map[string]function.Function`). A provider can only
publish into its own `provider::name::` namespace over gRPC and cannot touch the config's
own function table; a preprocessor cannot help either, since these functions are not
first-class values, so a call site whose arguments derive from resource attributes cannot
be macro-expanded ahead of eval. Only core can do it.

What the OpenTofu provider shows is that **the rest of the feature — loading local `.cty`, compiling it,
and exposing real typed functions — does not need core at all** on OpenTofu. The core
change buys exactly one thing: dropping the `provider::functy::` prefix. That is a
genuine ergonomic win and it is the thing the issue threads were actually asking for, but
it is a much smaller delta than this route's framing once implied, and a much worse
cost-to-benefit trade than simply shipping the OpenTofu provider.

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

## Route 3: a *built-in* `functy` provider — largely superseded by the OpenTofu provider

Core code that loads local `.cty` at config-load time and exposes it as
`provider::functy::name`, shipped *in* the tool rather than installed from the registry.
It reuses machinery both projects already accepted — dynamic provider-defined functions,
and the "built-in functions as sugar for a built-in provider" direction
(opentofu/opentofu#1707) — so it asks them to extend a mechanism they *own* rather than
add language syntax they have refused. It also answers a maintainer's own thread sketch —
`providers::custom_fn::exec(body, …)`, with the body as a string — by replacing the string
with real `.cty` source.

**This was written as the most viable path, and the OpenTofu provider has since overtaken
it.** Every
capability listed above is achievable *today*, on OpenTofu, by an ordinary third-party
provider, with no core change and no permission. What remains exclusive to a built-in
provider is thin: no separate `required_providers` entry / registry install, and a
plausible answer for **Terraform**, where the third-party route is blocked by the
static-schema rule that core itself would not be bound by.

So this stays recorded as the shape to propose *if* someone ever wants first-party
support — but the honest sequencing is now obvious: **build the OpenTofu provider first.**
A working
provider is the argument. Proposing a built-in before a working third-party one exists is
asking a governed project to adopt something nobody has demonstrated; proposing it after
is showing them something their users already run.

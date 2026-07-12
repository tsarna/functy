# functy — Rejected Designs

This file records designs that were worked out in enough detail to be judged, and
then **declined**. It is the counterpart to `FUTURE.md`: that file is a map of what
is left to do, this one is a map of what was deliberately not done, and why.

Nothing here is a backlog. An entry earns its place by being (a) plausible enough
that someone — including a future maintainer, including the author — will propose it
again, and (b) settled by an argument worth not re-deriving. If the argument that
killed an entry stops holding, the entry is a ready-made design to revive; that is
the reason to keep the analysis rather than the verdict alone.

## Embedding functy inside a host's HCL files (mixed `functy { … }` regions)

**Verdict: declined.** The mechanism works — two independent mechanisms work, in
fact, one requiring no changes to HCL at all — but any file containing an embedded
functy region *stops being an HCL file*, and that cost is not recoverable by better
engineering. The analysis below is kept because the mechanisms are sound and the
conclusion turns on a single judgement (portability of the host's config files)
that a different host, or a different day, might weigh differently. That judgement
turns out to be HCL's own: see *What would unblock this*, below, where the one change
that would make embedding portable is found to have been proposed to HCL in 2017 and
refused — on the same reasoning this document arrives at independently.

Today a host (eg Vinculum or perhaps Terraform or other software in the future)
links functy and loads `.cty` sources *alongside* its HCL
config. A tighter integration lets one file interleave both — an escape-to-functy
region inside an otherwise-HCL file, so function definitions live next to the
config that uses them:

```hcl
bus "main" { queue_size = 1000 }

functy {
    func double(n: number) -> number { return n * 2 }
}

server "http" "api" { listen = ":8080" }
```

**Mechanism — feasible with essentially no HCL changes and none *required* of
functy.** functy already tokenizes via `hclsyntax.LexConfig` (the same
native-syntax lexer HCL's own parser uses) from an absolute start position, so one
lex pass yields a token stream both grammars agree on and any carved-out byte span
keeps its original line/column. A preprocessing shim then: (1) lexes once;
(2) brace-matches the token stream to find each top-level `functy { … }` span
(template/heredoc braces are distinct token types, so this is robust against
`${…}` and `{k = v}`); (3) produces two equal-length overlay buffers by blanking
bytes with spaces while preserving newlines — one with the `functy` regions
blanked *out* (handed to the HCL parser), one with everything *except* the region
interiors blanked (handed to functy). Because blanking is a 1:1 byte substitution,
every offset/line/column is unchanged, so **both parsers report diagnostics at
true positions** and a single original-source file map renders snippets for both.
(Validated with a throwaway prototype: an HCL block and a functy `func` in one
file each parsed at their real lines, and a functy runtime error surfaced at its
true `line,col`.)

**Naming.** `functy` (not `functions`) as the block keyword: it avoids colliding
with hosts that already have a `function` construct, and a region can hold `func`
/ `const` / `var` / `type` / `test` — not only functions. Multiple `functy { }`
blocks merge into one namespace (matching functy's cross-source `ParseAll`
semantics); a label is unnecessary.

**Where the code lives + tooling impact.** The shim is one small shared component
(an exported helper — e.g. an `hclmix`-style package — so both the host and
functy's own tooling call it). Most CLI verbs then fall out of capabilities functy
already has:
- **`check` / `symbols`** need no host knowledge — a functy syntax/type check is
  late-bound (unresolved host names are not errors; they surface only at
  *runtime*), so functy can validate the functy slice of a mixed file on its own.
  The existing `-` (stdin) + `--filename` seam already accepts the blank-outside
  buffer and returns correctly-positioned results with **no new flags**
  (`functy check - --filename foo.vcl`); the host layers its own schema /
  host-symbol checks over the same file for the authoritative whole-file result.
- **`test`** works via the caller-supplied context (`RunTests`): the host runs
  *all* tests against its full context, while bare `functy test` runs the
  `@standalone`-marked subset (see the *Marker-annotation tier* under Annotations)
  and skips the rest.
- **`fmt` is the one genuinely hard piece** — the formatter is a whole-file AST
  reprinter, so a mixed file needs a two-layer splice: format each `functy` region
  and re-indent it to the block's column, leaving the HCL text untouched (full HCL
  re-canonicalization via `hclwrite` is a harder three-way splice, deferrable).
- **Editor support.** Highlighting is the standard "language-in-language" TextMate
  injection (embed `source.functy` inside `functy { }`); semantic features are
  best driven through the *host's* CLI (which links functy and runs the shim),
  rather than making two editor extensions cooperate at runtime.

**The unavoidable cost of embedding: a mixed file is not an HCL file.** Whatever the
mechanism, a file containing a `functy { }` region can only be parsed by something
that knows to treat that region specially. Generic HCL tooling — `hcl fmt`,
`hclwrite`-based rewriters, terraform-ls, any third-party linter — will choke on it
or silently mangle it. This is inherent to language-in-language embedding, *not* a
property of the shim, and no implementation strategy escapes it. It is the real price
of the feature and should be weighed as such: the payoff is that functions live next
to the config that uses them; the cost is that the host's files stop being
ecosystem-portable HCL. A host can mitigate but not eliminate this by keeping mixed
files opt-in (config that never uses `functy { }` stays plain HCL) and by routing
formatting through its own CLI.

**Core-change alternative — a raw-block parse option in `hclsyntax`.** If HCL itself
could be modified, the shim becomes unnecessary, and the change is surprisingly small.
The knob has to live at the **parse** layer, not the decode layer: a struct-tag
approach (`hcl:",tokens"` as a sibling of `,remain`) cannot work, because `gohcl`
reads tags only *after* the parser has already built the whole AST — `,remain` is a
"don't decode" tag, not a "don't parse" tag, and by the time it is consulted the
functy body has long since failed to parse as HCL. The workable shape is instead:

```go
hclsyntax.ParseConfigWithOptions(src, filename, start, hclsyntax.ParseOptions{
    RawBlockTypes: []string{"functy"},   // or a predicate over (type, labels)
})
```

The implementation fork is a single spot: in `finishParsingBodyBlock`, right after the
opening brace is consumed (where the parser would otherwise call `ParseBody(TokenCBrace)`),
a raw block instead scans forward counting `TokenOBrace` / `TokenCBrace` and captures
the span. Three properties make that cheap and robust: the peeker already holds the
entire pre-lexed token slice, so capture is index arithmetic with no second pass;
brace counting is safe against `${…}` and heredocs, because template braces are
distinct token types (`TokenTemplateInterp` / `TokenTemplateControl` /
`TokenTemplateSeqEnd`) and string/heredoc content lives inside its own token, so only
real braces count; and block labels work for free, since labels are parsed before the
opening brace. The captured block gets a `Body` that satisfies `hcl.Body` but returns
a diagnostic ("the body of a `functy` block is not HCL") from `Content()`, while
exposing `RawTokens()` / `RawRange()`. The **decode side then needs no changes at
all**: the host captures the block with the `,remain` tag it already has and
type-asserts to get the tokens.

One constraint survives: the body must still *lex* as HCL, since a single `LexConfig`
pass tokenizes the file before the parser runs. That is a non-issue for functy (which
lexes via `hclsyntax.LexConfig` already — the same reason the shim works) but means
this is an escape hatch for *lexically HCL-compatible* DSLs, not for arbitrary text.
A related wrinkle: lexer diagnostics are produced before the parser knows where the
raw regions are, so `ParseConfigWithOptions` must drop lex diags whose subject falls
inside a captured span — the shim has the identical problem and solves it identically,
so nothing is lost.

Upstream would nonetheless resist, for the same reason the feature is declined here:
it makes an HCL file unparseable without out-of-band knowledge of the host's raw-block
list, breaking HCL's invariant that any HCL file can be parsed by any HCL tool. And
`hclwrite` — which has its own parser over the same token stream — would need parallel
support before `fmt` worked at all.

**Why both mechanisms are declined.** The two mechanisms differ only in cleanliness,
not in consequence. The core change buys
no blank-out buffers, no overlay bookkeeping, and a general HCL capability rather than a
functy-specific shim; the shim buys working today against unmodified upstream HCL. Both
produce the same artifact: **a file that only the host can parse.**

That is the whole decision. functy's value proposition is that it is a *guest* in
someone else's ecosystem — it reuses HCL's lexer, HCL's expression grammar, and HCL's
type system precisely so that adopting it costs a host nothing it already has. Embedding
inverts that: it asks the host to trade its config files' compatibility with every
HCL tool in existence (`hcl fmt`, `hclwrite` rewriters, terraform-ls, third-party
linters) for the convenience of putting function definitions in the same file as the
config that calls them. Sibling `.cty` sources — the status quo — buy nearly all of that
convenience for none of the cost: the functions still live next to the config, just one
file over, and every file in the tree remains exactly what its extension claims it is.

The verdict would change if the calculus changed. If a host's config files were never
touched by generic HCL tooling — a closed ecosystem, or a tool that *is* the ecosystem
(see the Terraform/OpenTofu core-change discussion in `FUTURE.md`, where core itself
would own the parser) — the cost largely evaporates and the analysis above becomes a
build plan rather than a post-mortem.

## What would unblock this: self-describing opaque regions in HCL

The rejection above turns on one fact: a mixed file cannot be parsed by a tool that
does not already know which block types are secretly not-HCL. Both mechanisms hide
that knowledge out of band — in a shim's block list, or in a parse option — so the
bytes on disk are indistinguishable from a file whose `functy { … }` really is an
HCL block. **The fix is not a better way to skip the region; it is a way for the
region to announce itself.** Any syntax a tool can recognize *without knowing the
host's vocabulary* restores portability, because "leave this span alone" becomes a
property of the text rather than of the reader.

Two forms of that, in increasing order of ambition.

**The minimal ask: a raw (non-interpolating) heredoc.** HCL already has a
self-describing opaque-text delimiter that every existing tool handles correctly, and
functy is exactly one lexer feature away from being able to use it:

```hcl
functy = <<-'FUNCTY'
    func double(n: number) -> number { return n * 2 }
FUNCTY
```

Only the quotes around the terminator are hypothetical; everything else parses on stock
HCL today. `fmt` already leaves heredoc interiors alone (with `<<-`'s indent-stripping as
the stated rule), the `hclsyntax` AST hands back the body's exact byte range, and the
*user-chosen* terminator label means no lexeme is forbidden inside the body — the problem
that sinks any fixed delimiter (see the maximal ask below).

**The host never reads the heredoc's value.** It takes the body's `SrcRange` from the AST
and slices the *original* bytes, handing those to functy — so functy lexes real source at
true line/col with no overlay buffers, and `<<-`'s dedenting can never mangle a position,
because the dedented string is simply never used. This is a strictly better position than
the shim achieves, with less machinery.

Exactly one thing stops it working today: **a heredoc is a template expression**
(`hclsyntax/spec.md`), so `${…}` and `%{…}` inside it are interpolated, and HCL offers no
raw variant. Note the narrowness of this: heredocs do *not* process backslash escapes —
that is a quoted-template-only feature — so a functy regex like `"\d+"` already passes
through untouched. Only `$`/`%` before `{` are special.

Which means a *plain* heredoc very nearly works right now, and understanding why it
doesn't is the whole argument for the raw form. Since the host discards the interpolated
value, it does not matter what a `${…}` sequence evaluates *to* — it matters only that the
template **parses**. functy's own templates contain HCL expressions by construction, so
they generally do. But "generally" is a coincidence, not a contract: a functy line comment
reading `// TODO: handle ${foo` is a hard parse error in the *host's config file*. A
feature where a comment can break the build is cursed, and no amount of care by the functy
author fixes it, because the failure is in someone else's parser. A raw heredoc converts
the accident into a guarantee. That framing belongs in any upstream request — it shows the
feature is not sugar.

So the ask on HCL reduces to a *raw heredoc* — `<<'FUNCTY'`, following the long-standing
shell convention where quoting the delimiter word disables expansion. It is a lexer
change, not a grammar change; it alters no block structure, so an un-updated tool still
sees a well-formed file with a string-ish token in it, and the blast radius across the
ecosystem is a fraction of a new block form's. It also has motivation entirely independent
of functy: everyone embedding JSON, jq, SQL, or shell in HCL fights `$${` escaping today.

### This has already been asked, and refused — on our own argument

**hashicorp/hcl#207, "Support heredoc without interpolation" (Aug 2017, closed by Martin
Atkins / `apparentlymart` one day later).** The request was exactly `<<'EOT'`, motivated
exactly by `${` in an embedded foreign language. It was filed against HCL1 — but it was
declined by the principal author of HCL2, who at that moment was actively writing
`zclsyntax`, the `zcl` project that became HCL2. So the refusal came from the person then
designing the successor language, and that successor shipped without a raw heredoc and
still has none. The position is not a stale HCL1 artifact; it held across a clean-slate
redesign. Three reasons were
given, and it is worth being precise about them, because the two obvious ways to
strengthen the proposal are the two he pre-empted:

- **Language complexity.** *"there are already several different string variants in HCL,
  with subtle differences between them, and so I'm somewhat reticent to add another."*
  Note that this is about **surface area for readers, not implementation cost** — so
  "it's only a lexer change, not a parser change," however true, does not touch the
  objection.
- **The quoting is too subtle.** *"it is unlikely to be obvious to all readers that
  placing the identifier in quotes activates a new behavior. I understand that this is
  familiar to those with more advanced knowledge of shell languages, but HCL tries to be
  more explicit to ease understanding of configuration by newcomers."* So the shell
  precedent is not an argument the proposal failed to make — it is an argument that was
  **considered and rejected**, on the grounds that shell fluency is the wrong bar for
  HCL's audience.
- **The workaround: put it in its own file.** *"factor out such scripts into a separate
  file and include it using the `file` function… This is often easier to maintain for
  non-trivial scripts anyway, since e.g. a text editor can use its highlighting and
  tooling for the shell syntax rather than just seeing it as a nested string literal."*

That third reason is the uncomfortable one, and the reason this section ends where it
does. It is, nearly word for word, the argument **this document already makes** for
declining embedding in favor of sibling `.cty` sources. The person who designed HCL2
reached the same conclusion eight years earlier, from the other direction. That is not a
coincidence to be argued around; it is corroboration. An embedded foreign language wants
its own file, and a config language is entitled to insist on it.

**What would remain, for anyone who wants to reopen it.** Three angles the 2017 thread
never had to answer — recorded for completeness, not as encouragement:

- **Generality.** The refusal partly leaned on *"it seems like you are primarily motivated
  by a Terraform-specific use-case here,"* explicitly conceding *"this doesn't generalize
  to other applications that use HCL of course."* A proposal framed around **HCL hosts
  embedding any foreign language**, needing a delimiter generic tooling can skip, is the
  case he said he was not being offered.
- **An explicit spelling.** The objection is to *quotes*, not to raw heredocs in
  principle, and he closed with *"I certainly will keep this use-case in mind in case we
  find other opportunities to address the original concern in a different way."* Something
  self-announcing — `<<RAW EOT` — answers the subtlety complaint on its own terms.
- **`file()` is not equivalent for a *language*.** It resolves at eval time into an opaque
  string with no source positions: fine for a shell script that is inert data, useless for
  an embedded language that needs parse-time diagnostics at true line/col. The 2017 thread
  concerned a startup script, so this distinction never arose.

But observe where that leaves functy. For us, `file()`'s moral equivalent — a sibling
`.cty` source — is not a poor workaround; it is the recommendation. The only thing a raw
heredoc buys functy is co-location, which is precisely the benefit this document already
judged not worth its price. **The honest status is therefore not "worth pursuing upstream"
but "already refused, on reasoning we agree with."**

**The costs.** The cosmetic one is that `<<-'FUNCTY'` … `FUNCTY` is simply uglier than a
brace-delimited block. It is the price of the region announcing itself, and an ugly
available option beats an elegant unavailable one.

The philosophical one is that the region becomes an attribute *value* rather than a
declaration site — the "embedding bodies as strings makes functions third-class citizens"
objection from the OpenTofu thread (quoted under the Terraform item in `FUTURE.md`). It is
weaker than it sounds: highlighting is TextMate injection either way (SQL-in-heredoc is a
well-worn path), diagnostics land at true positions either way, and the host recovers a
byte range from the AST either way. The string-ness is a fact about the AST, not about the
authoring experience.

The structural one is the only one with teeth: **HCL forbids duplicate attributes in a
body, so `functy = <<…` can appear only once per file.** The block form explicitly allowed
multiple `functy { }` regions to merge into one namespace; the heredoc form silently
withdraws that. One region per file is a defensible rule — arguably a tidier one — and it
is the recommended shape.

**A note on top-level attributes.** Neither host that motivates this design currently
permits an attribute at the top level of a config file: Vinculum's `configSchema`
(`config/blocks.go`) is an `hcl.BodySchema` with a `Blocks` list and no `Attributes` at
all, and Terraform's `configFileSchema` likewise enumerates only block types — there is no
top-level argument anywhere in that language. This is a *host schema* convention, not an
HCL restriction; HCL's native syntax permits attributes in any body, and lifting the ban
is one entry in the schema. So `functy = <<…` would be its host's first top-level
attribute. That is a novelty worth naming, but not an obstacle: the host must extend its
schema to recognize the region *whatever* form it takes, so "the host has to change" does
not discriminate between the options, and the attribute is the smaller of the two changes.

**Fallback: a block wrapper.** For a host unwilling to introduce a top-level attribute —
or one that wants multiple regions per file after all — the same heredoc works inside an
ordinary block, at the cost of a nesting level:

```hcl
functy "helpers" {
  source = <<-'FUNCTY'
      func double(n: number) -> number { return n * 2 }
  FUNCTY
}
```

This needs no schema novelty — just one more entry in a block list the host already keeps
— it restores multiplicity and labels, and it hands the host a normal HCL block to decode
with a normal `hcl:"source"` field. It gives up nothing that made the heredoc attractive:
still self-describing to every tool, still an exact `SrcRange` for true-position lexing,
still no forbidden lexeme inside the body. The bare attribute is preferred for being
tidier at the call site; this is what to reach for if that preference meets resistance.

**The maximal ask: a distinct block-body delimiter.** If the region should be a genuine
declaration site rather than an attribute value, HCL would need a body form that is
visibly *not* an HCL body — so that tools need not guess whether a given block's braces
contain HCL or something else. Something like `<{ … }>` in place of `{ … }`:

```hcl
myblock "labels" {
  ...
}

myembed "may or may not have labels" <{
  ... functy source ...
}>
```

The delimiter must be one the embedded language cannot itself produce; `<< … >>` is the
obvious first guess and the wrong one, since `>>` is a plausible operator in any C-family
DSL (functy included). `<{ … }>` dodges that, but only by luck — the general answer is
the heredoc's, a *user-chosen* terminator, which is another argument for the minimal form
above.

**Status.** Both asks are recorded, neither is pursued. The maximal form asks HCL to grow
language surface *and* asks every tool in the ecosystem to learn it — a big ask, and an
unlikely one. The minimal form is a small and well-precedented change that would genuinely
unblock everything above, but it has already been put to HCL and declined (#207), and the
grounds of that refusal are grounds this document independently agrees with. Reviving it
would mean arguing HCL out of a position functy has itself adopted.

The whole chain terminates in one sentence, which is the real finding of this document:
**an embedded foreign language wants its own file.** Every mechanism explored here is a
way of not believing that, and each one ends up paying for the disbelief — in overlay
buffers, in an HCL fork, in files that only their host can read, or in a language-surface
argument with upstream that was settled in 2017. The status quo — functy in `.cty` files,
loaded alongside the host's HCL — is not the compromise. It is the answer.

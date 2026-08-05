package functy

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/ext/typeexpr"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
)

// TypeConstraint is a resolved type annotation: it knows how to coerce a value to
// satisfy the annotation and exposes the underlying cty.Type. A nil
// TypeConstraint means "dynamic" (no annotation) — no coercion is applied.
//
// It is the single source of truth for a declared type. Cty() derives the
// cty.Type, so callers that only need the type (host introspection, generated
// docs) need not carry it separately; Coerce() applies the enforcement.
//
// Three coercion disciplines exist: structural/primitive annotations convert the
// value (cty/convert); a named (capsule) type is checked by identity; an open
// type is checked by a predicate and otherwise passed through untouched. This is
// why functy owns its resolver rather than delegating to ext/typeexpr, whose
// closed grammar cannot name capsule types or express open ("at least these
// attributes") types — and why a bare cty.Type cannot represent every constraint
// (a predicate type's Cty() is only dynamic).
type TypeConstraint interface {
	// Coerce applies the constraint to a value, returning the value to store or
	// an error if it does not satisfy the constraint.
	Coerce(cty.Value) (cty.Value, error)
	// Cty returns the underlying cty.Type (dynamic for `any`, void, and open
	// predicate types).
	Cty() cty.Type
	String() string
}

// convertConstraint converts values to a concrete cty type (primitives,
// collections, and structural object/tuple types).
type convertConstraint struct{ ty cty.Type }

func (c convertConstraint) Coerce(v cty.Value) (cty.Value, error) { return convert.Convert(v, c.ty) }
func (c convertConstraint) Cty() cty.Type                         { return c.ty }

// String renders the constraint in functy's own type-annotation grammar, the syntax it
// was written in — `list(string)`, `object({ a = string })` — rather than cty's prose
// FriendlyName, which says "list of string" and flattens every object to bare "object".
// This is what a signature shows, so it has to round-trip back through the resolver.
func (c convertConstraint) String() string { return TypeString(c.ty) }

// defaultedConstraint is a convertConstraint whose annotation carried at least one
// optional object attribute with a default (`optional(T, default)`). Before converting,
// Coerce fills any missing/null optional attribute from the nested defaults tree, so an
// absent attribute takes its declared default. The tree is an ext/typeexpr.Defaults built
// by functy's own resolver; its Apply is the same routine Terraform/OpenTofu use, which is
// why the semantics match theirs exactly.
type defaultedConstraint struct {
	ty       cty.Type
	defaults *typeexpr.Defaults
}

// Coerce applies defaults first (permissive, never errors) then converts, matching
// typeexpr's documented usage — conversion is where a genuinely wrong-typed value is
// reported, with the better context of the final type.
func (c defaultedConstraint) Coerce(v cty.Value) (cty.Value, error) {
	return convert.Convert(c.defaults.Apply(v), c.ty)
}
func (c defaultedConstraint) Cty() cty.Type { return c.ty }

// String renders the annotation including its defaults (`optional(T, <literal>)`), so a
// signature round-trips back through the resolver — the plain typeString would drop the
// default, since the cty.Type does not carry it.
func (c defaultedConstraint) String() string { return typeStringDefaults(c.ty, c.defaults) }

// anyConstraint is the explicit `any` annotation: every value passes through.
type anyConstraint struct{}

func (anyConstraint) Coerce(v cty.Value) (cty.Value, error) { return v, nil }
func (anyConstraint) Cty() cty.Type                         { return cty.DynamicPseudoType }
func (anyConstraint) String() string                        { return "any" }

// nullConstraint is the `null` (void) return type: the result is always null. It
// is only valid in the return type part of a function declaration.
type nullConstraint struct{}

func (nullConstraint) Coerce(cty.Value) (cty.Value, error) {
	return cty.NullVal(cty.DynamicPseudoType), nil
}
func (nullConstraint) Cty() cty.Type  { return cty.DynamicPseudoType }
func (nullConstraint) String() string { return "null" }

// identityConstraint enforces a named (capsule) type by identity: the value must
// already be of that type (or null), unless the type itself defines a conversion.
type identityConstraint struct {
	name string
	ty   cty.Type
}

func (c identityConstraint) Coerce(v cty.Value) (cty.Value, error) {
	if v.IsNull() {
		return cty.NullVal(c.ty), nil
	}
	if v.Type().Equals(c.ty) {
		return v, nil
	}
	if conv, err := convert.Convert(v, c.ty); err == nil {
		return conv, nil
	}
	return cty.NilVal, fmt.Errorf("expected %s, got %s", c.name, v.Type().FriendlyName())
}
func (c identityConstraint) Cty() cty.Type  { return c.ty }
func (c identityConstraint) String() string { return c.name }

// predicateConstraint enforces an open named type: the value must satisfy a
// predicate and is otherwise passed through untouched (non-destructive), so extra
// attributes survive. Null passes through.
//
// The pass-through is the point *and* the hazard: the constraint asserts only that
// pred held, never a Go representation. A host that type-asserts the value
// downstream is relying on pred to have validated exactly what that code assumes —
// see Parser.RegisterOpenType.
type predicateConstraint struct {
	name string
	pred func(cty.Value) error
}

func (c predicateConstraint) Coerce(v cty.Value) (cty.Value, error) {
	if v.IsNull() {
		return v, nil
	}
	if err := c.pred(v); err != nil {
		return cty.NilVal, fmt.Errorf("value does not satisfy %s: %w", c.name, err)
	}
	return v, nil
}
func (c predicateConstraint) Cty() cty.Type  { return cty.DynamicPseudoType }
func (c predicateConstraint) String() string { return c.name }

// opaqueConstraint stands for a named type an extern declaration mentions that
// nobody registered here — it carries the *name* and enforces nothing.
//
// An extern names the types of the host that provides the functions it documents
// (`ctx`, `time`, `bytes`, …). Whoever reads the extern often is not that host: the
// functy CLI registers no types at all, so `functy check`, `functy symbols` and
// `functy fmt` would otherwise fail with "Unknown type" on exactly the files the
// feature exists to produce — and fmt refuses to reformat a file with any error at
// all, so an extern file could not even be formatted. Since an extern is never
// compiled and never called, the constraint is only ever *rendered*; standing in an
// unenforced name loses nothing and keeps extern files self-contained.
//
// The cost is that a mistyped name resolves instead of failing, so resolveType
// pairs this with a warning.
type opaqueConstraint struct{ name string }

func (c opaqueConstraint) Coerce(v cty.Value) (cty.Value, error) { return v, nil }
func (c opaqueConstraint) Cty() cty.Type                         { return cty.DynamicPseudoType }
func (c opaqueConstraint) String() string                        { return c.name }

// nestedOpaqueSummary marks the two errors resolveCtyType raises for a named type it
// cannot place in a nested position: an unregistered one, and a registered *open* one.
// In an extern — which documents rather than enforces — both degrade to an opaque
// rendering of the annotation from its source, the composite analogue of a bare
// unregistered name (see resolveTypeSpanAllowNull). Keyed on Summary so the parser can
// tell these apart from a genuinely malformed constructor ("Invalid type").
var nestedOpaqueSummary = map[string]bool{
	"Unknown type":               true,
	"Open type cannot be nested": true,
}

// diagsHaveSummary reports whether any error diagnostic in diags carries the given
// Summary.
func diagsHaveSummary(diags hcl.Diagnostics, summary string) bool {
	for _, d := range diags {
		if d.Severity == hcl.DiagError && d.Summary == summary {
			return true
		}
	}
	return false
}

// allNestedOpaque reports whether diags is non-empty and every error in it is a
// nested-name resolution failure (see nestedOpaqueSummary) rather than a structural one,
// so an extern can safely take the whole annotation opaquely instead.
func allNestedOpaque(diags hcl.Diagnostics) bool {
	saw := false
	for _, d := range diags {
		if d.Severity != hcl.DiagError {
			continue
		}
		if !nestedOpaqueSummary[d.Summary] {
			return false
		}
		saw = true
	}
	return saw
}

// opaqueTypeWarning is the diagnostic paired with an opaqueConstraint: a mistyped name
// resolves instead of failing, so the reader is told it was taken as an opaque name.
func opaqueTypeWarning(name string, subject hcl.Range) *hcl.Diagnostic {
	return &hcl.Diagnostic{
		Severity: hcl.DiagWarning,
		Summary:  "Unregistered type in extern declaration",
		Detail: fmt.Sprintf(
			"%q is not a type known here, so it is treated as an opaque name and not enforced. That is expected when the host that provides these functions registers it; check the spelling if not.", name),
		Subject: subject.Ptr(),
	}
}

// TypeResolver is functy's type system as a standalone, reusable component. It
// resolves functy type annotations (the same grammar used in `.cty` source) into
// TypeConstraints, against the built-in types plus any host-registered named
// types. It is usable independently of parsing functy programs — for example as a
// richer alternative to ext/typeexpr (a TypeConstraint carries enforcement via
// Coerce, not just a cty.Type), or for a host to type-check its own
// configuration (e.g. resolving a declared type and enforcing a value with
// Coerce).
//
// A Parser holds one (see Parser.Types); register named types once and use them
// both for parsing `.cty` files and for resolving standalone annotations.
type TypeResolver struct {
	env *typeEnv
}

// NewTypeResolver returns a resolver with functy's built-in types (including the
// `error` open type) and no host registrations.
func NewTypeResolver() *TypeResolver {
	return &TypeResolver{env: newTypeEnv()}
}

// RegisterType registers a named (capsule) type, enforced by type identity. See
// Parser.RegisterType. Returns the resolver for chaining.
func (r *TypeResolver) RegisterType(name string, ty cty.Type) *TypeResolver {
	r.env.registerType(name, ty)
	return r
}

// RegisterOpenType registers a named open type backed by a predicate. See
// Parser.RegisterOpenType. Returns the resolver for chaining.
func (r *TypeResolver) RegisterOpenType(name string, pred func(cty.Value) error) *TypeResolver {
	r.env.registerOpenType(name, pred)
	return r
}

// ResolveType resolves a parsed type-annotation expression (e.g. an HCL attribute
// value) into a TypeConstraint — the analog of typeexpr.TypeConstraint, but
// yielding a constraint that can enforce values, not just a cty.Type. `null` is
// not accepted here (it is only meaningful as a function return type).
func (r *TypeResolver) ResolveType(expr hcl.Expression) (TypeConstraint, hcl.Diagnostics) {
	return r.env.resolveType(expr, false)
}

// ParseType lexes a type annotation from source bytes and resolves it — a
// convenience for annotations stored as strings (e.g. a host config field).
func (r *TypeResolver) ParseType(src []byte, filename string) (TypeConstraint, hcl.Diagnostics) {
	expr, diags := hclsyntax.ParseExpression(src, filename, hcl.InitialPos)
	if diags.HasErrors() {
		return nil, diags
	}
	tc, rdiags := r.env.resolveType(expr, false)
	return tc, diags.Extend(rdiags)
}

// ConvertType wraps a concrete cty.Type as a TypeConstraint enforced by
// conversion (cty/convert). Useful for a host that already has a cty.Type — for
// example a backward-compatible path for a type that was specified some other way.
func ConvertType(ty cty.Type) TypeConstraint {
	return convertConstraint{ty: ty}
}

// typeEnv holds the host-registered named types consulted by the resolver. The
// built-in primitives, constructors, and pseudo-types are handled by the resolver
// directly and are not stored here.
type typeEnv struct {
	named map[string]TypeConstraint
}

func newTypeEnv() *typeEnv {
	e := &typeEnv{named: make(map[string]TypeConstraint)}
	// `error` is a built-in open type: the shape throw raises and catch binds.
	e.named["error"] = errorConstraint()
	return e
}

// clone returns a copy of the environment. parseSources resolves a parse call's
// type aliases into a clone so they stay scoped to that call — visible across all
// of its sources (aliases are project-scoped, not file-local), but not leaking
// back into the Parser's shared host registrations for a later, separate call.
func (e *typeEnv) clone() *typeEnv {
	n := newTypeEnv()
	for k, v := range e.named {
		n.named[k] = v
	}
	return n
}

// builtinTypeNames are the reserved built-in type keywords. A type alias may not
// redefine one.
var builtinTypeNames = map[string]bool{
	"string": true, "bool": true, "number": true, "any": true, "null": true,
	"list": true, "set": true, "map": true, "object": true, "tuple": true,
	"optional": true, "error": true,
}

func (e *typeEnv) registerType(name string, ty cty.Type) {
	e.named[name] = identityConstraint{name: name, ty: ty}
}

func (e *typeEnv) registerOpenType(name string, pred func(cty.Value) error) {
	e.named[name] = predicateConstraint{name: name, pred: pred}
}

// resolveType resolves a parsed annotation expression to a type constraint.
// allowNull permits the `null` (void) return type; where it is false (var/const/
// parameter annotations), `null` as a type is an error.
func (e *typeEnv) resolveType(expr hcl.Expression, allowNull bool) (TypeConstraint, hcl.Diagnostics) {
	return e.resolveTypeOpaque(expr, allowNull, false)
}

// resolveTypeOpaque is resolveType with control over what an unregistered named
// type means. With allowOpaque set — the extern-file case — an unknown *bare* name
// stands in as an opaqueConstraint plus a warning, rather than being an error; see
// opaqueConstraint for why. Nested positions (`list(ctx)`) are unaffected: they
// resolve through resolveCtyType, which knows only the built-in grammar.
func (e *typeEnv) resolveTypeOpaque(expr hcl.Expression, allowNull, allowOpaque bool) (TypeConstraint, hcl.Diagnostics) {
	if kw := hcl.ExprAsKeyword(expr); kw != "" {
		switch kw {
		case "any":
			return anyConstraint{}, nil
		case "string":
			return convertConstraint{cty.String}, nil
		case "bool":
			return convertConstraint{cty.Bool}, nil
		case "number":
			return convertConstraint{cty.Number}, nil
		case "null":
			if !allowNull {
				return nil, typeDiag(expr, "Invalid type",
					"null is only valid as a function return type (a void return); it cannot be used as a variable, constant, or parameter type.")
			}
			return nullConstraint{}, nil
		}
		if c, ok := e.named[kw]; ok {
			return c, nil
		}
		if allowOpaque {
			return opaqueConstraint{name: kw}, hcl.Diagnostics{opaqueTypeWarning(kw, expr.Range())}
		}
		return nil, typeDiag(expr, "Unknown type", fmt.Sprintf("%q is not a known type.", kw))
	}

	// Constructors (list/set/map/object/tuple) build a concrete cty type, coerced
	// by conversion. An `optional(T, default)` anywhere inside yields a defaults tree;
	// when present, the constraint also applies those defaults before converting.
	ty, defaults, diags := e.resolveCtyType(expr)
	if diags.HasErrors() {
		return nil, diags
	}
	if defaults != nil {
		return defaultedConstraint{ty: ty, defaults: defaults}, nil
	}
	return convertConstraint{ty}, nil
}

// resolveCtyType resolves an annotation to a concrete cty.Type using only the
// built-in grammar. It is used for nested positions (collection element types,
// object attributes, tuple elements), where open (predicate-backed) named types
// and null are not allowed — they have no single concrete cty type. Supporting
// open types in nested positions is future work (see FUTURE.md, "nested open
// types").
//
// It also returns an ext/typeexpr.Defaults tree carrying any optional-attribute
// defaults (`optional(T, default)`) found at or below this position, or nil when
// there are none — mirroring typeexpr's own getType. Defaults nest, so the tree is
// threaded up through every constructor: a collection wraps its element's defaults,
// a tuple/object collects its children's.
func (e *typeEnv) resolveCtyType(expr hcl.Expression) (cty.Type, *typeexpr.Defaults, hcl.Diagnostics) {
	if kw := hcl.ExprAsKeyword(expr); kw != "" {
		switch kw {
		case "any":
			return cty.DynamicPseudoType, nil, nil
		case "string":
			return cty.String, nil, nil
		case "bool":
			return cty.Bool, nil, nil
		case "number":
			return cty.Number, nil, nil
		case "null":
			return cty.NilType, nil, typeDiag(expr, "Invalid type",
				"null is only valid as a function return type.")
		}
		if c, ok := e.named[kw]; ok {
			// A type with a concrete cty.Type composes inside a collection or
			// structural type — a primitive/structural alias (convertConstraint),
			// `any`, or a named capsule type (identityConstraint), all enforced by
			// cty's own conversion (capsules convert only by identity / their own
			// ops). An open predicate type (error, host open types) has no concrete
			// cty.Type and would lose its non-destructive enforcement when nested —
			// and does not compose into a homogeneous collection anyway — so it stays
			// a whole-annotation leaf.
			switch cc := c.(type) {
			case convertConstraint:
				return cc.ty, nil, nil
			case identityConstraint:
				return cc.ty, nil, nil
			case anyConstraint:
				return cty.DynamicPseudoType, nil, nil
			default:
				return cty.NilType, nil, typeDiag(expr, "Open type cannot be nested",
					fmt.Sprintf("the open type %q can only be used as a whole annotation, not inside a collection or structural type.", kw))
			}
		}
		return cty.NilType, nil, typeDiag(expr, "Unknown type", fmt.Sprintf("%q is not a known type.", kw))
	}

	call, diags := hcl.ExprCall(expr)
	if diags.HasErrors() {
		return cty.NilType, nil, typeDiag(expr, "Invalid type",
			"A type must be a primitive (string, bool, number, any) or a constructor (list, set, map, object, tuple).")
	}

	switch call.Name {
	case "list", "set", "map":
		if len(call.Arguments) != 1 {
			return cty.NilType, nil, typeDiag(expr, "Invalid type", call.Name+" requires exactly one element type argument.")
		}
		elem, edefaults, ediags := e.resolveCtyType(call.Arguments[0])
		if ediags.HasErrors() {
			return cty.NilType, nil, ediags
		}
		var ty cty.Type
		switch call.Name {
		case "list":
			ty = cty.List(elem)
		case "set":
			ty = cty.Set(elem)
		default:
			ty = cty.Map(elem)
		}
		return ty, collectionDefaults(ty, edefaults), nil
	case "tuple":
		if len(call.Arguments) != 1 {
			return cty.NilType, nil, typeDiag(expr, "Invalid type", "tuple requires a single [...] list of element types.")
		}
		elems, ediags := hcl.ExprList(call.Arguments[0])
		if ediags.HasErrors() {
			return cty.NilType, nil, ediags
		}
		etys := make([]cty.Type, len(elems))
		children := make(map[string]*typeexpr.Defaults, len(elems))
		for i, el := range elems {
			ety, edefaults, eds := e.resolveCtyType(el)
			if eds.HasErrors() {
				return cty.NilType, nil, eds
			}
			etys[i] = ety
			if edefaults != nil {
				children[fmt.Sprintf("%d", i)] = edefaults
			}
		}
		ty := cty.Tuple(etys)
		return ty, structuredDefaults(ty, nil, children), nil
	case "object":
		if len(call.Arguments) != 1 {
			return cty.NilType, nil, typeDiag(expr, "Invalid type", "object requires a single { ... } attribute map.")
		}
		return e.resolveObjectType(call.Arguments[0])
	default:
		return cty.NilType, nil, typeDiag(expr, "Unknown type constructor",
			fmt.Sprintf("%q is not a known type constructor.", call.Name))
	}
}

func (e *typeEnv) resolveObjectType(arg hcl.Expression) (cty.Type, *typeexpr.Defaults, hcl.Diagnostics) {
	pairs, diags := hcl.ExprMap(arg)
	if diags.HasErrors() {
		return cty.NilType, nil, diags
	}
	attrs := make(map[string]cty.Type, len(pairs))
	var optional []string
	defaultValues := make(map[string]cty.Value)
	children := make(map[string]*typeexpr.Defaults)
	for _, pair := range pairs {
		name := hcl.ExprAsKeyword(pair.Key)
		if name == "" {
			return cty.NilType, nil, typeDiag(pair.Key, "Invalid object type",
				"Object attribute names must be identifiers.")
		}
		if _, seen := attrs[name]; seen {
			return cty.NilType, nil, typeDiag(pair.Key, "Invalid object type",
				fmt.Sprintf("duplicate attribute %q.", name))
		}
		valExpr := pair.Value
		// optional(T) marks an optional attribute; optional(T, default) also gives it a
		// default value that fills the attribute when absent (recorded in the defaults
		// tree, since cty.ObjectWithOptionalAttrs stores no default of its own).
		var defaultExpr hcl.Expression
		if call, cdiags := hcl.ExprCall(pair.Value); !cdiags.HasErrors() && call.Name == "optional" {
			switch len(call.Arguments) {
			case 1:
				// optional attribute, no default.
			case 2:
				defaultExpr = call.Arguments[1]
			default:
				return cty.NilType, nil, typeDiag(pair.Value, "Invalid optional attribute",
					"optional takes the attribute type and an optional default value: optional(T) or optional(T, default).")
			}
			optional = append(optional, name)
			valExpr = call.Arguments[0]
		}
		aty, adefaults, adiags := e.resolveCtyType(valExpr)
		if adiags.HasErrors() {
			return cty.NilType, nil, adiags
		}
		attrs[name] = aty
		if adefaults != nil {
			children[name] = adefaults
		}
		if defaultExpr != nil {
			// The default is a pure literal, evaluated with no context (parity with
			// typeexpr), then checked convertible to the attribute type.
			defaultVal, ddiags := defaultExpr.Value(nil)
			if ddiags.HasErrors() {
				return cty.NilType, nil, ddiags
			}
			converted, err := convert.Convert(defaultVal, aty)
			if err != nil {
				return cty.NilType, nil, typeDiag(defaultExpr, "Invalid default value for optional attribute",
					fmt.Sprintf("this default value is not compatible with the attribute's type: %s.", err))
			}
			defaultValues[name] = converted
		}
	}
	// ObjectWithOptionalAttrs with an empty optional list is exactly cty.Object.
	// structuredDefaults returns nil unless there is a default or a nested child, so a
	// required attribute whose type itself carries defaults still propagates them up.
	ty := cty.ObjectWithOptionalAttrs(attrs, optional)
	return ty, structuredDefaults(ty, defaultValues, children), nil
}

// collectionDefaults and structuredDefaults build the ext/typeexpr.Defaults nodes for
// collection and structural types. They mirror typeexpr's own unexported helpers (which
// functy cannot call): both return nil when there is nothing to store, so a type with no
// optional-attribute defaults produces a nil tree and the no-default coercion path is
// unchanged.
func collectionDefaults(ty cty.Type, defaults *typeexpr.Defaults) *typeexpr.Defaults {
	if defaults == nil {
		return nil
	}
	return &typeexpr.Defaults{
		Type:     ty,
		Children: map[string]*typeexpr.Defaults{"": defaults},
	}
}

func structuredDefaults(ty cty.Type, defaultValues map[string]cty.Value, children map[string]*typeexpr.Defaults) *typeexpr.Defaults {
	if len(defaultValues) == 0 && len(children) == 0 {
		return nil
	}
	defaults := &typeexpr.Defaults{Type: ty}
	if len(defaultValues) > 0 {
		defaults.DefaultValues = defaultValues
	}
	if len(children) > 0 {
		defaults.Children = children
	}
	return defaults
}

func typeDiag(expr hcl.Expression, summary, detail string) hcl.Diagnostics {
	return hcl.Diagnostics{{
		Severity: hcl.DiagError,
		Summary:  summary,
		Detail:   detail,
		Subject:  expr.Range().Ptr(),
	}}
}

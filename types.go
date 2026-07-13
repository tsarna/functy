package functy

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
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
func (c convertConstraint) String() string                        { return c.ty.FriendlyName() }

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
			return opaqueConstraint{name: kw}, hcl.Diagnostics{&hcl.Diagnostic{
				Severity: hcl.DiagWarning,
				Summary:  "Unregistered type in extern declaration",
				Detail: fmt.Sprintf(
					"%q is not a type known here, so it is treated as an opaque name and not enforced. That is expected when the host that provides these functions registers it; check the spelling if not.", kw),
				Subject: expr.Range().Ptr(),
			}}
		}
		return nil, typeDiag(expr, "Unknown type", fmt.Sprintf("%q is not a known type.", kw))
	}

	// Constructors (list/set/map/object/tuple) build a concrete cty type, coerced
	// by conversion.
	ty, diags := e.resolveCtyType(expr)
	if diags.HasErrors() {
		return nil, diags
	}
	return convertConstraint{ty}, nil
}

// resolveCtyType resolves an annotation to a concrete cty.Type using only the
// built-in grammar. It is used for nested positions (collection element types,
// object attributes, tuple elements), where open (predicate-backed) named types
// and null are not allowed — they have no single concrete cty type. Supporting
// open types in nested positions is future work (see FUTURE.md, "nested open
// types").
func (e *typeEnv) resolveCtyType(expr hcl.Expression) (cty.Type, hcl.Diagnostics) {
	if kw := hcl.ExprAsKeyword(expr); kw != "" {
		switch kw {
		case "any":
			return cty.DynamicPseudoType, nil
		case "string":
			return cty.String, nil
		case "bool":
			return cty.Bool, nil
		case "number":
			return cty.Number, nil
		case "null":
			return cty.NilType, typeDiag(expr, "Invalid type",
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
				return cc.ty, nil
			case identityConstraint:
				return cc.ty, nil
			case anyConstraint:
				return cty.DynamicPseudoType, nil
			default:
				return cty.NilType, typeDiag(expr, "Open type cannot be nested",
					fmt.Sprintf("the open type %q can only be used as a whole annotation, not inside a collection or structural type.", kw))
			}
		}
		return cty.NilType, typeDiag(expr, "Unknown type", fmt.Sprintf("%q is not a known type.", kw))
	}

	call, diags := hcl.ExprCall(expr)
	if diags.HasErrors() {
		return cty.NilType, typeDiag(expr, "Invalid type",
			"A type must be a primitive (string, bool, number, any) or a constructor (list, set, map, object, tuple).")
	}

	switch call.Name {
	case "list", "set", "map":
		if len(call.Arguments) != 1 {
			return cty.NilType, typeDiag(expr, "Invalid type", call.Name+" requires exactly one element type argument.")
		}
		elem, ediags := e.resolveCtyType(call.Arguments[0])
		if ediags.HasErrors() {
			return cty.NilType, ediags
		}
		switch call.Name {
		case "list":
			return cty.List(elem), nil
		case "set":
			return cty.Set(elem), nil
		default:
			return cty.Map(elem), nil
		}
	case "tuple":
		if len(call.Arguments) != 1 {
			return cty.NilType, typeDiag(expr, "Invalid type", "tuple requires a single [...] list of element types.")
		}
		elems, ediags := hcl.ExprList(call.Arguments[0])
		if ediags.HasErrors() {
			return cty.NilType, ediags
		}
		etys := make([]cty.Type, len(elems))
		for i, el := range elems {
			ety, eds := e.resolveCtyType(el)
			if eds.HasErrors() {
				return cty.NilType, eds
			}
			etys[i] = ety
		}
		return cty.Tuple(etys), nil
	case "object":
		if len(call.Arguments) != 1 {
			return cty.NilType, typeDiag(expr, "Invalid type", "object requires a single { ... } attribute map.")
		}
		return e.resolveObjectType(call.Arguments[0])
	default:
		return cty.NilType, typeDiag(expr, "Unknown type constructor",
			fmt.Sprintf("%q is not a known type constructor.", call.Name))
	}
}

func (e *typeEnv) resolveObjectType(arg hcl.Expression) (cty.Type, hcl.Diagnostics) {
	pairs, diags := hcl.ExprMap(arg)
	if diags.HasErrors() {
		return cty.NilType, diags
	}
	attrs := make(map[string]cty.Type, len(pairs))
	var optional []string
	for _, pair := range pairs {
		name := hcl.ExprAsKeyword(pair.Key)
		if name == "" {
			return cty.NilType, typeDiag(pair.Key, "Invalid object type",
				"Object attribute names must be identifiers.")
		}
		valExpr := pair.Value
		// optional(T) marks an optional attribute.
		if call, cdiags := hcl.ExprCall(pair.Value); !cdiags.HasErrors() && call.Name == "optional" {
			if len(call.Arguments) != 1 {
				return cty.NilType, typeDiag(pair.Value, "Invalid optional attribute",
					"optional(T) takes a single type argument.")
			}
			optional = append(optional, name)
			valExpr = call.Arguments[0]
		}
		aty, adiags := e.resolveCtyType(valExpr)
		if adiags.HasErrors() {
			return cty.NilType, adiags
		}
		attrs[name] = aty
	}
	if len(optional) > 0 {
		return cty.ObjectWithOptionalAttrs(attrs, optional), nil
	}
	return cty.Object(attrs), nil
}

func typeDiag(expr hcl.Expression, summary, detail string) hcl.Diagnostics {
	return hcl.Diagnostics{{
		Severity: hcl.DiagError,
		Summary:  summary,
		Detail:   detail,
		Subject:  expr.Range().Ptr(),
	}}
}

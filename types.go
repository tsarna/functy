package functy

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
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

// typeEnv holds the host-registered named types consulted by the resolver. The
// built-in primitives, constructors, and pseudo-types are handled by the resolver
// directly and are not stored here.
type typeEnv struct {
	named map[string]TypeConstraint
}

func newTypeEnv() *typeEnv {
	return &typeEnv{named: make(map[string]TypeConstraint)}
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
	"optional": true,
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
// object attributes, tuple elements), where named/capsule and null types are not
// yet allowed (deferred — see FUNCTY-SPEC §4.4 / §13).
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
			// A concrete alias (resolving to a plain cty.Type) may nest inside a
			// collection or structural type; a host capsule/open named type may
			// not (its non-destructive enforcement does not compose yet).
			switch cc := c.(type) {
			case convertConstraint:
				return cc.ty, nil
			case anyConstraint:
				return cty.DynamicPseudoType, nil
			default:
				return cty.NilType, typeDiag(expr, "Nested named type not supported",
					fmt.Sprintf("the named type %q cannot yet appear inside a collection or structural type.", kw))
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

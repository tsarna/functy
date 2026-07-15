package functy

import (
	"sort"
	"strings"

	"github.com/zclconf/go-cty/cty"
)

// richMarkers are the attribute names by which a "rich object" carries its
// underlying capsule: _capsule for value objects, _ctx for context objects. An
// object holding one of these (as a capsule) is a facade over that capsule, so
// typeof/typekind name it by the capsule (e.g. "bytes", "ctx") rather than exposing
// the object's internal fields. functy recognizes the marker only to *name* the
// type; behavior dispatch (tostring/length via Stringable/Lengthable) stays with
// the rich-object package.
var richMarkers = [...]string{"_capsule", "_ctx"}

// richCapsule reports the underlying capsule type of a rich object (an object with
// a _capsule/_ctx capsule attribute), or false if ty is not one.
func richCapsule(ty cty.Type) (cty.Type, bool) {
	if !ty.IsObjectType() {
		return cty.NilType, false
	}
	for _, m := range richMarkers {
		if ty.HasAttribute(m) {
			if at := ty.AttributeType(m); at.IsCapsuleType() {
				return at, true
			}
		}
	}
	return cty.NilType, false
}

// typeString renders a cty type in functy's own type-annotation grammar — the same
// syntax used in declarations — so it round-trips through the type resolver:
// `string`, `list(string)`, `map(number)`, `object({ a = string, b = bool })`,
// `tuple([string, number])`. Unlike hcl/v2/ext/typeexpr's TypeString it does not
// panic on capsule types (functy has them); a capsule — or a rich object wrapping
// one — renders as its type name. This is the value of typeof().
func typeString(ty cty.Type) string {
	switch {
	case ty == cty.String:
		return "string"
	case ty == cty.Bool:
		return "bool"
	case ty == cty.Number:
		return "number"
	case ty == cty.DynamicPseudoType:
		return "any"
	case ty.IsCapsuleType():
		return ty.FriendlyName()
	case ty.IsListType():
		return "list(" + typeString(ty.ElementType()) + ")"
	case ty.IsSetType():
		return "set(" + typeString(ty.ElementType()) + ")"
	case ty.IsMapType():
		return "map(" + typeString(ty.ElementType()) + ")"
	case ty.IsObjectType():
		if cap, ok := richCapsule(ty); ok {
			return cap.FriendlyName()
		}
		ats := ty.AttributeTypes()
		if len(ats) == 0 {
			return "object({})"
		}
		names := make([]string, 0, len(ats))
		for name := range ats {
			names = append(names, name)
		}
		sort.Strings(names)
		parts := make([]string, len(names))
		for i, name := range names {
			at := typeString(ats[name])
			// Optional attributes exist only in a type *constraint* (a declaration), not
			// in the type of a value, so this branch is dead for typeof() and live for
			// rendering a signature — where dropping the marker would misreport a
			// wholly optional object as one that demands every attribute.
			if ty.AttributeOptional(name) {
				at = "optional(" + at + ")"
			}
			parts[i] = name + " = " + at
		}
		return "object({ " + strings.Join(parts, ", ") + " })"
	case ty.IsTupleType():
		ets := ty.TupleElementTypes()
		parts := make([]string, len(ets))
		for i, et := range ets {
			parts[i] = typeString(et)
		}
		return "tuple([" + strings.Join(parts, ", ") + "])"
	default:
		return ty.FriendlyName()
	}
}

// typeKind renders just the top-level kind of a cty type — `string`, `number`,
// `bool`, `any`, `list`, `set`, `map`, `object`, `tuple`, or a capsule's name —
// dropping element and attribute detail. This is the value of typekind(), meant for
// dispatch ("is it a list?") where the full typeString would be too specific.
func typeKind(ty cty.Type) string {
	switch {
	case ty == cty.String:
		return "string"
	case ty == cty.Bool:
		return "bool"
	case ty == cty.Number:
		return "number"
	case ty == cty.DynamicPseudoType:
		return "any"
	case ty.IsCapsuleType():
		return ty.FriendlyName()
	case ty.IsListType():
		return "list"
	case ty.IsSetType():
		return "set"
	case ty.IsMapType():
		return "map"
	case ty.IsObjectType():
		if cap, ok := richCapsule(ty); ok {
			return cap.FriendlyName()
		}
		return "object"
	case ty.IsTupleType():
		return "tuple"
	default:
		return ty.FriendlyName()
	}
}

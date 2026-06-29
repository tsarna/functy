package functy

import (
	"fmt"

	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
)

// binding is a single variable slot: its current value and, for a pinned
// (typed) variable, the declared type that every assignment is converted to. A
// dynamic variable has ty == cty.NilType and accepts any value unchanged.
type binding struct {
	val cty.Value
	ty  cty.Type
}

// Scope is a chained lexical scope. Variable lookup walks outward through the
// parent chain; the nearest binding wins. Declare always creates a binding in
// the innermost scope (shadowing any outer one); Set reassigns the nearest
// existing binding, converting to its pinned type.
//
// dirty supports the interpreter's eval-context cache: it is set whenever a
// binding in this scope changes, so the interpreter knows to rebuild the merged
// *hcl.EvalContext before the next statement. Statements that cannot mutate a
// binding (a bare expression evaluated for its side effects, for instance) leave
// it clear, so consecutive such statements reuse one context.
type Scope struct {
	vars   map[string]*binding
	parent *Scope
	dirty  bool
}

// NewScope creates a scope with the given parent (nil for a function root). A
// fresh scope is dirty so the interpreter builds its context once before use.
func NewScope(parent *Scope) *Scope {
	return &Scope{
		vars:   make(map[string]*binding),
		parent: parent,
		dirty:  true,
	}
}

// Get looks up a variable's value by walking the scope chain outward.
func (s *Scope) Get(name string) (cty.Value, bool) {
	if b := s.find(name); b != nil {
		return b.val, true
	}
	return cty.NilVal, false
}

func (s *Scope) find(name string) *binding {
	for cur := s; cur != nil; cur = cur.parent {
		if b, ok := cur.vars[name]; ok {
			return b
		}
	}
	return nil
}

// Declare introduces a new binding in this (innermost) scope, shadowing any
// binding of the same name in an enclosing scope. For a typed declaration the
// initial value is converted to ty.
func (s *Scope) Declare(name string, ty cty.Type, val cty.Value) error {
	if ty != cty.NilType {
		conv, err := convert.Convert(val, ty)
		if err != nil {
			return fmt.Errorf("cannot assign to %q: %w", name, err)
		}
		val = conv
	}
	s.vars[name] = &binding{val: val, ty: ty}
	s.dirty = true
	return nil
}

// Set reassigns the nearest existing binding of name, converting the value to
// that binding's pinned type. It is an error if name is not declared in any
// enclosing scope, or if the value cannot be converted.
func (s *Scope) Set(name string, val cty.Value) error {
	b := s.find(name)
	if b == nil {
		return fmt.Errorf("%q is not declared; use \"var %s = ...\" to declare it", name, name)
	}
	if b.ty != cty.NilType {
		conv, err := convert.Convert(val, b.ty)
		if err != nil {
			return fmt.Errorf("cannot assign to %q: %w", name, err)
		}
		val = conv
	}
	b.val = val
	s.dirty = true
	return nil
}

// declared reports whether name is bound in this scope or any enclosing scope.
func (s *Scope) declared(name string) bool {
	return s.find(name) != nil
}

// ToMap flattens the scope chain into a single name->value map, inner scopes
// taking precedence over outer ones.
func (s *Scope) ToMap() map[string]cty.Value {
	result := make(map[string]cty.Value)
	s.collectInto(result)
	return result
}

func (s *Scope) collectInto(m map[string]cty.Value) {
	if s.parent != nil {
		s.parent.collectInto(m)
	}
	for k, b := range s.vars {
		m[k] = b.val
	}
}

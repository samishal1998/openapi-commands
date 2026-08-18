package oascmd

import (
	"fmt"
	"strings"
)

// SplitBodyPath splits a dotted envelope path ("data.attributes") into its
// segments. An empty string yields no segments.
func SplitBodyPath(path string) []string {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(path, ".") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// NestBody wraps values under path, so ["json"] turns {"a":1} into
// {"json":{"a":1}}. An empty path returns values unchanged.
func NestBody(path []string, values map[string]any) map[string]any {
	for i := len(path) - 1; i >= 0; i-- {
		values = map[string]any{path[i]: values}
	}
	return values
}

// ResolveBody computes the effective flag surface of op.Body: it unwraps a
// single-property envelope (or the one named by x-cli-body-unwrap or a
// hook), applies x-cli-body-wrap, sets Body.WrapPath, and decides
// Body.Flat.
//
// The rules, in order:
//
//  1. x-cli-body-wrap always contributes the outermost segments of
//     WrapPath, even when the spec does not describe that envelope.
//  2. Unwrapping: "none"/"false" disables it. An explicit path
//     ("payload", "data.attributes") must exist and be an object, else it
//     is an error. Otherwise ("", "auto", "true") the body is unwrapped
//     automatically while it has exactly one property and that property is
//     an object: {"json": {...}} yields the inner flags, {"a": {...},
//     "b": 1} does not.
//  3. The resolved properties must all be scalars or arrays of scalars.
//     Any remaining nested object leaves the body non-flat (--data only).
//  4. Two resolved properties deriving the same flag name is an error;
//     disambiguate with x-cli-flag-name or by disabling unwrapping.
func ResolveBody(op *Operation) error {
	b := op.Body
	if b == nil {
		return nil
	}
	wrap := SplitBodyPath(b.Ext.Wrap)
	props := b.Props

	unwrap, auto, err := unwrapMode(b.Ext.Unwrap)
	if err != nil {
		return err
	}
	var inner []string
	switch {
	case len(props) == 0:
		// Nothing to derive flags from; --data only.
	case auto:
		inner, props = autoUnwrap(props)
	case len(unwrap) > 0:
		inner, props, err = explicitUnwrap(unwrap, props)
		if err != nil {
			return err
		}
	}

	b.WrapPath = nil
	if len(wrap)+len(inner) > 0 {
		b.WrapPath = append(append([]string{}, wrap...), inner...)
	}
	b.Props = props
	b.Flat = len(props) > 0
	for _, p := range props {
		if p.Object != nil {
			b.Flat = false
			break
		}
	}
	if !b.Flat {
		b.Props = nil
		return nil
	}
	return checkFlagCollisions(b.Props)
}

// unwrapMode interprets the x-cli-body-unwrap value. It returns the
// explicit path (possibly empty) and whether automatic detection applies.
func unwrapMode(value string) (path []string, auto bool, err error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "auto", "true":
		return nil, true, nil
	case "none", "false", "off":
		return nil, false, nil
	}
	path = SplitBodyPath(value)
	if len(path) == 0 {
		return nil, false, fmt.Errorf("x-cli-body-unwrap: %q is not a property path", value)
	}
	return path, false, nil
}

// autoUnwrap descends while the body has exactly one property and that
// property is an object, returning the path taken and the inner properties.
func autoUnwrap(props []BodyProp) ([]string, []BodyProp) {
	var path []string
	for len(props) == 1 && props[0].Object != nil {
		path = append(path, props[0].Name)
		props = props[0].Object
	}
	return path, props
}

// explicitUnwrap follows a declared path, erroring when a segment is
// missing or is not an object.
func explicitUnwrap(path []string, props []BodyProp) ([]string, []BodyProp, error) {
	for i, segment := range path {
		found := false
		for _, p := range props {
			if p.Name != segment {
				continue
			}
			if p.Object == nil {
				return nil, nil, fmt.Errorf("x-cli-body-unwrap: %q is not an object property",
					strings.Join(path[:i+1], "."))
			}
			props = p.Object
			found = true
			break
		}
		if !found {
			return nil, nil, fmt.Errorf("x-cli-body-unwrap: no property %q in the request body",
				strings.Join(path[:i+1], "."))
		}
	}
	return path, props, nil
}

// checkFlagCollisions rejects two properties deriving the same flag name,
// which unwrapping can surface (two inner objects merged, or an inner name
// clashing with an x-cli-flag-name override).
func checkFlagCollisions(props []BodyProp) error {
	seen := map[string]string{}
	for _, p := range props {
		flag := FlagName(p.Name, p.Ext)
		if other, clash := seen[flag]; clash {
			return fmt.Errorf("body properties %q and %q both map to --%s; set x-cli-flag-name on one of them or disable unwrapping with x-cli-body-unwrap: none",
				other, p.Name, flag)
		}
		seen[flag] = p.Name
	}
	return nil
}

package oascmd

import (
	"strings"
	"unicode"
)

// CommandPath is the derived location of an operation in the command tree:
// zero or more group words followed by the leaf command name.
type CommandPath struct {
	// Groups are the nested parent command names, outermost first.
	Groups []string
	// Name is the leaf command name (usually the verb).
	Name string
}

// NameFunc derives the command path for an operation. DeriveCommandPath is
// the default; supply your own via Options.NameFunc to override.
type NameFunc func(op Operation) CommandPath

// DeriveCommandPath is the default naming rule. The derivation, precisely:
//
//  1. Group words: x-cli-group when set (split on spaces); otherwise the
//     first tag, split into words (camelCase, digits, "_", "-", " " are word
//     boundaries) and kebab-cased. An operation with no tag and no
//     x-cli-group has no group and its command attaches to the parent
//     directly.
//
//  2. Leaf words: x-cli-name when set (split on spaces; the last word is the
//     leaf, the preceding words extend the groups). Otherwise the
//     operationId is split into words; leading words that repeat the group
//     words (case-insensitively, ignoring a trailing "s") are dropped, the
//     next word is treated as the verb and the remaining words as the
//     resource. Leading resource words repeating the group words are
//     dropped the same way. The command path is then
//     groups + remaining-resource-words + verb, so tag "dns" +
//     operationId "getDnsRecords" -> "dns records get".
//
//  3. No operationId: the verb is the lower-case HTTP method and the
//     resource is the static path segments (path parameters skipped),
//     each kebab-cased, deduplicated against the group as in rule 2.
func DeriveCommandPath(op Operation) CommandPath {
	var groups []string
	if op.Ext.Group != "" {
		groups = strings.Fields(op.Ext.Group)
	} else if len(op.Tags) > 0 {
		groups = []string{kebab(op.Tags[0])}
	}

	if op.Ext.Name != "" {
		words := strings.Fields(op.Ext.Name)
		return CommandPath{
			Groups: append(groups, words[:len(words)-1]...),
			Name:   words[len(words)-1],
		}
	}

	var verb string
	var resource []string
	if op.ID != "" {
		words := splitWords(op.ID)
		if stripped := stripGroupPrefix(words, groups); len(stripped) > 0 {
			words = stripped
		}
		verb = words[0]
		resource = words[1:]
	} else {
		verb = strings.ToLower(op.Method)
		for _, seg := range strings.Split(op.Path, "/") {
			if seg == "" || strings.HasPrefix(seg, "{") {
				continue
			}
			resource = append(resource, kebab(seg))
		}
	}
	resource = stripGroupPrefix(resource, groups)
	return CommandPath{Groups: append(groups, resource...), Name: verb}
}

// stripGroupPrefix drops leading resource words that repeat the group words,
// comparing case-insensitively and ignoring a trailing "s" on either side.
func stripGroupPrefix(resource, groups []string) []string {
	for len(resource) > 0 && len(groups) > 0 {
		matched := false
		for _, g := range groups {
			if singular(resource[0]) == singular(g) {
				matched = true
				break
			}
		}
		if !matched {
			break
		}
		resource = resource[1:]
	}
	return resource
}

func singular(w string) string {
	w = strings.ToLower(w)
	return strings.TrimSuffix(w, "s")
}

// FlagName derives the flag name for a parameter or body property name:
// x-cli-flag-name when set, otherwise the kebab-cased name.
func FlagName(name string, ext ParamExtensions) string {
	if ext.FlagName != "" {
		return ext.FlagName
	}
	return kebab(name)
}

// kebab joins the words of s with hyphens, lower-cased.
func kebab(s string) string {
	return strings.Join(splitWords(s), "-")
}

// splitWords splits s on camelCase boundaries, digit boundaries, "_", "-",
// "." and spaces, returning lower-cased words.
func splitWords(s string) []string {
	var words []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			words = append(words, strings.ToLower(cur.String()))
			cur.Reset()
		}
	}
	runes := []rune(s)
	for i, r := range runes {
		switch {
		case r == '_' || r == '-' || r == ' ' || r == '.':
			flush()
		case unicode.IsUpper(r):
			// Boundary before an upper rune, except inside an
			// acronym run (previous rune upper and next rune not
			// lower).
			prevUpper := i > 0 && unicode.IsUpper(runes[i-1])
			nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
			if !prevUpper || nextLower {
				flush()
			}
			cur.WriteRune(r)
		case unicode.IsDigit(r):
			if i > 0 && !unicode.IsDigit(runes[i-1]) {
				flush()
			}
			cur.WriteRune(r)
		default:
			if i > 0 && unicode.IsDigit(runes[i-1]) {
				flush()
			}
			cur.WriteRune(r)
		}
	}
	flush()
	if len(words) == 0 {
		return []string{""}
	}
	return words
}

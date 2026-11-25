package types

import "strings"

// maxSchemaDepth bounds $ref inlining: a self-referencing model would otherwise
// expand until the stack runs out.
const maxSchemaDepth = 10

// droppedSchemaKeywords are annotations with no meaning for a tool call (or
// keywords outside every provider dialect). They only cost prompt tokens, and
// some providers reject schemas that carry them.
var droppedSchemaKeywords = map[string]bool{
	"$schema":       true,
	"$id":           true,
	"$anchor":       true,
	"$comment":      true,
	"$defs":         true,
	"definitions":   true,
	"title":         true,
	"examples":      true,
	"example":       true,
	"default":       true,
	"deprecated":    true,
	"readOnly":      true,
	"writeOnly":     true,
	"discriminator": true,
}

// SanitizeJSONSchema makes a schema published by a server (MCP) acceptable to
// every provider: local $refs are inlined, anyOf/oneOf unions are reduced to a
// single branch when they only spell nullability, allOf is merged and the
// annotations are dropped. It must stay lossless where it matters: a property
// published as a nullable anyOf has no "type", so re-deriving the schema from
// the typed ToolProperty emitted "type": "" and the provider rejected the whole
// request.
func SanitizeJSONSchema(schema map[string]interface{}) map[string]interface{} {
	if len(schema) == 0 {
		return nil
	}
	return sanitizeSchema(schema, schema, 0, map[string]bool{})
}

func sanitizeSchema(schema, root map[string]interface{}, depth int, open map[string]bool) map[string]interface{} {
	out := make(map[string]interface{}, len(schema))

	if ref, ok := schema["$ref"].(string); ok && depth < maxSchemaDepth {
		if name, target, ok := resolveLocalRef(ref, root); ok && !open[name] {
			open[name] = true
			mergeSchema(out, sanitizeSchema(target, root, depth+1, open))
			delete(open, name)
		}
	}

	if list, ok := schema["allOf"].([]interface{}); ok {
		for _, branch := range sanitizeBranches(list, root, depth, open) {
			mergeSchema(out, branch)
		}
	}

	for _, keyword := range []string{"anyOf", "oneOf"} {
		list, ok := schema[keyword].([]interface{})
		if !ok {
			continue
		}
		// A union of one real branch is just that branch (the usual pydantic
		// Optional[...] / Literal[...] shape). A genuine union has no portable
		// spelling, so it is dropped: an unconstrained property is still valid.
		if branches := sanitizeBranches(list, root, depth, open); len(branches) == 1 {
			mergeSchema(out, branches[0])
		}
	}

	for k, v := range schema {
		switch k {
		case "$ref", "allOf", "anyOf", "oneOf":
			continue
		case "type":
			if t, ok := normalizeSchemaType(v); ok {
				out["type"] = t
			}
		case "const":
			if _, hasEnum := schema["enum"]; !hasEnum {
				out["enum"] = []interface{}{v}
			}
		case "properties", "patternProperties":
			props, ok := v.(map[string]interface{})
			if !ok {
				continue
			}
			sanitized := make(map[string]interface{}, len(props))
			for name, prop := range props {
				propSchema, ok := prop.(map[string]interface{})
				if !ok {
					continue
				}
				sanitized[name] = sanitizeSchema(propSchema, root, depth+1, open)
			}
			out[k] = sanitized
		case "items":
			switch items := v.(type) {
			case map[string]interface{}:
				out[k] = sanitizeSchema(items, root, depth+1, open)
			case []interface{}:
				// Tuple validation has no counterpart in the providers' dialects.
				if len(items) > 0 {
					if first, ok := items[0].(map[string]interface{}); ok {
						out[k] = sanitizeSchema(first, root, depth+1, open)
					}
				}
			}
		case "additionalProperties", "not", "contains", "propertyNames", "if", "then", "else":
			switch sub := v.(type) {
			case bool:
				out[k] = sub
			case map[string]interface{}:
				out[k] = sanitizeSchema(sub, root, depth+1, open)
			}
		default:
			if !droppedSchemaKeywords[k] {
				out[k] = v
			}
		}
	}
	return out
}

func sanitizeBranches(list []interface{}, root map[string]interface{}, depth int, open map[string]bool) []map[string]interface{} {
	branches := make([]map[string]interface{}, 0, len(list))
	for _, branch := range list {
		schema, ok := branch.(map[string]interface{})
		if !ok {
			continue
		}
		sanitized := sanitizeSchema(schema, root, depth+1, open)
		if isNullSchema(sanitized) {
			continue // nullability has no portable spelling: the type is just absent
		}
		branches = append(branches, sanitized)
	}
	return branches
}

// isNullSchema reports a branch that only spells "null".
func isNullSchema(schema map[string]interface{}) bool {
	if t, ok := schema["type"].(string); !ok || t != "null" {
		return false
	}
	for _, k := range []string{"properties", "items", "enum", "anyOf", "oneOf", "allOf"} {
		if _, ok := schema[k]; ok {
			return false
		}
	}
	return true
}

// normalizeSchemaType keeps one type name: a list (["string", "null"]) is
// reported by its first non-null entry and an empty value is dropped instead of
// being sent as an invalid type.
func normalizeSchemaType(v interface{}) (string, bool) {
	switch t := v.(type) {
	case string:
		if t == "" {
			return "", false
		}
		return t, true
	case []interface{}:
		for _, item := range t {
			if s, ok := item.(string); ok && s != "" && s != "null" {
				return s, true
			}
		}
	case []string:
		for _, s := range t {
			if s != "" && s != "null" {
				return s, true
			}
		}
	}
	return "", false
}

func mergeSchema(dst, src map[string]interface{}) {
	for k, v := range src {
		dst[k] = v
	}
}

// resolveLocalRef resolves a same-document JSON pointer ("#/$defs/Name").
func resolveLocalRef(ref string, root map[string]interface{}) (string, map[string]interface{}, bool) {
	if !strings.HasPrefix(ref, "#/") {
		return "", nil, false
	}
	var current interface{} = root
	for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		m, ok := current.(map[string]interface{})
		if !ok {
			return "", nil, false
		}
		next, found := m[part]
		if !found {
			return "", nil, false
		}
		current = next
	}
	target, ok := current.(map[string]interface{})
	if !ok {
		return "", nil, false
	}
	return ref, target, true
}

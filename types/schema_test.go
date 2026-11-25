package types

import (
	"encoding/json"
	"reflect"
	"testing"
)

// blenderShapedSchema is the shape a pydantic/FastMCP server publishes: every
// Optional[...] parameter becomes an anyOf with a null branch and has no
// "type" of its own.
func blenderShapedSchema() map[string]interface{} {
	return map[string]interface{}{
		"$schema": "http://json-schema.org/draft-07/schema#",
		"title":   "get_scene_infoArguments",
		"type":    "object",
		"properties": map[string]interface{}{
			"root": map[string]interface{}{
				"anyOf":       []interface{}{map[string]interface{}{"type": "string"}, map[string]interface{}{"type": "null"}},
				"default":     nil,
				"description": "An object whose whole hierarchy to list.",
				"title":       "Root",
			},
		},
	}
}

func TestSanitizeJSONSchemaReducesNullableAnyOf(t *testing.T) {
	got := SanitizeJSONSchema(blenderShapedSchema())
	want := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"root": map[string]interface{}{
				"type":        "string",
				"description": "An object whose whole hierarchy to list.",
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sanitized = %#v, want %#v", got, want)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) == "" || containsEmptyType(encoded) {
		t.Fatalf("schema still carries an empty type: %s", encoded)
	}
}

func containsEmptyType(raw []byte) bool {
	var decoded interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return false
	}
	return hasEmptyType(decoded)
}

func hasEmptyType(v interface{}) bool {
	switch value := v.(type) {
	case map[string]interface{}:
		if t, ok := value["type"].(string); ok && t == "" {
			return true
		}
		for _, child := range value {
			if hasEmptyType(child) {
				return true
			}
		}
	case []interface{}:
		for _, child := range value {
			if hasEmptyType(child) {
				return true
			}
		}
	}
	return false
}

func TestSanitizeJSONSchemaInlinesRefs(t *testing.T) {
	got := SanitizeJSONSchema(map[string]interface{}{
		"type": "object",
		"$defs": map[string]interface{}{
			"Item": map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{"name": map[string]interface{}{"type": "string"}},
				"required":   []interface{}{"name"},
			},
		},
		"properties": map[string]interface{}{
			"items": map[string]interface{}{
				"type":  "array",
				"items": map[string]interface{}{"$ref": "#/$defs/Item"},
			},
		},
	})
	wantItems := map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{"name": map[string]interface{}{"type": "string"}},
		"required":   []interface{}{"name"},
	}
	props, _ := got["properties"].(map[string]interface{})
	items, _ := props["items"].(map[string]interface{})
	if !reflect.DeepEqual(items["items"], wantItems) {
		t.Fatalf("items = %#v, want %#v", items["items"], wantItems)
	}
	if _, ok := got["$defs"]; ok {
		t.Fatal("$defs must not survive: no provider accepts it")
	}
}

func TestSanitizeJSONSchemaStopsRecursiveRefs(t *testing.T) {
	// A self-referencing model must terminate and stay a valid schema.
	got := SanitizeJSONSchema(map[string]interface{}{
		"type": "object",
		"$defs": map[string]interface{}{
			"Node": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"next": map[string]interface{}{"$ref": "#/$defs/Node"},
				},
			},
		},
		"$ref": "#/$defs/Node",
	})
	if len(got) == 0 {
		t.Fatal("recursive schema collapsed to nothing")
	}
	if got["type"] != "object" {
		t.Fatalf("type = %v, want object", got["type"])
	}
	if _, err := json.Marshal(got); err != nil {
		t.Fatal(err)
	}
}

func TestSanitizeJSONSchemaNormalizesTypes(t *testing.T) {
	got := SanitizeJSONSchema(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"listy":  map[string]interface{}{"type": []interface{}{"string", "null"}},
			"empty":  map[string]interface{}{"type": ""},
			"consty": map[string]interface{}{"const": "fast"},
		},
	})
	props, _ := got["properties"].(map[string]interface{})
	if props["listy"].(map[string]interface{})["type"] != "string" {
		t.Fatalf("list type = %#v, want string", props["listy"])
	}
	if _, ok := props["empty"].(map[string]interface{})["type"]; ok {
		t.Fatalf("empty type survived: %#v", props["empty"])
	}
	if !reflect.DeepEqual(props["consty"].(map[string]interface{})["enum"], []interface{}{"fast"}) {
		t.Fatalf("const = %#v, want enum [fast]", props["consty"])
	}
}

func TestToSchemaOmitsEmptyType(t *testing.T) {
	prop := ToolProperty{Description: "nullable"}
	if _, ok := prop.ToSchema()["type"]; ok {
		t.Fatalf("empty type must be omitted: %#v", prop.ToSchema())
	}
}

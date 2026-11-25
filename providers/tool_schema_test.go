package providers

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"fuckyourcli/types"
)

// richMCPTool mirrors what a pydantic/FastMCP server publishes: a nullable
// parameter (anyOf + null) and an array of objects. Neither survives the typed
// ToolProperty, and re-deriving them from it emitted "type": "" — an invalid
// schema that made Anthropic reject the whole request (400 invalid_request_error).
func richMCPTool() types.Tool {
	return types.Tool{
		Type: "function",
		Function: types.ToolFunction{
			Name:        "blender__get_scene_info",
			Description: "Read the scene",
			Parameters: types.ToolParameters{
				Type:       "object",
				Properties: map[string]types.ToolProperty{"root": {Description: "An object whose whole hierarchy to list."}},
				RawSchema: types.SanitizeJSONSchema(map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"root": map[string]interface{}{
							"anyOf":       []interface{}{map[string]interface{}{"type": "string"}, map[string]interface{}{"type": "null"}},
							"description": "An object whose whole hierarchy to list.",
						},
						"objects": map[string]interface{}{
							"type":  "array",
							"items": map[string]interface{}{"type": "object", "properties": map[string]interface{}{"name": map[string]interface{}{"type": "string"}}},
						},
					},
					"required": []interface{}{"objects"},
				}),
			},
		},
	}
}

func mustJSON(t *testing.T, v interface{}) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestProviderConversionsKeepMCPSchema(t *testing.T) {
	tools := []types.Tool{richMCPTool()}
	converted := map[string]string{
		"anthropic": mustJSON(t, convertToolsToAnthropic(tools)),
		"openai":    mustJSON(t, convertToolsToOpenAI(tools)),
		"codex":     mustJSON(t, convertToolsToCodex(tools)),
		"gemini":    mustJSON(t, convertToolsToGeminiNative(tools)),
	}
	for name, got := range converted {
		if strings.Contains(got, `"type":""`) {
			t.Fatalf("%s: empty type in schema: %s", name, got)
		}
		if !strings.Contains(got, `"root":{"description":"An object whose whole hierarchy to list.","type":"string"}`) {
			t.Fatalf("%s: nullable parameter lost: %s", name, got)
		}
		if !strings.Contains(got, `"objects":{"items":{"properties":{"name":{"type":"string"}},"type":"object"},"type":"array"}`) {
			t.Fatalf("%s: nested array items lost: %s", name, got)
		}
	}
}

func TestProviderConversionsKeepTypedTools(t *testing.T) {
	tools := []types.Tool{{
		Type: "function",
		Function: types.ToolFunction{
			Name: "sh",
			Parameters: types.ToolParameters{
				Type:       "object",
				Properties: map[string]types.ToolProperty{"command": {Type: "string", Description: "cmd"}},
				Required:   []string{"command"},
			},
		},
	}}
	want := map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{"command": map[string]interface{}{"type": "string", "description": "cmd"}},
		"required":   []string{"command"},
	}
	if got := convertToolsToAnthropic(tools)[0].InputSchema; !reflect.DeepEqual(got, want) {
		t.Fatalf("anthropic schema = %#v, want %#v", got, want)
	}
	openai := convertToolsToOpenAI(tools)[0].Function.Parameters
	if got := mustJSON(t, openai); got != `{"properties":{"command":{"description":"cmd","type":"string"}},"required":["command"],"type":"object"}` {
		t.Fatalf("openai schema = %s", got)
	}
}

func TestGeminiDropsEmptyProperties(t *testing.T) {
	tools := []types.Tool{{
		Type:     "function",
		Function: types.ToolFunction{Name: "noargs", Parameters: types.ToolParameters{Type: "object"}},
	}}
	decl := convertToolsToGeminiNative(tools)[0].FunctionDeclarations[0]
	if _, ok := decl.Parameters["properties"]; ok {
		t.Fatalf("empty properties must be omitted: %#v", decl.Parameters)
	}
	if decl.Parameters["type"] != "object" {
		t.Fatalf("parameters = %#v, want type object", decl.Parameters)
	}
}

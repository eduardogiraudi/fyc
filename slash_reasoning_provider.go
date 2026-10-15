package main

import (
	"fmt"
	"strconv"
	"strings"

	"fuckyourcli/config"
	"fuckyourcli/ui"
)

// The per-provider reasoning override (config.ReasoningProviderConfig) is edited
// here, field by field. It is the typed escape hatch for providers the built-in
// transport tables (reasoning/profiles.go) and the models.dev catalog cannot
// describe: the user states *where* the thinking fields go on the wire.
//
// Every edit is persisted immediately, so Esc never loses work; a dismissal
// (Esc/q) always returns to the field list, and the field list returns to the
// reasoning-mode menu.

// fieldKind drives how a row is presented and edited.
type fieldKind int

const (
	fieldMode    fieldKind = iota // reasoning mode selector for this provider
	fieldText                     // free text (a wire path, an effort label)
	fieldAny                      // JSON-ish scalar: true, "enabled", 4096
	fieldList                     // comma-separated list
	fieldStrMap                   // label=value,label=value
	fieldInt                      // positive/negative integer
	fieldIntMap                   // label=123,label=456
	fieldJSONMap                  // JSON object
	fieldBool                     // toggled in place
)

// reasoningField is one editable row of the override.
type reasoningField struct {
	key   string
	kind  fieldKind
	label string // already rendered, including the current value
}

// reasoningFieldRows builds the editable rows for the current override. The
// order is stable so muscle memory works; the value is always shown inline.
func reasoningFieldRows(pc config.ReasoningProviderConfig) []reasoningField {
	p := config.ReasoningProfileConfig{}
	if pc.Profile != nil {
		p = *pc.Profile
	}
	mode := strings.TrimSpace(pc.Intent.Mode)
	if mode == "" {
		mode = "inherit"
	}
	return []reasoningField{
		{key: "mode", kind: fieldMode, label: fieldRow("Mode for this provider", mode+" ("+pc.Intent.Label()+")")},
		{key: "effort", kind: fieldText, label: fieldRow("Effort label (mode=effort)", pc.Intent.Effort)},
		{key: "budget", kind: fieldInt, label: fieldRow("Budget tokens (mode=budget)", intOrEmpty(pc.Intent.Budget))},
		{key: "toggle_path", kind: fieldText, label: fieldRow("Toggle path", p.TogglePath)},
		{key: "toggle_on", kind: fieldAny, label: fieldRow("Toggle ON value", anyOrEmpty(p.ToggleEnabledValue))},
		{key: "toggle_off", kind: fieldAny, label: fieldRow("Toggle OFF value", anyOrEmpty(p.ToggleDisabledValue))},
		{key: "toggle_adaptive", kind: fieldAny, label: fieldRow("Toggle ADAPTIVE value", anyOrEmpty(p.ToggleAdaptiveValue))},
		{key: "toggle_from_control", kind: fieldBool, label: fieldRow("Send the toggle with effort/budget", boolText(p.ToggleFromControl))},
		{key: "cannot_disable", kind: fieldBool, label: fieldRow("Thinking cannot be disabled", boolText(p.CannotDisable))},
		{key: "effort_path", kind: fieldText, label: fieldRow("Effort path", p.EffortPath)},
		{key: "effort_values", kind: fieldList, label: fieldRow("Effort levels", strings.Join(p.EffortValues, ","))},
		{key: "effort_map", kind: fieldStrMap, label: fieldRow("Effort map (label=wire)", formatStringMap(p.EffortMap))},
		{key: "budget_path", kind: fieldText, label: fieldRow("Budget path", p.BudgetPath)},
		{key: "budget_min", kind: fieldInt, label: fieldRow("Budget minimum", intOrEmpty(p.BudgetMin))},
		{key: "budget_max", kind: fieldInt, label: fieldRow("Budget maximum", intOrEmpty(p.BudgetMax))},
		{key: "budget_from_effort", kind: fieldIntMap, label: fieldRow("Budget per effort (label=123)", formatIntMap(p.BudgetFromEffort))},
		{key: "adaptive_budget", kind: fieldInt, label: fieldRow("Budget for Adaptive", intOrEmpty(p.AdaptiveBudget))},
		{key: "forbids_temperature", kind: fieldBool, label: fieldRow("Rejects temperature", boolText(p.ForbidsTemperature))},
		{key: "always", kind: fieldJSONMap, label: fieldRow("Always-sent fields (JSON)", formatAnyMap(p.Always))},
		{key: "alongside_control", kind: fieldJSONMap, label: fieldRow("Fields sent with a control (JSON)", formatAnyMap(p.AlongsideControl))},
	}
}

// fieldRow renders one row: "<name>: <value>", with "(unset)" for an empty
// value and a hard cap so a long path cannot break the selector layout.
func fieldRow(name, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = "(unset)"
	}
	if len(value) > 60 {
		value = value[:57] + "..."
	}
	return name + ": " + value
}

// applyReasoningFieldEdit writes one textual edit into the override. An empty
// value clears the field (the entry itself disappears once nothing is left).
// It is pure, so the mapping is unit-testable without the TUI.
func applyReasoningFieldEdit(pc *config.ReasoningProviderConfig, key, value string) error {
	value = strings.TrimSpace(value)
	switch key {
	case "mode":
		return applyReasoningModeEdit(pc, value)
	case "effort":
		pc.Intent.Effort = value
		return nil
	case "budget":
		n, err := parseOptionalInt(value)
		if err != nil {
			return fmt.Errorf("budget must be a number")
		}
		pc.Intent.Budget = n
		return nil
	}

	p := pc.Profile
	if p == nil {
		p = &config.ReasoningProfileConfig{}
	}
	switch key {
	case "toggle_path":
		p.TogglePath = value
	case "toggle_on", "toggle_off", "toggle_adaptive":
		v, err := parseOptionalValue(value)
		if err != nil {
			return fmt.Errorf("expected a JSON value (true, \"enabled\", 4096)")
		}
		switch key {
		case "toggle_on":
			p.ToggleEnabledValue = v
		case "toggle_off":
			p.ToggleDisabledValue = v
		default:
			p.ToggleAdaptiveValue = v
		}
	case "toggle_from_control":
		p.ToggleFromControl = value == "true"
	case "cannot_disable":
		p.CannotDisable = value == "true"
	case "effort_path":
		p.EffortPath = value
	case "effort_values":
		p.EffortValues = splitList(value)
	case "effort_map":
		m, err := parseStringMap(value)
		if err != nil {
			return err
		}
		p.EffortMap = m
	case "budget_path":
		p.BudgetPath = value
	case "budget_min":
		n, err := parseOptionalInt(value)
		if err != nil {
			return fmt.Errorf("budget minimum must be a number")
		}
		p.BudgetMin = n
	case "budget_max":
		n, err := parseOptionalInt(value)
		if err != nil {
			return fmt.Errorf("budget maximum must be a number")
		}
		p.BudgetMax = n
	case "budget_from_effort":
		m, err := parseIntMap(value)
		if err != nil {
			return err
		}
		p.BudgetFromEffort = m
	case "adaptive_budget":
		n, err := parseOptionalInt(value)
		if err != nil {
			return fmt.Errorf("adaptive budget must be a number")
		}
		p.AdaptiveBudget = n
	case "forbids_temperature":
		p.ForbidsTemperature = value == "true"
	case "always":
		m, err := parseJSONMap(value)
		if err != nil {
			return err
		}
		p.Always = m
	case "alongside_control":
		m, err := parseJSONMap(value)
		if err != nil {
			return err
		}
		p.AlongsideControl = m
	default:
		return fmt.Errorf("unknown field %q", key)
	}
	pc.Profile = p
	return nil
}

// applyReasoningModeEdit handles the mode row. "inherit" drops the provider
// intent; "effort:<label>" and "budget:<n>" set both the mode and its parameter
// in one step, which is what the selector does.
func applyReasoningModeEdit(pc *config.ReasoningProviderConfig, value string) error {
	switch {
	case value == "" || value == "inherit":
		pc.Intent = config.ReasoningConfig{}
	case value == "default" || value == "off" || value == "adaptive":
		pc.Intent.Mode = value
	case strings.HasPrefix(value, "effort:"):
		pc.Intent.Mode = "effort"
		pc.Intent.Effort = strings.TrimPrefix(value, "effort:")
	case strings.HasPrefix(value, "budget:"):
		n, err := strconv.Atoi(strings.TrimPrefix(value, "budget:"))
		if err != nil {
			return fmt.Errorf("budget must be a number")
		}
		pc.Intent.Mode = "budget"
		pc.Intent.Budget = n
	default:
		return fmt.Errorf("unknown mode %q", value)
	}
	return nil
}

// toggleReasoningField flips one boolean field in place.
func toggleReasoningField(pc config.ReasoningProviderConfig, key string) config.ReasoningProviderConfig {
	p := pc.Profile
	if p == nil {
		p = &config.ReasoningProfileConfig{}
	}
	switch key {
	case "toggle_from_control":
		p.ToggleFromControl = !p.ToggleFromControl
	case "cannot_disable":
		p.CannotDisable = !p.CannotDisable
	case "forbids_temperature":
		p.ForbidsTemperature = !p.ForbidsTemperature
	}
	pc.Profile = p
	return pc
}

// reasoningProviderFieldsMenu is the interactive editor for the active
// provider's override. It returns when the user goes back to the mode menu.
func reasoningProviderFieldsMenu(s *ChatSession, key string) {
	const (
		resetLabel = "Reset — remove this provider's override"
		backLabel  = "← Back"
	)
	for {
		pc, _ := config.ReasoningForProvider(key)
		rows := reasoningFieldRows(pc)
		options := make([]string, 0, len(rows)+2)
		for _, r := range rows {
			options = append(options, r.label)
		}
		resetIdx := len(rows)
		backIdx := len(rows) + 1
		options = append(options, resetLabel, backLabel)

		idx := s.Sink.SelectOption("Wire fields for provider: "+key, options)
		if idx < 0 || idx == backIdx {
			return // back to the reasoning-mode menu
		}
		if idx == resetIdx {
			config.ClearReasoningForProvider(key)
			config.Save()
			s.Sink.WriteText(ui.OkStyle.Render("Provider override removed: " + key))
			s.Sink.WriteNewline()
			continue
		}

		row := rows[idx]
		if row.kind == fieldBool {
			config.SetReasoningForProvider(key, toggleReasoningField(pc, row.key))
			config.Save()
			continue
		}

		value, ok := promptFieldValue(s, row, pc)
		if !ok {
			continue // Esc in the prompt: back to the field list
		}
		if err := applyReasoningFieldEdit(&pc, row.key, value); err != nil {
			s.Sink.WriteText(ui.InfoStyle.Render("Invalid value: " + err.Error()))
			s.Sink.WriteNewline()
			continue
		}
		config.SetReasoningForProvider(key, pc)
		config.Save()
	}
}

// promptFieldValue asks for the new value of one row. ok is false when the user
// cancels (Esc): the caller then redraws the field list.
func promptFieldValue(s *ChatSession, row reasoningField, pc config.ReasoningProviderConfig) (string, bool) {
	const backLabel = "← Back"
	if row.kind == fieldMode {
		options := []string{"inherit", "default", "off", "adaptive", "effort", "budget", backLabel}
		idx := s.Sink.SelectOption("Reasoning mode for this provider", options)
		if idx < 0 || idx == len(options)-1 {
			return "", false
		}
		switch options[idx] {
		case "effort":
			line, ok := promptLine("Effort label for this provider (e.g. high): ")
			if !ok || line == "" || isBack(line) {
				return "", false
			}
			return "effort:" + line, true
		case "budget":
			line, ok := promptLine("Budget tokens for this provider (e.g. 8192): ")
			if !ok || line == "" || isBack(line) {
				return "", false
			}
			return "budget:" + line, true
		}
		return options[idx], true
	}

	line, ok := promptLine(fieldPrompt(row))
	if !ok {
		return "", false // Esc: back to the field list
	}
	if isBack(line) {
		return "", false
	}
	return line, true
}

// fieldPrompt builds the inline prompt for a row, mentioning the expected shape
// and that an empty line clears the field.
func fieldPrompt(row reasoningField) string {
	shape := map[fieldKind]string{
		fieldText:    "a wire path, e.g. chat_template_kwargs.enable_thinking",
		fieldAny:     `true, false, "enabled" or a number`,
		fieldList:    "comma-separated levels, e.g. low,high,max",
		fieldStrMap:  "label=wire pairs, e.g. minimal=low,high=high",
		fieldInt:     "a number",
		fieldIntMap:  "label=number pairs, e.g. low=2048,high=16384",
		fieldJSONMap: `a JSON object, e.g. {"reasoning.summary":"auto"}`,
	}[row.kind]
	if shape == "" {
		shape = "a value"
	}
	return fmt.Sprintf("%s — %s (empty clears; 'back' returns): ", rowTitle(row.label), shape)
}

// rowTitle strips the rendered value from a row label, leaving the field name.
func rowTitle(label string) string {
	if i := strings.Index(label, ": "); i > 0 {
		return label[:i]
	}
	return label
}

// ── small parsers and formatters ─────────────────────────────────────────────

func boolText(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func intOrEmpty(v int) string {
	if v == 0 {
		return ""
	}
	return strconv.Itoa(v)
}

func anyOrEmpty(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func formatStringMap(m map[string]string) string {
	keys := sortedKeys(m)
	parts := make([]string, 0, len(m))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, ",")
}

func formatIntMap(m map[string]int) string {
	keys := sortedKeys(m)
	parts := make([]string, 0, len(m))
	for _, k := range keys {
		parts = append(parts, k+"="+strconv.Itoa(m[k]))
	}
	return strings.Join(parts, ",")
}

func formatAnyMap(m map[string]any) string {
	keys := sortedKeys(m)
	parts := make([]string, 0, len(m))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, m[k]))
	}
	return strings.Join(parts, ",")
}

// sortedKeys returns the map keys in a stable order (maps iterate randomly).
func sortedKeys[M ~map[string]V, V any](m M) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ { // insertion sort: tiny maps, no import
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// splitList parses a comma-separated list, trimming and dropping duplicates
// (the menu must not offer the same level twice).
func splitList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(value))
	for _, p := range strings.Split(value, ",") {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

func parseOptionalInt(value string) (int, error) {
	if value == "" {
		return 0, nil
	}
	return strconv.Atoi(value)
}

// parseOptionalValue interprets an empty string as "unset"; anything else goes
// through parseExtraValue, which accepts JSON scalars and falls back to text.
func parseOptionalValue(value string) (any, error) {
	if value == "" {
		return nil, nil
	}
	return parseExtraValue(value), nil
}

func parseStringMap(value string) (map[string]string, error) {
	if value == "" {
		return nil, nil
	}
	out := map[string]string{}
	for _, pair := range strings.Split(value, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		k, v, found := strings.Cut(pair, "=")
		k = strings.TrimSpace(k)
		if !found || k == "" {
			return nil, fmt.Errorf("expected label=value pairs, got %q", pair)
		}
		out[k] = strings.TrimSpace(v)
	}
	return out, nil
}

func parseIntMap(value string) (map[string]int, error) {
	if value == "" {
		return nil, nil
	}
	out := map[string]int{}
	for _, pair := range strings.Split(value, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		k, v, found := strings.Cut(pair, "=")
		k = strings.TrimSpace(k)
		if !found || k == "" {
			return nil, fmt.Errorf("expected label=number pairs, got %q", pair)
		}
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return nil, fmt.Errorf("%q is not a number", v)
		}
		out[k] = n
	}
	return out, nil
}

func parseJSONMap(value string) (map[string]any, error) {
	if value == "" {
		return nil, nil
	}
	parsed := parseExtraValue(value)
	m, ok := parsed.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected a JSON object, got %q", value)
	}
	return m, nil
}

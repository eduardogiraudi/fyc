package reasoning

// Transport identifies the wire protocol a provider speaks. The same model
// family can behave differently across transports (DeepSeek exposes reasoning
// differently on Chat Completions, on the Anthropic Messages API and on the
// Responses API), so a Profile is always keyed by transport as well as model.
type Transport string

const (
	ChatCompletions       Transport = "chat_completions"
	OpenAIResponses       Transport = "responses"
	AnthropicMessages     Transport = "anthropic_messages"
	GeminiGenerateContent Transport = "gemini_generate_content"
)

// Profile captures how one (transport, model) pair exposes thinking on the wire.
// It is data, not code: every documented quirk is a field here, so a new
// provider is a table entry rather than a new branch. An empty field means "this
// model does not expose that control"; Translate then warns and omits it, never
// sending a field the model might reject.
//
// Field paths are dotted, e.g. "thinking.budget_tokens" or
// "generationConfig.thinkingConfig.thinkingLevel". Translate nests them into a
// JSON object.
type Profile struct {
	Transport Transport
	Model     string

	// Toggle: a boolean-ish on/off field.
	//   TogglePath          where the toggle lives (e.g. "thinking").
	//   ToggleEnabledValue  value that turns thinking on.
	//   ToggleDisabledValue value that turns it off.
	//   ToggleAdaptiveValue value that asks the model to decide (Anthropic
	//                       "adaptive"); empty when unsupported.
	//   CannotDisable       the model always thinks: even "disabled" is
	//                       rejected, so the request must omit the field.
	TogglePath          string
	ToggleEnabledValue  any
	ToggleDisabledValue any
	ToggleAdaptiveValue any
	CannotDisable       bool

	// ToggleFromControl makes an enabled toggle accompany the effort/budget
	// control. Anthropic phase-1 requires thinking.type=enabled together with
	// thinking.budget_tokens.
	ToggleFromControl bool

	// Effort: a named level, e.g. "reasoning_effort": "high".
	EffortPath   string
	EffortValues []string          // supported wire labels; empty = unknown
	EffortMap    map[string]string // user label -> wire label; OmitAll drops it

	// Budget: an explicit token count. When EffortPath is empty but BudgetPath
	// is set, a named effort is resolved through BudgetFromEffort.
	BudgetPath       string
	BudgetMin        int
	BudgetMax        int
	BudgetFromEffort map[string]int
	// AdaptiveBudget is the budget value used for ModeAdaptive when there is no
	// ToggleAdaptiveValue (Gemini dynamic thinking uses -1).
	AdaptiveBudget int

	Always map[string]any

	// AlongsideControl holds fields sent only when an effort or budget control is
	// actually emitted (e.g. reasoning.summary on the Responses API, which must
	// not appear when thinking is off). Its keys are dotted paths.
	AlongsideControl map[string]any
	// ForbidsTemperature marks models that reject temperature/top_p on every
	// request; the caller must omit them. When the rejection depends on thinking
	// being on, use ForbidsTemperatureWithThinking instead.
	ForbidsTemperature bool

	// ForbidsTemperatureWithThinking marks models where temperature is only
	// incompatible while thinking is on (Anthropic phase 1/2: "temperature and
	// top_k are incompatible with thinking"). The field is dropped only when the
	// intent actually enables thinking, so an "off"/default request keeps the
	// user's temperature.
	ForbidsTemperatureWithThinking bool
}

// SupportsAdaptive reports whether ModeAdaptive can actually be encoded for this
// profile: either an adaptive toggle, or the budget-as-adaptive form (a budget
// path with a sentinel value and no toggle path). Translate and the /reasoning
// menu both use it, so the menu never offers a mode that would send nothing.
func (p Profile) SupportsAdaptive() bool {
	if p.TogglePath != "" && p.ToggleAdaptiveValue != nil {
		return true
	}
	return p.TogglePath == "" && p.BudgetPath != "" && p.AdaptiveBudget != 0
}

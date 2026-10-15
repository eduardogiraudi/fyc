package config

func GetToolStates() map[string]bool {
	states := make(map[string]bool, len(config.ToolStates))
	for name, enabled := range config.ToolStates {
		states[name] = enabled
	}
	return states
}

func SetToolEnabled(name string, enabled bool) {
	if config.ToolStates == nil {
		config.ToolStates = map[string]bool{}
	}
	config.ToolStates[name] = enabled
}

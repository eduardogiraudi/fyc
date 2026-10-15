package providers

// SilentOutputSink no-op: all operations are empty.
// Used for sub-agents and silent calls.
type SilentOutputSink struct{}

func NewSilentOutputSink() *SilentOutputSink { return &SilentOutputSink{} }

func (s *SilentOutputSink) WriteText(token string)                          {}
func (s *SilentOutputSink) WriteReasoning(delta string)                     {}
func (s *SilentOutputSink) StartSpinner(label string)                       {}
func (s *SilentOutputSink) StopSpinner()                                    {}
func (s *SilentOutputSink) WriteDiffCraft(add, del int)                     {}
func (s *SilentOutputSink) FinishDiffCraft()                                {}
func (s *SilentOutputSink) WriteToolOutput(label, res string)               {}
func (s *SilentOutputSink) WriteInterrupt()                                 {}
func (s *SilentOutputSink) WriteNewline()                                   {}
func (s *SilentOutputSink) WriteStats(providerInfo, tokenStats string)      {}
func (s *SilentOutputSink) Reset()                                          {}
func (s *SilentOutputSink) Finalize()                                       {}
func (s *SilentOutputSink) SelectOption(title string, options []string) int { return 0 }
func (s *SilentOutputSink) ReadLine() (string, bool)                        { return "", false }
func (s *SilentOutputSink) StartSubagent(id, label string)                  {}
func (s *SilentOutputSink) UpdateSubagent(id, status string)                {}
func (s *SilentOutputSink) FinishSubagent(id, label, result string)         {}
func (s *SilentOutputSink) StartSubagentBatch(count int)                    {}
func (s *SilentOutputSink) FinishSubagentBatch()                            {}
func (s *SilentOutputSink) StartSubagentPanel(interface{})                  {}

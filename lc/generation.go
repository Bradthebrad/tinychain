package lc

type ChatGeneration struct {
	Text           string         `json:"text,omitempty"`
	Message        BaseMessage    `json:"message"`
	GenerationInfo map[string]any `json:"generation_info,omitempty"`
	Type           string         `json:"type,omitempty"`
}

type LLMResult struct {
	Generations [][]ChatGeneration `json:"generations"`
	LLMOutput   map[string]any     `json:"llm_output,omitempty"`
	Run         []RunInfo          `json:"run,omitempty"`
	Type        string             `json:"type,omitempty"`
}

type RunInfo struct {
	RunID string `json:"run_id"`
}

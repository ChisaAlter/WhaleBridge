package gateway

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"

	"github.com/tidwall/gjson"

	"github.com/ChisaAlter/WhaleBridge/internal/provider"
)

// outputLimit is the most a reply of a provider's model may hold, where
// known: its catalog entry's (the user's setting, else the vendor's list
// or models.dev).
func outputLimit(p provider.Provider, model string) int {
	for _, e := range provider.Served() {
		if e.Group == "" && e.Provider.ID == p.ID && (e.Model == model || e.ID == model) && e.Output > 0 {
			return e.Output
		}
	}
	return 0
}

// userOutput is the Max output explicitly set for this provider's model.
// Only the user's value caps a plain provider request: a vendor's catalog
// may be stale, while a group's members are each capped by outputLimit.
func userOutput(p provider.Provider, model string) int {
	for _, e := range provider.Served() {
		if e.Group == "" && e.Provider.ID == p.ID && (e.Model == model || e.ID == model) && e.OutputSet && e.Output > 0 {
			return e.Output
		}
	}
	return 0
}

// askedOutput is the reply limit a request that names none is sent on
// Anthropic's Messages API, which needs one: the model's own as the agents
// are told it (the user's Max output, else the vendor's list or
// models.dev, within the window), or 0 when none is known. A Codex turn
// names none, and was sent a flat 16384, which a long thinking and tool
// turn ran out of, Codex showing "Incomplete response returned, reason:
// max_output_tokens" (#1792, tensornull).
func askedOutput(p provider.Provider, model string) int {
	for _, e := range provider.Served() {
		if e.Group == "" && e.Provider.ID == p.ID && (e.Model == model || e.ID == model) {
			return e.PublishedOutput()
		}
	}
	return 0
}

// defaultMaxTokens is what a request naming no reply limit is sent on
// Anthropic's API for a model whose limit isn't known.
const defaultMaxTokens = 16384

// withMaxOutput is body, in proto's words, asking for a reply of at most
// limit tokens: a request that asks for more is lowered to limit, and
// Anthropic's thinking budget, which must be under it, with it. A request
// that asks for limit or less, or names no limit, goes as it is.
func withMaxOutput(proto provider.Protocol, body []byte, limit int) []byte {
	var v struct {
		MaxTokens           int `json:"max_tokens"`
		MaxCompletionTokens int `json:"max_completion_tokens"`
		MaxOutputTokens     int `json:"max_output_tokens"`
		Thinking            *struct {
			Type   string `json:"type"`
			Budget int    `json:"budget_tokens"`
		} `json:"thinking"`
		GenerationConfig map[string]any `json:"generationConfig"`
	}
	if limit <= 0 || json.Unmarshal(body, &v) != nil {
		return body
	}
	fields := map[string]any{}
	switch proto {
	case provider.Chat:
		if v.MaxCompletionTokens > limit {
			fields["max_completion_tokens"] = limit
		}
		if v.MaxTokens > limit {
			fields["max_tokens"] = limit
		}
	case provider.Responses:
		if v.MaxOutputTokens > limit {
			fields["max_output_tokens"] = limit
		}
	case provider.Anthropic:
		if v.MaxTokens <= limit {
			return body
		}
		fields["max_tokens"] = limit
		if t := v.Thinking; t != nil && t.Type == "enabled" && t.Budget >= limit {
			if limit-1 >= 1024 {
				fields["thinking"] = map[string]any{"type": "enabled", "budget_tokens": limit - 1}
			} else {
				// no budget fits under it (Anthropic's least is 1024)
				fields["thinking"] = map[string]any{"type": "disabled"}
			}
		}
	case provider.Gemini:
		if n, ok := v.GenerationConfig["maxOutputTokens"].(float64); ok && int(n) > limit {
			v.GenerationConfig["maxOutputTokens"] = limit
			fields["generationConfig"] = v.GenerationConfig
		}
	}
	if len(fields) == 0 {
		return body
	}
	return withFields(body, fields)
}

// replyLeft uses the vendor's own prompt count, including protocol overhead,
// when the prompt fits but its requested reply pushes it past the window.
func replyLeft(msg []byte) int {
	window, prompt := 0, 0
	if m := windowAndPrompt.FindSubmatch(msg); m != nil {
		window, _ = strconv.Atoi(string(m[1]))
		prompt, _ = strconv.Atoi(string(m[2]))
		if m[2] == nil {
			prompt, _ = strconv.Atoi(string(m[3]))
		}
	} else if m := inputPlusReply.FindSubmatch(msg); m != nil {
		prompt, _ = strconv.Atoi(string(m[1]))
		window, _ = strconv.Atoi(string(m[2]))
	}
	if window <= 0 || prompt <= 0 || prompt >= window {
		return 0
	}
	return window - prompt
}

var windowAndPrompt = regexp.MustCompile(`(?is)maximum context length is (\d+) tokens.{0,200}?(?:\((\d+) in the messages, \d+ in the completion\)|your prompt contains at least (\d+) input tokens)`)
var inputPlusReply = regexp.MustCompile(`(?i)input length and .{0,3}max_tokens.{0,3} exceed context limit:\s*(\d+)\s*\+\s*\d+\s*>\s*(\d+)`)

const leastReplyLeft = 8192

// withReplyCap lowers a reply, or fills a missing Chat/Responses limit. An
// existing smaller limit is left alone, so a retry can happen only once.
func withReplyCap(proto provider.Protocol, body []byte, limit int) ([]byte, bool) {
	if limit <= 0 {
		return nil, false
	}
	out := withMaxOutput(proto, body, limit)
	if !bytes.Equal(out, body) {
		return out, true
	}
	var key string
	switch proto {
	case provider.Chat:
		if gjson.GetBytes(body, "max_tokens").Exists() || gjson.GetBytes(body, "max_completion_tokens").Exists() {
			return nil, false
		}
		key = "max_tokens"
	case provider.Responses:
		if gjson.GetBytes(body, "max_output_tokens").Exists() {
			return nil, false
		}
		key = "max_output_tokens"
	default:
		return nil, false
	}
	return withFields(body, map[string]any{key: limit}), true
}

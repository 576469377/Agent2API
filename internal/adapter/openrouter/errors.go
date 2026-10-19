package openrouter

import (
	"encoding/json"
	"regexp"
	"strings"
)

var errorCredentials = regexp.MustCompile(`(?i)\b(?:sk-[a-z0-9_-]{8,}|bearer\s+[a-z0-9._-]+|eyJ[a-z0-9_-]+\.[a-z0-9_-]+\.[a-z0-9_-]+)`)

func errorText(value string, limit int) string {
	value = errorCredentials.ReplaceAllString(value, "[redacted]")
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > limit {
		value = string(runes[:limit]) + "…"
	}
	return value
}

// OpenRouter wraps provider errors in metadata.raw, which can be an object or
// a JSON-encoded string. Extract only the error message, never dump the raw
// metadata (which can also contain request data and user identifiers).
func (e *apiError) summary(fallback string) string {
	message := errorText(e.Message, 512)
	if message == "" {
		message = fallback
	}
	if provider := errorText(e.Metadata.Provider, 80); provider != "" {
		message += " [" + provider + "]"
	}
	raw := e.Metadata.Raw
	var encoded string
	if json.Unmarshal(raw, &encoded) == nil {
		raw = []byte(encoded)
	}
	var detail struct {
		Message string `json:"message"`
		Msg     string `json:"msg"`
		Error   *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &detail) == nil {
		text := detail.Message
		if text == "" {
			text = detail.Msg
		}
		if text == "" && detail.Error != nil {
			text = detail.Error.Message
		}
		if text = errorText(text, 512); text != "" && text != errorText(e.Message, 512) {
			message += ": " + text
		}
	}
	return message
}

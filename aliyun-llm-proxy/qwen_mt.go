package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var qwenMTLanguages = map[string]string{
	"en": "English", "zh": "Chinese", "zh_tw": "Traditional Chinese", "ru": "Russian", "ja": "Japanese", "ko": "Korean", "es": "Spanish", "fr": "French", "pt": "Portuguese", "de": "German", "it": "Italian", "th": "Thai", "vi": "Vietnamese", "id": "Indonesian", "ms": "Malay", "ar": "Arabic", "hi": "Hindi", "he": "Hebrew", "my": "Burmese", "ta": "Tamil", "ur": "Urdu", "bn": "Bengali", "pl": "Polish", "nl": "Dutch", "ro": "Romanian", "tr": "Turkish", "km": "Khmer", "lo": "Lao", "yue": "Cantonese", "cs": "Czech", "el": "Greek", "sv": "Swedish", "hu": "Hungarian", "da": "Danish", "fi": "Finnish", "uk": "Ukrainian", "bg": "Bulgarian", "sr": "Serbian", "te": "Telugu", "af": "Afrikaans", "hy": "Armenian", "as": "Assamese", "ast": "Asturian", "eu": "Basque", "be": "Belarusian", "bs": "Bosnian", "ca": "Catalan", "ceb": "Cebuano", "hr": "Croatian", "arz": "Egyptian Arabic", "et": "Estonian", "gl": "Galician", "ka": "Georgian", "gu": "Gujarati", "is": "Icelandic", "jv": "Javanese", "kn": "Kannada", "kk": "Kazakh", "lv": "Latvian", "lt": "Lithuanian", "lb": "Luxembourgish", "mk": "Macedonian", "mai": "Maithili", "mt": "Maltese", "mr": "Marathi", "acm": "Mesopotamian Arabic", "ary": "Moroccan Arabic", "ars": "Najdi Arabic", "ne": "Nepali", "az": "North Azerbaijani", "apc": "North Levantine Arabic", "uz": "Northern Uzbek", "nb": "Norwegian Bokmål", "nn": "Norwegian Nynorsk", "oc": "Occitan", "or": "Odia", "pag": "Pangasinan", "scn": "Sicilian", "sd": "Sindhi", "si": "Sinhala", "sk": "Slovak", "sl": "Slovenian", "ajp": "South Levantine Arabic", "sw": "Swahili", "tl": "Tagalog", "acq": "Ta’izzi-Adeni Arabic", "sq": "Tosk Albanian", "aeb": "Tunisian Arabic", "vec": "Venetian", "war": "Waray", "cy": "Welsh", "fa": "Western Persian",
}

var qwenMTLiteLanguages = map[string]bool{
	"en": true, "zh": true, "zh_tw": true, "ru": true, "ja": true, "ko": true, "es": true, "fr": true, "pt": true, "de": true, "it": true, "th": true, "vi": true, "id": true, "ms": true, "ar": true, "hi": true, "he": true, "ur": true, "bn": true, "pl": true, "nl": true, "tr": true, "km": true, "cs": true, "sv": true, "hu": true, "da": true, "fi": true, "tl": true, "fa": true,
}

var qwenMTAliases = map[string]string{
	"simplified mandarin chinese": "zh", "traditional mandarin chinese": "zh_tw", "simplified chinese": "zh", "chinese (simplified)": "zh", "traditional chinese": "zh_tw", "chinese (traditional)": "zh_tw", "standard arabic": "ar", "javanese (javanese)": "jv", "iranian persian": "fa", "persian": "fa", "swahili (individual language)": "sw", "bosnian (cyrillic)": "bs", "serbian (cyrillic)": "sr", "northern uzbek (cyrillic)": "uz", "malay (individual language) (arabic)": "ms", "nepali (individual language)": "ne", "north azerbaijani (cyrillic)": "az", "modern greek (1453-)": "el", "albanian": "sq", "简体中文": "zh", "中文": "zh", "繁体中文": "zh_tw", "英语": "en", "英文": "en", "日语": "ja", "韩语": "ko", "法语": "fr", "德语": "de", "西班牙语": "es",
}

func normalizeLanguage(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
}

func qwenMTLanguageCode(value string) (string, bool) {
	key := normalizeLanguage(value)
	if key == "" {
		return "", false
	}
	if code, exists := qwenMTAliases[key]; exists {
		return code, true
	}
	code := strings.ReplaceAll(key, "-", "_")
	if _, exists := qwenMTLanguages[code]; exists {
		return code, true
	}
	for candidate, name := range qwenMTLanguages {
		if normalizeLanguage(name) == key {
			return candidate, true
		}
	}
	return "", false
}

func qwenMTModelSupports(modelID, code string) bool {
	if _, exists := qwenMTLanguages[code]; !exists {
		return false
	}
	return modelID != "qwen-mt-lite" || qwenMTLiteLanguages[code]
}

type qwenMTUnsupportedLanguage struct{ RawTarget, LanguageCode, ModelID string }

func (e qwenMTUnsupportedLanguage) Error() string {
	return fmt.Sprintf("%s does not support target language %q", e.ModelID, e.RawTarget)
}

func messageText(content any) string {
	if content == nil {
		return ""
	}
	if text, ok := content.(string); ok {
		return text
	}
	items, ok := content.([]any)
	if !ok {
		return fmt.Sprint(content)
	}
	parts := []string{}
	for _, raw := range items {
		if item, ok := raw.(map[string]any); ok && item["type"] == "text" {
			if text, ok := item["text"].(string); ok {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

var targetLanguagePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?is)(?:translate|translation).*?\b(?:into|to)\s+([A-Za-z][A-Za-z ()_-]{1,40}?)(?:[.,;:\n]|$)`),
	regexp.MustCompile(`(?is)target\s+language\s*[:：]\s*([A-Za-z][A-Za-z ()_-]{1,40}?)(?:[.,;:\n]|$)`),
	regexp.MustCompile(`(?is)(?:翻译成|翻译为|目标语言\s*[:：])\s*([^，。；;：:\n]{1,30})`),
}

func inferTargetLanguage(messages []any, fallback string) string {
	users, instructions := []string{}, []string{}
	for _, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role, _ := message["role"].(string)
		text := messageText(message["content"])
		if role == "user" {
			users = append(users, text)
		} else if role == "system" || role == "developer" {
			instructions = append(instructions, text)
		}
	}
	for left, right := 0, len(users)-1; left < right; left, right = left+1, right-1 {
		users[left], users[right] = users[right], users[left]
	}
	prompt := strings.Join(append(users, instructions...), "\n")
	for _, pattern := range targetLanguagePatterns {
		if match := pattern.FindStringSubmatch(prompt); len(match) > 1 {
			return strings.TrimSpace(match[1])
		}
	}
	return fallback
}

var sourceWrapperPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?is)^\s*translate\s+(?:the\s+following\s+(?:text|content)\s+)?(?:into|to)\s+[^:\n：]+\s*[:：]\s*(.+?)\s*$`),
	regexp.MustCompile(`(?is)^\s*(?:请)?(?:将)?(?:以下)?(?:文本|内容)?\s*翻译(?:成|为)\s*[^:\n：]+\s*[:：]\s*(.+?)\s*$`),
}

func mtSourceText(messages []any) string {
	source := ""
	for _, raw := range messages {
		if message, ok := raw.(map[string]any); ok && message["role"] == "user" {
			source = messageText(message["content"])
		}
	}
	for _, pattern := range sourceWrapperPatterns {
		if match := pattern.FindStringSubmatch(source); len(match) > 1 && strings.TrimSpace(match[1]) != "" {
			return strings.TrimSpace(match[1])
		}
	}
	return source
}

func upstreamPayload(body map[string]any, state *modelState) (map[string]any, error) {
	if state.Config.Adapter != "qwen-mt" {
		encoded, _ := json.Marshal(body)
		var copied map[string]any
		_ = json.Unmarshal(encoded, &copied)
		copied["model"] = state.Config.ID
		return copied, nil
	}
	messages, _ := body["messages"].([]any)
	fallback := state.Config.DefaultTargetLanguage
	if fallback == "" {
		fallback = "Chinese"
	}
	rawTarget := inferTargetLanguage(messages, fallback)
	code, exists := qwenMTLanguageCode(rawTarget)
	if !exists || !qwenMTModelSupports(state.Config.ID, code) {
		return nil, qwenMTUnsupportedLanguage{RawTarget: rawTarget, LanguageCode: code, ModelID: state.Config.ID}
	}
	payload := map[string]any{
		"model":               state.Config.ID,
		"messages":            []map[string]any{{"role": "user", "content": mtSourceText(messages)}},
		"translation_options": map[string]string{"source_lang": "auto", "target_lang": code},
		"stream":              body["stream"] == true,
	}
	if payload["stream"] == true {
		if options, ok := body["stream_options"].(map[string]any); ok {
			payload["stream_options"] = options
		}
	}
	return payload, nil
}

func isQwenMTLanguageError(status int, code, message string, model modelConfig) bool {
	if status != 400 || model.Adapter != "qwen-mt" {
		return false
	}
	normalizedCode, normalizedMessage := strings.ToLower(strings.TrimSpace(code)), strings.ToLower(strings.TrimSpace(message))
	if normalizedCode != "invalid_parameter_error" && normalizedCode != "invalidparameter" {
		return false
	}
	return strings.Contains(message, "不支持当前设置的语种") || strings.Contains(normalizedMessage, "unsupported language") || (strings.Contains(normalizedMessage, "language") && strings.Contains(normalizedMessage, "not support"))
}

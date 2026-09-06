package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
)

// Check names are stable report IDs. Unselected checks remain visible as skipped.
var compatibilityChecks = []string{
	"models", "model", "chat", "system", "multi-turn", "usage", "stream",
	"stream-usage", "tools", "tool-result", "json", "structured", "responses", "embeddings", "invalid-request",
}

func discoverCompatibilityTargets(cfg Config) ([]Target, error) {
	base, err := url.Parse(cfg.BaseURL)
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") {
		return nil, fmt.Errorf("-base-url must be an absolute HTTP(S) API root, including any /v1 prefix")
	}
	if base.RawQuery != "" || base.Fragment != "" || base.User != nil {
		return nil, fmt.Errorf("-base-url cannot contain credentials, query parameters, or fragments; use -H, -query, or -api-key-env")
	}
	selected := map[string]bool{}
	if cfg.Checks == "" || cfg.Checks == "all" {
		for _, name := range compatibilityChecks {
			selected[name] = true
		}
	} else {
		for _, name := range strings.Split(cfg.Checks, ",") {
			name = strings.TrimSpace(name)
			known := false
			for _, candidate := range compatibilityChecks {
				if name == candidate {
					known = true
				}
			}
			if !known {
				return nil, fmt.Errorf("unknown check %q; available: %s", name, strings.Join(compatibilityChecks, ","))
			}
			selected[name] = true
		}
	}
	var targets []Target
	for _, name := range compatibilityChecks {
		path, method := "/chat/completions", http.MethodPost
		messages := []map[string]any{{"role": "user", "content": "Reply with a short greeting."}}
		payload := map[string]any{"model": cfg.Model, "messages": messages, cfg.TokenLimitField: cfg.MaxTokens}
		target := Target{ID: "openai:" + name, Check: name, Source: "openai", RequestedModel: cfg.Model, ExpectedStatuses: []string{"200"}}
		if !selected[name] {
			target.SkipReason = "not selected"
		}
		switch name {
		case "models":
			path, method, payload = "/models", http.MethodGet, nil
		case "model":
			path, method, payload = "/models/"+url.PathEscape(cfg.Model), http.MethodGet, nil
		case "system":
			payload["messages"] = []map[string]any{{"role": "system", "content": "Reply to every message with exactly COMPAT_OK and nothing else."}, {"role": "user", "content": "Please respond."}}
		case "multi-turn":
			payload["messages"] = []map[string]any{{"role": "user", "content": "Remember this word: violet."}, {"role": "assistant", "content": "I will remember it."}, {"role": "user", "content": "What word did I ask you to remember? Reply with only that word."}}
		case "stream", "stream-usage":
			payload["stream"] = true
			if name == "stream-usage" {
				payload["stream_options"] = map[string]any{"include_usage": true}
			}
		case "tools":
			payload["messages"] = []map[string]any{{"role": "user", "content": "Use the add function to add 19 and 23."}}
			payload["tools"] = []any{map[string]any{"type": "function", "function": map[string]any{"name": "add", "description": "Add two integers.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "integer"}, "b": map[string]any{"type": "integer"}}, "required": []string{"a", "b"}, "additionalProperties": false}}}}
			payload["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": "add"}}
		case "tool-result":
			payload["messages"] = []map[string]any{{"role": "user", "content": "Use the tool result to answer. Reply only with the resulting number."}, {"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": "call_compat_add", "type": "function", "function": map[string]any{"name": "add", "arguments": `{"a":19,"b":23}`}}}}, {"role": "tool", "tool_call_id": "call_compat_add", "content": "42"}}
		case "json", "structured":
			payload["messages"] = []map[string]any{{"role": "user", "content": "Return a JSON object with exactly one property, answer, whose value is the integer 42. No other text."}}
			payload["response_format"] = map[string]any{"type": "json_object"}
			if name == "structured" {
				payload["response_format"] = map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "compat_answer", "strict": true, "schema": map[string]any{"type": "object", "properties": map[string]any{"answer": map[string]any{"type": "integer", "enum": []int{42}}}, "required": []string{"answer"}, "additionalProperties": false}}}
			}
		case "responses":
			path = "/responses"
			payload = map[string]any{"model": cfg.Model, "input": "Reply with a short greeting.", "max_output_tokens": cfg.MaxTokens, "store": false}
		case "invalid-request":
			payload = map[string]any{"model": cfg.Model}
			target.ExpectedStatuses = []string{"400"}
		case "embeddings":
			path = "/embeddings"
			if cfg.EmbeddingModel == "" {
				target.SkipReason = "requires -embedding-model"
			}
			target.RequestedModel = cfg.EmbeddingModel
			payload = map[string]any{"model": cfg.EmbeddingModel, "input": []string{"A short sentence.", "A different sentence."}, "encoding_format": "float"}
		}
		target.URL, target.Path, target.Method = strings.TrimRight(base.String(), "/")+path, path, method
		media := "application/json"
		if name == "stream" || name == "stream-usage" {
			media = "text/event-stream"
		}
		target.ExpectedContentTypesByCode = map[string][]string{"200": {media}}
		if name == "invalid-request" {
			target.ExpectedContentTypesByCode = map[string][]string{"400": {"application/json"}}
		}
		if payload != nil {
			target.RequestBody, err = json.Marshal(payload)
			if err != nil {
				return nil, err
			}
			target.RequestBodyBytes = len(target.RequestBody)
			target.RequestContentType = "application/json"
		}
		targets = append(targets, target)
	}
	return targets, nil
}

func compatibilityVariance(kind, message string) Variance {
	return Variance{Type: kind, Severity: "error", Message: message}
}

func evaluateCompatibility(target Target, resp *http.Response, body []byte, result *Result) {
	if target.Check == "invalid-request" && (resp.StatusCode == 400 || resp.StatusCode == 200) {
		result.Outcome = "failed"
		if resp.StatusCode == 200 {
			result.Variances = append(result.Variances, compatibilityVariance("invalid_request_accepted", "chat request missing the required messages field was accepted"))
			return
		}
		root, err := decodeObject(body)
		errorObject := asMap(root["error"])
		if err != nil || textValue(errorObject["message"]) == "" || textValue(errorObject["type"]) == "" {
			result.Variances = append(result.Variances, compatibilityVariance("error_shape", "HTTP 400 must contain an error object with string message and type"))
		}
		for _, field := range []string{"param", "code"} {
			if value := errorObject[field]; value != nil {
				if _, ok := value.(string); !ok {
					result.Variances = append(result.Variances, compatibilityVariance("error_shape", "error param and code must be strings or null"))
				}
			}
		}
		if len(result.Variances) == 0 {
			result.Outcome = "passed"
		}
		return
	}
	if resp.StatusCode != http.StatusOK {
		result.Outcome = "failed"
		message := "unexpected HTTP status for this request"
		switch {
		case resp.StatusCode == 401 || resp.StatusCode == 403:
			result.Outcome, message = "blocked", "authentication or authorization prevented this check"
		case resp.StatusCode == 429:
			result.Outcome, message = "blocked", "rate limit or quota prevented this check"
		case resp.StatusCode == 404 || resp.StatusCode == 405 || resp.StatusCode == 501:
			result.Outcome, message = "unavailable", "route or model was unavailable; this does not establish model capability"
		case resp.StatusCode == 400 || resp.StatusCode == 422:
			result.Outcome, message = "rejected", "request was rejected; inspect the provider error and token-limit-field setting"
		case resp.StatusCode >= 500:
			result.Outcome, message = "inconclusive", "provider failure prevented this check"
		}
		result.Variances = []Variance{{Type: "compatibility_" + result.Outcome, Severity: "warning", Message: message, Actual: fmt.Sprint(resp.StatusCode)}}
		var envelope struct {
			Error struct {
				Message string `json:"message"`
				Type    string `json:"type"`
				Code    any    `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal(body, &envelope) == nil && envelope.Error.Message != "" {
			result.Variances[0].Actual += " " + shorten(envelope.Error.Type+": "+envelope.Error.Message, 500)
		}
		return
	}
	var issues []Variance
	if target.Check == "stream" || target.Check == "stream-usage" {
		issues = validateChatStream(body, target.Check == "stream-usage")
	} else {
		root, err := decodeObject(body)
		if err != nil {
			issues = []Variance{compatibilityVariance("invalid_json", "response must be one complete JSON object")}
		} else if root["error"] != nil {
			issues = []Variance{compatibilityVariance("error_in_success", "HTTP 200 contained an API error object")}
		} else {
			result.ReportedModel = textValue(root["model"])
			if target.Check == "model" {
				result.ReportedModel = textValue(root["id"])
			}
			switch target.Check {
			case "models":
				issues = validateModels(root)
			case "model":
				issues = validateModel(root)
			case "embeddings":
				issues = validateEmbeddings(root)
			case "responses":
				issues = validateResponseObject(root)
			default:
				issues = validateChat(root, target.Check)
			}
		}
	}
	result.Variances = append(result.Variances, issues...)
	result.Outcome = "passed"
	if len(result.Variances) > 0 {
		result.Outcome = "inconclusive"
		for _, issue := range result.Variances {
			if issue.Type != "generation_incomplete" {
				result.Outcome = "failed"
				break
			}
		}
	}
}

func decodeObject(body []byte) (map[string]any, error) {
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, err
	}
	if root == nil {
		return nil, fmt.Errorf("expected object")
	}
	return root, nil
}

func textValue(value any) string { text, _ := value.(string); return text }
func nonnegativeInteger(value any) bool {
	n, ok := value.(float64)
	return ok && !math.IsNaN(n) && !math.IsInf(n, 0) && n >= 0 && math.Trunc(n) == n
}

func validateModel(root map[string]any) []Variance {
	if textValue(root["id"]) == "" || root["object"] != "model" || !nonnegativeInteger(root["created"]) || textValue(root["owned_by"]) == "" {
		return []Variance{compatibilityVariance("model_shape", "model requires id, object=model, integer created, and owned_by")}
	}
	return nil
}

func validateModels(root map[string]any) []Variance {
	data, ok := root["data"].([]any)
	if root["object"] != "list" || !ok {
		return []Variance{compatibilityVariance("models_shape", "model list requires object=list and a data array")}
	}
	var issues []Variance
	seen := map[string]bool{}
	for _, item := range data {
		model := asMap(item)
		issues = append(issues, validateModel(model)...)
		id := textValue(model["id"])
		if seen[id] {
			issues = append(issues, compatibilityVariance("models_shape", "model list contains duplicate IDs"))
			break
		}
		seen[id] = true
		if len(issues) >= 10 {
			break
		}
	}
	return issues
}

func validateUsage(raw any, embeddings bool) []Variance {
	usage := asMap(raw)
	prompt, total := usage["prompt_tokens"], usage["total_tokens"]
	if !nonnegativeInteger(prompt) || !nonnegativeInteger(total) {
		return []Variance{compatibilityVariance("usage_shape", "usage requires nonnegative integer prompt_tokens and total_tokens")}
	}
	completion := float64(0)
	if !embeddings {
		if !nonnegativeInteger(usage["completion_tokens"]) {
			return []Variance{compatibilityVariance("usage_shape", "usage requires nonnegative integer completion_tokens")}
		}
		completion = usage["completion_tokens"].(float64)
	}
	if prompt.(float64)+completion != total.(float64) {
		return []Variance{compatibilityVariance("usage_total", "total_tokens must equal prompt_tokens plus completion_tokens")}
	}
	return nil
}

func validateChat(root map[string]any, check string) []Variance {
	var issues []Variance
	if textValue(root["id"]) == "" || root["object"] != "chat.completion" || textValue(root["model"]) == "" || !nonnegativeInteger(root["created"]) {
		issues = append(issues, compatibilityVariance("chat_shape", "chat response requires id, object=chat.completion, model, and integer created"))
	}
	choices, ok := root["choices"].([]any)
	if !ok || len(choices) != 1 {
		return append(issues, compatibilityVariance("choices_shape", "one choice is required for the default n=1 request"))
	}
	choice := asMap(choices[0])
	message := asMap(choice["message"])
	if choice["index"] != float64(0) || message["role"] != "assistant" {
		issues = append(issues, compatibilityVariance("message_shape", "choice must have index=0 and an assistant message"))
	}
	if root["usage"] != nil || check == "usage" {
		issues = append(issues, validateUsage(root["usage"], false)...)
	}
	finish := textValue(choice["finish_reason"])
	if finish == "length" || finish == "content_filter" || textValue(message["refusal"]) != "" {
		return append(issues, compatibilityVariance("generation_incomplete", "generation was truncated or refused; increase the token budget or inspect model restrictions"))
	}
	if check == "tools" {
		if finish != "tool_calls" {
			issues = append(issues, compatibilityVariance("tool_finish_reason", "forced tool request must finish with tool_calls"))
		}
		calls, ok := message["tool_calls"].([]any)
		if !ok || len(calls) != 1 {
			return append(issues, compatibilityVariance("tool_call_missing", "forced add function must produce exactly one tool call"))
		}
		call := asMap(calls[0])
		function := asMap(call["function"])
		args, err := decodeObject([]byte(textValue(function["arguments"])))
		if call["type"] != "function" || textValue(call["id"]) == "" || function["name"] != "add" || err != nil || len(args) != 2 || args["a"] != float64(19) || args["b"] != float64(23) {
			issues = append(issues, compatibilityVariance("tool_call_invalid", "expected a function call with ID, name=add, and JSON arguments a=19, b=23"))
		}
		return issues
	}
	if finish != "stop" {
		issues = append(issues, compatibilityVariance("finish_reason", "text request must finish with stop"))
	}
	content := strings.TrimSpace(textValue(message["content"]))
	if content == "" {
		return append(issues, compatibilityVariance("empty_content", "text request must return nonempty string content"))
	}
	expected := map[string]string{"system": "COMPAT_OK", "multi-turn": "violet", "tool-result": "42"}[check]
	if expected != "" && content != expected {
		issues = append(issues, Variance{Type: "behavior_mismatch", Severity: "warning", Message: "response did not follow the check's exact-output instruction; this measures observed behavior as well as API support", Expected: expected})
	}
	if check == "json" || check == "structured" {
		object, err := decodeObject([]byte(content))
		if err != nil {
			issues = append(issues, compatibilityVariance("json_output_invalid", "message content is not a JSON object"))
		} else if len(object) != 1 || object["answer"] != float64(42) {
			issues = append(issues, compatibilityVariance("structured_output_mismatch", "expected exactly one integer property answer=42"))
		}
	}
	return issues
}

func validateEmbeddings(root map[string]any) []Variance {
	data, ok := root["data"].([]any)
	if root["object"] != "list" || textValue(root["model"]) == "" || !ok || len(data) != 2 {
		return []Variance{compatibilityVariance("embeddings_shape", "batch of two inputs requires object=list, model, and two embedding records")}
	}
	issues := validateUsage(root["usage"], true)
	seen := map[float64]bool{}
	dimensions := 0
	for _, item := range data {
		embedding := asMap(item)
		index, validIndex := embedding["index"].(float64)
		vector, validVector := embedding["embedding"].([]any)
		if embedding["object"] != "embedding" || !validIndex || (index != 0 && index != 1) || seen[index] || !validVector || len(vector) == 0 {
			return append(issues, compatibilityVariance("embedding_shape", "embedding requires a unique input index, object=embedding, and nonempty numeric vector"))
		}
		seen[index] = true
		if dimensions != 0 && len(vector) != dimensions {
			issues = append(issues, compatibilityVariance("embedding_dimensions", "all vectors in a batch must have equal dimensions"))
		}
		dimensions = len(vector)
		for _, value := range vector {
			if _, ok := value.(float64); !ok {
				return append(issues, compatibilityVariance("embedding_value", "embedding vector contains a nonnumeric value"))
			}
		}
	}
	return issues
}

func validateResponseObject(root map[string]any) []Variance {
	var issues []Variance
	created, validCreated := root["created_at"].(float64)
	if root["object"] != "response" || textValue(root["id"]) == "" || textValue(root["model"]) == "" || !validCreated || created < 0 {
		issues = append(issues, compatibilityVariance("responses_shape", "Responses result requires object=response, id, model, and numeric created_at"))
	}
	if root["status"] == "incomplete" {
		return append(issues, compatibilityVariance("generation_incomplete", "Responses generation was incomplete; inspect the token budget"))
	}
	if root["status"] != "completed" {
		issues = append(issues, compatibilityVariance("responses_status", "non-streaming Responses request must complete"))
	}
	output, ok := root["output"].([]any)
	found := false
	for _, raw := range output {
		item := asMap(raw)
		if item["type"] != "message" {
			continue
		}
		if item["role"] != "assistant" || textValue(item["id"]) == "" || item["status"] != "completed" {
			issues = append(issues, compatibilityVariance("responses_message", "output message requires id, role=assistant, and status=completed"))
		}
		for _, rawContent := range asSlice(item["content"]) {
			content := asMap(rawContent)
			if content["type"] == "refusal" {
				return append(issues, compatibilityVariance("generation_incomplete", "Responses request was refused"))
			}
			if content["type"] == "output_text" && strings.TrimSpace(textValue(content["text"])) != "" {
				found = true
			}
		}
	}
	if !ok || !found {
		issues = append(issues, compatibilityVariance("responses_output", "Responses output must contain a nonempty assistant output_text item"))
	}
	if root["usage"] != nil {
		usage := asMap(root["usage"])
		if !nonnegativeInteger(usage["input_tokens"]) || !nonnegativeInteger(usage["output_tokens"]) || !nonnegativeInteger(usage["total_tokens"]) {
			issues = append(issues, compatibilityVariance("usage_shape", "Responses usage requires integer input_tokens, output_tokens, and total_tokens"))
		} else if usage["input_tokens"].(float64)+usage["output_tokens"].(float64) != usage["total_tokens"].(float64) {
			issues = append(issues, compatibilityVariance("usage_total", "Responses token usage does not add up"))
		}
	}
	return issues
}

func redactResult(result *Result, cfg Config) {
	var secrets []string
	if cfg.APIToken != "" {
		secrets = append(secrets, cfg.APIToken)
	}
	for _, values := range cfg.Headers {
		secrets = append(secrets, values...)
	}
	for _, value := range cfg.AuthCredentials {
		secrets = append(secrets, value)
	}
	for _, value := range cfg.QueryParams {
		secrets = append(secrets, value)
	}
	redact := func(value string) string {
		for _, secret := range secrets {
			if secret == "" {
				continue
			}
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
			value = strings.ReplaceAll(value, url.QueryEscape(secret), "[REDACTED]")
		}
		return value
	}
	result.Error = redact(result.Error)
	result.Status = redact(result.Status)
	result.ContentType = redact(result.ContentType)
	result.ReportedModel = redact(result.ReportedModel)
	for i := range result.Variances {
		result.Variances[i].Message = redact(result.Variances[i].Message)
		result.Variances[i].Actual = redact(result.Variances[i].Actual)
		result.Variances[i].Expected = redact(result.Variances[i].Expected)
	}
}

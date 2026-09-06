package main

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"strings"
	"time"
)

// SSE events end at a blank line, not at each data line. Comments and CRLF are valid.
type sseEventReader struct{ data []string }

func (s *sseEventReader) line(line string) (string, bool) {
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	if line == "" {
		if len(s.data) == 0 {
			return "", false
		}
		data := strings.Join(s.data, "\n")
		s.data = nil
		return data, true
	}
	field, value, _ := strings.Cut(line, ":")
	if field == "data" {
		s.data = append(s.data, strings.TrimPrefix(value, " "))
	}
	return "", false
}

func readCompatibilityBody(resp *http.Response, limit int64, startedAt time.Time, result *Result) ([]byte, error) {
	reader := io.LimitReader(resp.Body, limit+1)
	if mediaTypeBase(resp.Header.Get("Content-Type")) != "text/event-stream" {
		return io.ReadAll(reader)
	}
	buffered := bufio.NewReader(reader)
	var body bytes.Buffer
	var events sseEventReader
	for {
		line, err := buffered.ReadString('\n')
		body.WriteString(line)
		data, ready := events.line(line)
		if ready && data == "[DONE]" {
			return body.Bytes(), nil
		}
		if ready && result.FirstTokenMillis == 0 {
			if chunk, parseErr := decodeObject([]byte(data)); parseErr == nil {
				result.ReportedModel = textValue(chunk["model"])
				for _, raw := range asSlice(chunk["choices"]) {
					if textValue(asMap(asMap(raw)["delta"])["content"]) != "" {
						result.FirstTokenMillis = millisSince(startedAt)
					}
				}
			}
		}
		if err == io.EOF {
			return body.Bytes(), nil
		}
		if err != nil {
			return body.Bytes(), err
		}
	}
}

func validateChatStream(body []byte, requireUsage bool) []Variance {
	var issues []Variance
	var events sseEventReader
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 4096), len(body)+1)
	var id, model string
	var created any
	var done, finished, content, role, usageSeen bool
	chunks := 0
	for scanner.Scan() {
		data, ready := events.line(scanner.Text())
		if !ready {
			continue
		}
		if done {
			return []Variance{compatibilityVariance("stream_order", "received data after the [DONE] event")}
		}
		if data == "[DONE]" {
			done = true
			continue
		}
		chunk, err := decodeObject([]byte(data))
		if err != nil || chunk["error"] != nil {
			return []Variance{compatibilityVariance("stream_json", "SSE data must contain a JSON completion chunk, not an error")}
		}
		chunks++
		if chunk["object"] != "chat.completion.chunk" || textValue(chunk["id"]) == "" || textValue(chunk["model"]) == "" || !nonnegativeInteger(chunk["created"]) {
			return []Variance{compatibilityVariance("stream_shape", "chunk requires id, object=chat.completion.chunk, model, and integer created")}
		}
		if id == "" {
			id, model, created = textValue(chunk["id"]), textValue(chunk["model"]), chunk["created"]
		}
		if chunk["id"] != id || chunk["model"] != model || chunk["created"] != created {
			return []Variance{compatibilityVariance("stream_identity", "completion id, model, or created changed during the stream")}
		}
		choices, ok := chunk["choices"].([]any)
		if !ok {
			return []Variance{compatibilityVariance("stream_choices", "stream chunk requires a choices array")}
		}
		if chunk["usage"] != nil {
			issues = append(issues, validateUsage(chunk["usage"], false)...)
			if requireUsage && len(choices) == 0 && finished {
				usageSeen = true
			}
		}
		if len(choices) == 0 {
			if chunk["usage"] == nil {
				return []Variance{compatibilityVariance("stream_choices", "empty choices are only valid for a usage chunk")}
			}
			continue
		}
		if len(choices) != 1 || finished {
			return []Variance{compatibilityVariance("stream_order", "unexpected choice count or choice after finish_reason")}
		}
		choice := asMap(choices[0])
		delta, ok := choice["delta"].(map[string]any)
		if choice["index"] != float64(0) || !ok {
			return []Variance{compatibilityVariance("stream_delta", "stream choice requires index=0 and a delta object")}
		}
		if rawRole, exists := delta["role"]; exists {
			if rawRole != "assistant" {
				return []Variance{compatibilityVariance("stream_role", "stream role must be assistant")}
			}
			role = true
		}
		if rawContent := delta["content"]; rawContent != nil {
			text, ok := rawContent.(string)
			if !ok {
				return []Variance{compatibilityVariance("stream_content", "delta content must be a string or null")}
			}
			content = content || strings.TrimSpace(text) != ""
		}
		if textValue(delta["refusal"]) != "" {
			issues = append(issues, compatibilityVariance("generation_incomplete", "streamed request was refused"))
		}
		if reason := choice["finish_reason"]; reason != nil {
			finished = true
			switch reason {
			case "stop":
			case "length", "content_filter":
				issues = append(issues, compatibilityVariance("generation_incomplete", "stream generation was truncated or filtered"))
			default:
				return []Variance{compatibilityVariance("stream_finish_reason", "unexpected finish_reason for a text stream")}
			}
		}
	}
	if scanner.Err() != nil || len(events.data) != 0 {
		issues = append(issues, compatibilityVariance("stream_framing", "stream ended inside an SSE event"))
	}
	if !done || !finished || chunks == 0 {
		issues = append(issues, compatibilityVariance("stream_incomplete", "stream requires completion chunks, a finish_reason, and a final [DONE] event"))
	}
	if !role {
		issues = append(issues, compatibilityVariance("stream_role", "stream did not identify the assistant role"))
	}
	incomplete := false
	for _, issue := range issues {
		incomplete = incomplete || issue.Type == "generation_incomplete"
	}
	if !content && !incomplete {
		issues = append(issues, compatibilityVariance("stream_empty", "stream contained no text content"))
	}
	if requireUsage && !usageSeen {
		issues = append(issues, compatibilityVariance("stream_usage_missing", "include_usage requires a final usage chunk with empty choices before [DONE]"))
	}
	return issues
}

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func compatibilityConfig(t *testing.T, base string, extra ...string) Config {
	t.Helper()
	args := []string{"-openai", "-base-url", base, "-model", "test-chat", "-progress", "0", "-include-success-samples"}
	cfg, err := parseConfig(append(args, extra...))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func chatFixture(content string) map[string]any {
	return map[string]any{"id": "chatcmpl-test", "object": "chat.completion", "created": float64(1), "model": "test-chat",
		"choices": []any{map[string]any{"index": float64(0), "message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}},
		"usage":   map[string]any{"prompt_tokens": float64(5), "completion_tokens": float64(2), "total_tokens": float64(7)}}
}

func streamFixture(usage bool) string {
	chunks := []map[string]any{
		{"delta": map[string]any{"role": "assistant", "content": ""}, "finish_reason": nil, "index": 0},
		{"delta": map[string]any{"content": "Hello"}, "finish_reason": nil, "index": 0},
		{"delta": map[string]any{}, "finish_reason": "stop", "index": 0},
	}
	var b strings.Builder
	b.WriteString(": heartbeat\r\n\r\n")
	for _, choice := range chunks {
		chunk, _ := json.Marshal(map[string]any{"id": "chatcmpl-test", "object": "chat.completion.chunk", "created": 1, "model": "test-chat", "choices": []any{choice}})
		fmt.Fprintf(&b, "data: %s\r\n\r\n", chunk)
	}
	if usage {
		b.WriteString("data: {\"id\":\"chatcmpl-test\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"test-chat\",\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2,\"total_tokens\":7}}\r\n\r\n")
	}
	b.WriteString("data: [DONE]\r\n\r\n")
	return b.String()
}

func TestCompatibilitySuiteEndToEnd(t *testing.T) {
	t.Setenv("COMPAT_TEST_TOKEN", "secret-test-value")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-test-value" {
			t.Error("missing authorization")
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		model := map[string]any{"id": "test-chat", "object": "model", "created": 1, "owned_by": "provider"}
		switch r.URL.Path {
		case "/v1/models":
			json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []any{model}})
			return
		case "/v1/models/test-chat":
			json.NewEncoder(w).Encode(model)
			return
		case "/v1/embeddings":
			var request map[string]any
			json.NewDecoder(r.Body).Decode(&request)
			if request["model"] != "test-embed" || len(asSlice(request["input"])) != 2 {
				t.Error("invalid embedding request")
			}
			json.NewEncoder(w).Encode(map[string]any{"object": "list", "model": "test-embed", "data": []any{map[string]any{"object": "embedding", "index": 0, "embedding": []float64{0.1, 0.2}}, map[string]any{"object": "embedding", "index": 1, "embedding": []float64{0.3, 0.4}}}, "usage": map[string]any{"prompt_tokens": 2, "total_tokens": 2}})
			return
		case "/v1/responses":
			json.NewEncoder(w).Encode(map[string]any{"object": "response", "id": "resp-test", "model": "test-chat", "created_at": 1, "status": "completed", "output": []any{map[string]any{"id": "msg-test", "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "Hi"}}}}})
			return
		case "/v1/chat/completions":
		default:
			t.Errorf("unexpected route: %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if request["messages"] == nil {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"message":"messages is required","type":"invalid_request_error","param":"messages","code":null}}`)
			return
		}
		if request["max_completion_tokens"] != float64(256) {
			t.Error("missing token budget")
		}
		if request["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, streamFixture(request["stream_options"] != nil))
			return
		}
		response := chatFixture("Hello")
		choice := asMap(asSlice(response["choices"])[0])
		message := asMap(choice["message"])
		messages := asSlice(request["messages"])
		if asMap(messages[0])["role"] == "system" {
			message["content"] = "COMPAT_OK"
		}
		if len(messages) == 3 {
			message["content"] = "violet"
		}
		if asMap(messages[len(messages)-1])["role"] == "tool" {
			message["content"] = "42"
		}
		if request["response_format"] != nil {
			message["content"] = `{"answer":42}`
		}
		if request["tools"] != nil {
			if request["tool_choice"] == nil {
				t.Error("tool choice was not forced")
			}
			message["content"] = nil
			message["tool_calls"] = []any{map[string]any{"id": "call-test", "type": "function", "function": map[string]any{"name": "add", "arguments": `{"a":19,"b":23}`}}}
			choice["finish_reason"] = "tool_calls"
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()
	cfg := compatibilityConfig(t, server.URL+"/v1", "-embedding-model", "test-embed", "-api-key-env", "COMPAT_TEST_TOKEN", "-iterations", "2", "-concurrency", "3")
	targets, err := discoverTargets(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	report, err := runTargets(context.Background(), cfg, targets)
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.TotalRequests != 2*len(compatibilityChecks) || report.Summary.RequestsWithVariances != 0 {
		t.Fatalf("unexpected report: %+v samples=%+v", report.Summary, report.Samples)
	}
	for _, summary := range report.TargetsSummary {
		if summary.OutcomeCounts["passed"] != 2 {
			t.Errorf("%s: %+v", summary.Check, summary.OutcomeCounts)
		}
	}
	for _, sample := range report.Samples {
		if strings.Contains(sample.TargetID, "stream") && sample.FirstTokenMillis <= 0 {
			t.Errorf("missing first token timing: %+v", sample)
		}
	}
	encoded, _ := json.Marshal(report)
	if strings.Contains(string(encoded), "secret-test-value") {
		t.Fatal("report leaked API token")
	}
	if !strings.Contains(markdownReport(report), "OpenAI API Compatibility") {
		t.Fatal("missing compatibility report")
	}
}

func TestCompatibilityFailures(t *testing.T) {
	tests := []struct {
		name, check, body, contentType, outcome, variance string
		status                                            int
	}{
		{"html200", "chat", "<html>OK</html>", "text/html", "failed", "invalid_json", 200},
		{"empty", "chat", `{}`, "application/json", "failed", "chat_shape", 200},
		{"error200", "chat", `{"error":{"message":"bad"}}`, "application/json", "failed", "error_in_success", 200},
		{"trailing-json", "chat", `{} {}`, "application/json", "failed", "invalid_json", 200},
		{"auth", "chat", `{"error":{"message":"bad key","type":"auth"}}`, "application/json", "blocked", "compatibility_blocked", 401},
		{"quota", "chat", `{}`, "application/json", "blocked", "compatibility_blocked", 429},
		{"missing", "responses", `{}`, "application/json", "unavailable", "compatibility_unavailable", 404},
		{"reject", "structured", `{}`, "application/json", "rejected", "compatibility_rejected", 400},
		{"provider", "chat", `{}`, "application/json", "inconclusive", "compatibility_inconclusive", 503},
		{"bad-stream", "stream", "data: [DONE]\n\n", "text/event-stream", "failed", "stream_incomplete", 200},
		{"missing-usage", "stream-usage", streamFixture(false), "text/event-stream", "failed", "stream_usage_missing", 200},
		{"bad-negative", "invalid-request", `{}`, "application/json", "failed", "error_shape", 400},
		{"accepted-negative", "invalid-request", `{}`, "application/json", "failed", "invalid_request_accepted", 200},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", test.contentType)
				w.WriteHeader(test.status)
				io.WriteString(w, test.body)
			}))
			defer server.Close()
			cfg := compatibilityConfig(t, server.URL, "-checks", test.check, "-max-samples", "0")
			targets, err := discoverTargets(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			report, err := runTargets(context.Background(), cfg, targets)
			if err != nil {
				t.Fatal(err)
			}
			for _, summary := range report.TargetsSummary {
				if summary.Check != test.check {
					continue
				}
				if summary.OutcomeCounts[test.outcome] != 1 || summary.FirstVariance == nil {
					t.Fatalf("unexpected summary %+v", summary)
				}
				found := false
				for _, v := range summary.FirstVariance.Variances {
					found = found || v.Type == test.variance
				}
				if !found {
					t.Fatalf("missing %s: %+v", test.variance, summary.FirstVariance)
				}
			}
		})
	}
}

func TestChatBehaviorValidation(t *testing.T) {
	for _, test := range []struct {
		name, check string
		change      func(map[string]any)
	}{
		{"usage", "usage", func(root map[string]any) { asMap(root["usage"])["total_tokens"] = float64(999) }},
		{"tools", "tools", func(root map[string]any) {}},
		{"structured", "structured", func(root map[string]any) {
			asMap(asMap(asSlice(root["choices"])[0])["message"])["content"] = `{"answer":"42","extra":true}`
		}},
		{"role", "chat", func(root map[string]any) { asMap(asMap(asSlice(root["choices"])[0])["message"])["role"] = "user" }},
		{"index", "chat", func(root map[string]any) { asMap(asSlice(root["choices"])[0])["index"] = float64(1) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := chatFixture("Hello")
			test.change(root)
			if len(validateChat(root, test.check)) == 0 {
				t.Fatal("broken response passed")
			}
		})
	}
	root := chatFixture("")
	asMap(asSlice(root["choices"])[0])["finish_reason"] = "length"
	body, _ := json.Marshal(root)
	result := Result{}
	evaluateCompatibility(Target{Check: "chat"}, &http.Response{StatusCode: 200}, body, &result)
	if result.Outcome != "inconclusive" {
		t.Fatalf("truncation misclassified: %+v", result)
	}
}

func TestStreamValidation(t *testing.T) {
	valid := streamFixture(true)
	for _, test := range []struct{ name, body string }{
		{"missing-done", strings.ReplaceAll(valid, "data: [DONE]\r\n\r\n", "")},
		{"truncated-event", strings.TrimSuffix(valid, "\r\n")},
		{"identity", strings.Replace(valid, `"id":"chatcmpl-test"`, `"id":"other"`, 1)},
		{"after-done", valid + "data: {}\n\n"},
		{"non-json", "data: not-json\n\ndata: [DONE]\n\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if len(validateChatStream([]byte(test.body), true)) == 0 {
				t.Fatal("broken stream passed")
			}
		})
	}
	if issues := validateChatStream([]byte(valid), true); len(issues) != 0 {
		t.Fatalf("valid CRLF stream rejected: %+v", issues)
	}
	multiline := strings.Replace(valid, `"object":"chat.completion.chunk",`, "\"object\":\"chat.completion.chunk\",\r\ndata: ", 1)
	if issues := validateChatStream([]byte(multiline), true); len(issues) != 0 {
		t.Fatalf("valid multiline SSE rejected: %+v", issues)
	}
}

func TestStreamDoneDoesNotWaitForConnectionClose(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, streamFixture(false))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	cfg := compatibilityConfig(t, server.URL, "-checks", "stream", "-timeout", "500ms")
	targets, err := discoverTargets(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	report, err := runTargets(context.Background(), cfg, targets)
	if err != nil || report.Summary.RequestsWithVariances != 0 {
		t.Fatalf("completed stream failed: %+v %v", report, err)
	}
}

func TestRunnerReadFailureAndDeadline(t *testing.T) {
	t.Run("truncated-body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "100")
			io.WriteString(w, "short")
		}))
		defer server.Close()
		cfg, _ := parseConfig([]string{"-url", server.URL, "-methods", "GET", "-progress", "0"})
		report, err := runTargets(context.Background(), cfg, discoverSingleURLTargets(cfg))
		if err != nil {
			t.Fatal(err)
		}
		if report.Summary.VarianceCounts["response_read_error"] != 1 {
			t.Fatalf("read error lost: %+v", report.Summary)
		}
	})
	t.Run("duration-during-rate-wait", func(t *testing.T) {
		cfg := compatibilityConfig(t, "http://127.0.0.1:1/v1", "-checks", "chat", "-duration", "30ms", "-rate", "0.01")
		targets, _ := discoverTargets(context.Background(), cfg)
		started := time.Now()
		report, err := runTargets(context.Background(), cfg, targets)
		if err != nil || time.Since(started) > time.Second || report.Summary.TotalRequests != 0 || report.Summary.UntestedTargets != 1 {
			t.Fatalf("duration did not stop rate wait: %+v %v", report, err)
		}
	})
	t.Run("duration-during-body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		}))
		defer server.Close()
		cfg, _ := parseConfig([]string{"-url", server.URL, "-methods", "GET", "-duration", "80ms", "-timeout", "5s", "-progress", "0", "-concurrency", "1"})
		started := time.Now()
		report, err := runTargets(context.Background(), cfg, discoverSingleURLTargets(cfg))
		if err != nil || time.Since(started) > time.Second || report.Summary.VarianceCounts["response_read_error"] != 1 {
			t.Fatalf("duration did not cancel body: %+v %v", report, err)
		}
	})
	t.Run("body-latency", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			time.Sleep(40 * time.Millisecond)
			io.WriteString(w, "ok")
		}))
		defer server.Close()
		cfg, _ := parseConfig([]string{"-url", server.URL, "-methods", "GET", "-include-success-samples", "-progress", "0"})
		report, _ := runTargets(context.Background(), cfg, discoverSingleURLTargets(cfg))
		if report.Samples[0].DurationMillis < 35 {
			t.Fatal("latency omitted response body")
		}
	})
}

func TestCompatibilityLimitRedirectAndSkipped(t *testing.T) {
	var redirected atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/destination" {
			redirected.Add(1)
			return
		}
		http.Redirect(w, r, "/destination", 302)
	}))
	defer server.Close()
	cfg := compatibilityConfig(t, server.URL, "-checks", "chat")
	targets, _ := discoverTargets(context.Background(), cfg)
	report, _ := runTargets(context.Background(), cfg, targets)
	if redirected.Load() != 0 || report.Summary.StatusCounts["302"] != 1 {
		t.Fatal("redirect hid actual endpoint status")
	}
	for _, summary := range report.TargetsSummary {
		if summary.Check == "embeddings" && summary.Requests != 0 {
			t.Fatal("embeddings ran without model")
		}
	}
	oversized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, strings.Repeat("x", 1000)) }))
	defer oversized.Close()
	cfg = compatibilityConfig(t, oversized.URL, "-checks", "chat", "-max-response-bytes", "100")
	targets, _ = discoverTargets(context.Background(), cfg)
	report, _ = runTargets(context.Background(), cfg, targets)
	if report.Summary.VarianceCounts["response_too_large"] != 1 || report.Samples[0].ContentLength != 101 {
		t.Fatalf("body cap failed: %+v", report)
	}
}

func TestCompatibilityConfigAndSecrets(t *testing.T) {
	for _, args := range [][]string{
		{"-openai"}, {"-openai", "-base-url", "https://example.test/v1", "-model", "m", "-body", "{}"},
		{"-url", "http://example.test", "-rate", "NaN"}, {"-url", "http://example.test", "-duration", "-1s"},
		{"-url", "http://example.test", "-report-format", "bogus"}, {"-url", "http://example.test", "-max-response-bytes", "0"},
	} {
		if _, err := parseConfig(args); err == nil {
			t.Errorf("accepted invalid config: %v", args)
		}
	}
	cfg := compatibilityConfig(t, "https://example.test/v1", "-checks", "unknown")
	if _, err := discoverTargets(context.Background(), cfg); err == nil {
		t.Fatal("accepted unknown check")
	}
	cfg = compatibilityConfig(t, "https://example.test/v1?api_key=secret")
	if _, err := discoverTargets(context.Background(), cfg); err == nil {
		t.Fatal("accepted credential-bearing base URL")
	}
	t.Setenv("COMPAT_TEST_TOKEN", "a-secret-token")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		io.WriteString(w, `{"error":{"message":"invalid a-secret-token","type":"auth"}}`)
	}))
	defer server.Close()
	cfg = compatibilityConfig(t, server.URL, "-checks", "chat", "-api-key-env", "COMPAT_TEST_TOKEN")
	targets, _ := discoverTargets(context.Background(), cfg)
	report, _ := runTargets(context.Background(), cfg, targets)
	body, _ := json.Marshal(report)
	if strings.Contains(string(body), "a-secret-token") || !strings.Contains(string(body), "[REDACTED]") {
		t.Fatal("provider error secret redaction failed")
	}
}

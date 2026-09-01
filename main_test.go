package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseMethodsAll(t *testing.T) {
	methods, err := parseMethods("all")
	if err != nil {
		t.Fatalf("parseMethods: %v", err)
	}
	if len(methods) != len(standardMethods) {
		t.Fatalf("got %d methods, want %d", len(methods), len(standardMethods))
	}
}

func TestStatusAllowedPatterns(t *testing.T) {
	tests := []struct {
		status  int
		pattern string
		want    bool
	}{
		{200, "200", true},
		{201, "2XX", true},
		{404, "2XX", false},
		{204, "200-299", true},
		{500, "default", true},
	}
	for _, test := range tests {
		patterns, err := parseStatusPatterns(test.pattern)
		if err != nil {
			t.Fatalf("parseStatusPatterns(%q): %v", test.pattern, err)
		}
		if got := statusAllowed(test.status, patterns); got != test.want {
			t.Fatalf("statusAllowed(%d, %q) = %v, want %v", test.status, test.pattern, got, test.want)
		}
	}
}

func TestDiscoverOpenAPITargets(t *testing.T) {
	spec := `openapi: 3.0.3
servers:
  - url: https://api.example.test/v1
paths:
  /widgets/{id}:
    parameters:
      - name: id
        in: path
        required: true
        schema:
          type: integer
    get:
      operationId: getWidget
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
    patch:
      operationId: updateWidget
      parameters:
        - name: verbose
          in: query
          required: true
          schema:
            type: boolean
      requestBody:
        content:
          application/json:
            schema:
              type: object
              required: [name]
              properties:
                name:
                  type: string
      responses:
        "204":
          description: updated
`
	path := filepath.Join(t.TempDir(), "openapi.yaml")
	if err := os.WriteFile(path, []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		SpecPath:         path,
		BaseURL:          "https://override.example.test",
		Methods:          []string{"GET", "PATCH"},
		ExpectedStatuses: []string{"2XX"},
		Parameters:       map[string]string{"id": "42"},
	}
	targets, err := discoverOpenAPITargets(context.Background(), cfg)
	if err != nil {
		t.Fatalf("discoverOpenAPITargets: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("got %d targets, want 2", len(targets))
	}
	if targets[0].URL != "https://override.example.test/widgets/42" {
		t.Fatalf("GET URL = %q", targets[0].URL)
	}
	if targets[1].URL != "https://override.example.test/widgets/42?verbose=true" {
		t.Fatalf("PATCH URL = %q", targets[1].URL)
	}
	if targets[1].RequestBodyBytes == 0 {
		t.Fatal("PATCH target did not generate a request body")
	}
}

func TestRunTargetsCapturesVariance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMethodNotAllowed)
	}))
	defer server.Close()

	cfg := Config{
		URL:              server.URL,
		Methods:          []string{"GET"},
		ExpectedStatuses: []string{"2XX"},
		Concurrency:      1,
		Iterations:       1,
		Timeout:          time.Second,
		MaxSamples:       10,
		Headers:          make(http.Header),
	}
	report, err := runTargets(context.Background(), cfg, discoverSingleURLTargets(cfg))
	if err != nil {
		t.Fatalf("runTargets: %v", err)
	}
	if report.Summary.RequestsWithVariances != 1 {
		t.Fatalf("variance requests = %d, want 1", report.Summary.RequestsWithVariances)
	}
	if len(report.Samples) != 1 || report.Samples[0].Variances[0].Type != "method_not_supported" {
		t.Fatalf("unexpected samples: %+v", report.Samples)
	}
}

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
security:
  - ApiKeyAuth: []
components:
  securitySchemes:
    ApiKeyAuth:
      type: apiKey
      in: query
      name: api_key
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
		AuthCredentials:  map[string]string{"ApiKeyAuth": "secret"},
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
	if got := targets[0].SecurityRequirementSource; got != securitySourceRoot {
		t.Fatalf("security source = %q, want %q", got, securitySourceRoot)
	}
	if _, ok := targets[0].SecuritySchemes["ApiKeyAuth"]; !ok {
		t.Fatal("target did not include ApiKeyAuth metadata")
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

func TestRunTargetsAppliesOpenAPIQueryAuthWithoutLeakingSecret(t *testing.T) {
	const secret = "top-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("api_key") != secret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := Config{
		Methods:               []string{"GET"},
		AuthCredentials:       map[string]string{"ApiKeyAuth": secret},
		QueryParams:           map[string]string{},
		ExpectedStatuses:      []string{"200"},
		Concurrency:           1,
		Iterations:            1,
		Timeout:               time.Second,
		MaxSamples:            10,
		IncludeSuccessSamples: true,
		Headers:               make(http.Header),
	}
	target := Target{
		ID:                        "secure GET /secure",
		Method:                    "GET",
		URL:                       server.URL + "/secure",
		Source:                    "openapi",
		ExpectedStatuses:          []string{"200"},
		SecurityRequirementSource: securitySourceOperation,
		SecurityRequirements: []SecurityRequirement{
			{SchemeNames: []string{"ApiKeyAuth"}},
		},
		SecuritySchemes: map[string]SecurityScheme{
			"ApiKeyAuth": {
				Name:      "ApiKeyAuth",
				Type:      "apiKey",
				In:        "query",
				ParamName: "api_key",
			},
		},
	}

	report, err := runTargets(context.Background(), cfg, []Target{target})
	if err != nil {
		t.Fatalf("runTargets: %v", err)
	}
	if report.Summary.TransportErrors != 0 || report.Summary.RequestsWithVariances != 0 {
		t.Fatalf("unexpected report summary: %+v", report.Summary)
	}
	if len(report.Samples) != 1 {
		t.Fatalf("sample count = %d, want 1", len(report.Samples))
	}
	if strings.Contains(report.Samples[0].URL, secret) {
		t.Fatalf("sample URL leaked secret: %q", report.Samples[0].URL)
	}
	if len(report.Config.AuthSchemes) != 1 || report.Config.AuthSchemes[0] != "ApiKeyAuth" {
		t.Fatalf("auth scheme report metadata = %#v", report.Config.AuthSchemes)
	}
}

func TestRunTargetsAppliesOpenAPIBearerAuth(t *testing.T) {
	const token = "token-value"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	cfg := Config{
		Methods:               []string{"GET"},
		AuthCredentials:       map[string]string{"BearerAuth": token},
		ExpectedStatuses:      []string{"204"},
		Concurrency:           1,
		Iterations:            1,
		Timeout:               time.Second,
		MaxSamples:            10,
		IncludeSuccessSamples: true,
		Headers:               make(http.Header),
	}
	target := Target{
		ID:                        "secure GET /secure",
		Method:                    "GET",
		URL:                       server.URL + "/secure",
		Source:                    "openapi",
		ExpectedStatuses:          []string{"204"},
		SecurityRequirementSource: securitySourceOperation,
		SecurityRequirements: []SecurityRequirement{
			{SchemeNames: []string{"BearerAuth"}},
		},
		SecuritySchemes: map[string]SecurityScheme{
			"BearerAuth": {
				Name:   "BearerAuth",
				Type:   "http",
				Scheme: "bearer",
			},
		},
	}

	report, err := runTargets(context.Background(), cfg, []Target{target})
	if err != nil {
		t.Fatalf("runTargets: %v", err)
	}
	if report.Summary.RequestsWithVariances != 0 {
		t.Fatalf("variance requests = %d, want 0", report.Summary.RequestsWithVariances)
	}
}

func TestParseConfigRejectsAuthWithoutSpec(t *testing.T) {
	_, err := parseConfig([]string{"-url", "https://api.example.test", "-auth", "ApiKeyAuth=secret"})
	if err == nil {
		t.Fatal("parseConfig succeeded, want error")
	}
	if !strings.Contains(err.Error(), "-auth requires -spec") {
		t.Fatalf("unexpected error: %v", err)
	}
}

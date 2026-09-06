package main

import (
	"net/http"
	"time"
)

var standardMethods = []string{"GET", "PUT", "POST", "DELETE", "OPTIONS", "HEAD", "PATCH", "TRACE"}

type Config struct {
	URL                   string
	SpecPath              string
	BaseURL               string
	Methods               []string
	Headers               http.Header
	Parameters            map[string]string
	QueryParams           map[string]string
	AuthCredentials       map[string]string
	ExpectedStatuses      []string
	RequestBody           []byte
	RequestContentType    string
	Concurrency           int
	Duration              time.Duration
	Iterations            int
	Rate                  float64
	Timeout               time.Duration
	ProgressInterval      time.Duration
	ReportPath            string
	ReportFormat          string
	MaxSamples            int
	IncludeSuccessSamples bool
	FailOnVariance        bool
	InsecureTLS           bool
	ProbeUndocumented     bool
	UserAgent             string
	MaxResponseBytes      int64
	OpenAI                bool
	Model                 string
	EmbeddingModel        string
	Checks                string
	APIToken              string
	MaxTokens             int
	TokenLimitField       string
}

type Target struct {
	ID                         string                    `json:"id"`
	Method                     string                    `json:"method"`
	URL                        string                    `json:"url"`
	Path                       string                    `json:"path,omitempty"`
	OperationID                string                    `json:"operationId,omitempty"`
	Source                     string                    `json:"source"`
	ExpectedStatuses           []string                  `json:"expectedStatuses,omitempty"`
	ExpectedContentTypesByCode map[string][]string       `json:"expectedContentTypesByCode,omitempty"`
	RequestContentType         string                    `json:"requestContentType,omitempty"`
	RequestBodyBytes           int                       `json:"requestBodyBytes,omitempty"`
	RequestBody                []byte                    `json:"-"`
	SecurityRequirementSource  string                    `json:"securityRequirementSource,omitempty"`
	SecurityRequirements       []SecurityRequirement     `json:"securityRequirements,omitempty"`
	SecuritySchemes            map[string]SecurityScheme `json:"securitySchemes,omitempty"`
	Check                      string                    `json:"check,omitempty"`
	SkipReason                 string                    `json:"skipReason,omitempty"`
	RequestedModel             string                    `json:"requestedModel,omitempty"`
}

type SecurityRequirement struct {
	SchemeNames []string `json:"schemeNames"`
}

type SecurityScheme struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	In           string `json:"in,omitempty"`
	ParamName    string `json:"paramName,omitempty"`
	Scheme       string `json:"scheme,omitempty"`
	BearerFormat string `json:"bearerFormat,omitempty"`
}

type Variance struct {
	Type     string `json:"type"`
	Severity string `json:"severity"`
	Expected string `json:"expected,omitempty"`
	Actual   string `json:"actual,omitempty"`
	Message  string `json:"message"`
}

type Result struct {
	TargetID         string     `json:"targetId"`
	Method           string     `json:"method"`
	URL              string     `json:"url"`
	Path             string     `json:"path,omitempty"`
	OperationID      string     `json:"operationId,omitempty"`
	StartedAt        time.Time  `json:"startedAt"`
	DurationMillis   float64    `json:"durationMillis"`
	StatusCode       int        `json:"statusCode,omitempty"`
	Status           string     `json:"status,omitempty"`
	ContentLength    int64      `json:"contentLength"`
	ContentType      string     `json:"contentType,omitempty"`
	Error            string     `json:"error,omitempty"`
	Variances        []Variance `json:"variances,omitempty"`
	Outcome          string     `json:"outcome,omitempty"`
	FirstTokenMillis float64    `json:"firstTokenMillis,omitempty"`
	ReportedModel    string     `json:"reportedModel,omitempty"`
}

type RunReport struct {
	Config         ReportConfig    `json:"config"`
	Targets        []Target        `json:"targets"`
	Summary        RunSummary      `json:"summary"`
	TargetsSummary []TargetSummary `json:"targetsSummary"`
	Samples        []Result        `json:"samples,omitempty"`
}

type ReportConfig struct {
	URL                   string        `json:"url,omitempty"`
	SpecPath              string        `json:"spec,omitempty"`
	BaseURL               string        `json:"baseUrl,omitempty"`
	Methods               []string      `json:"methods"`
	Concurrency           int           `json:"concurrency"`
	Duration              time.Duration `json:"duration"`
	Iterations            int           `json:"iterations"`
	Rate                  float64       `json:"rate"`
	Timeout               time.Duration `json:"timeout"`
	ExpectedStatuses      []string      `json:"expectedStatuses,omitempty"`
	QueryParams           []string      `json:"queryParams,omitempty"`
	AuthSchemes           []string      `json:"authSchemes,omitempty"`
	ProbeUndocumented     bool          `json:"probeUndocumented"`
	MaxSamples            int           `json:"maxSamples"`
	IncludeSuccessSamples bool          `json:"includeSuccessSamples"`
	MaxResponseBytes      int64         `json:"maxResponseBytes"`
	OpenAI                bool          `json:"openai"`
	Model                 string        `json:"model,omitempty"`
	EmbeddingModel        string        `json:"embeddingModel,omitempty"`
	Checks                string        `json:"checks,omitempty"`
	MaxTokens             int           `json:"maxTokens,omitempty"`
	TokenLimitField       string        `json:"tokenLimitField,omitempty"`
}

type RunSummary struct {
	StartedAt             time.Time      `json:"startedAt"`
	EndedAt               time.Time      `json:"endedAt"`
	DurationMillis        float64        `json:"durationMillis"`
	TargetCount           int            `json:"targetCount"`
	UntestedTargets       int            `json:"untestedTargets"`
	TotalRequests         int            `json:"totalRequests"`
	RequestsWithVariances int            `json:"requestsWithVariances"`
	TotalVarianceItems    int            `json:"totalVarianceItems"`
	TransportErrors       int            `json:"transportErrors"`
	StatusCounts          map[string]int `json:"statusCounts,omitempty"`
	VarianceCounts        map[string]int `json:"varianceCounts,omitempty"`
}

type TargetSummary struct {
	TargetID              string         `json:"targetId"`
	Method                string         `json:"method"`
	URL                   string         `json:"url"`
	Path                  string         `json:"path,omitempty"`
	OperationID           string         `json:"operationId,omitempty"`
	Requests              int            `json:"requests"`
	RequestsWithVariances int            `json:"requestsWithVariances"`
	TransportErrors       int            `json:"transportErrors"`
	StatusCounts          map[string]int `json:"statusCounts,omitempty"`
	VarianceCounts        map[string]int `json:"varianceCounts,omitempty"`
	MinLatencyMillis      float64        `json:"minLatencyMillis,omitempty"`
	AvgLatencyMillis      float64        `json:"avgLatencyMillis,omitempty"`
	MaxLatencyMillis      float64        `json:"maxLatencyMillis,omitempty"`
	TotalLatencyMillis    float64        `json:"-"`
	Check                 string         `json:"check,omitempty"`
	SkipReason            string         `json:"skipReason,omitempty"`
	OutcomeCounts         map[string]int `json:"outcomeCounts,omitempty"`
	FirstVariance         *Result        `json:"firstVariance,omitempty"`
	ReportedModels        map[string]int `json:"reportedModels,omitempty"`
}

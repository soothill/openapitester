package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"
)

func main() {
	cfg, err := parseConfig(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "openapitester: %v\n\n", err)
		printUsage(os.Stderr)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	targets, err := discoverTargets(ctx, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "openapitester: %v\n", err)
		os.Exit(1)
	}

	report, err := runTargets(ctx, cfg, targets)
	if err != nil {
		fmt.Fprintf(os.Stderr, "openapitester: %v\n", err)
		os.Exit(1)
	}

	if err := writeConsoleReport(os.Stdout, report); err != nil {
		fmt.Fprintf(os.Stderr, "openapitester: failed to print report: %v\n", err)
		os.Exit(1)
	}

	if cfg.ReportPath != "" {
		if err := writeReportFile(cfg.ReportPath, cfg.ReportFormat, report); err != nil {
			fmt.Fprintf(os.Stderr, "openapitester: failed to write report: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "wrote report: %s\n", cfg.ReportPath)
	}

	if cfg.FailOnVariance && (report.Summary.RequestsWithVariances > 0 || report.Summary.TransportErrors > 0) {
		os.Exit(2)
	}
}

func parseConfig(args []string) (Config, error) {
	var headers headerFlags
	var params paramFlags
	var bodyFile string
	var bodyText string
	var methodsText string
	var expectedStatusText string

	cfg := Config{
		Concurrency:      4,
		Iterations:       1,
		Timeout:          10 * time.Second,
		ProgressInterval: 10 * time.Second,
		ReportFormat:     "auto",
		MaxSamples:       1000,
		UserAgent:        "openapitester/0.1",
	}

	fs := flag.NewFlagSet("openapitester", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&cfg.URL, "url", "", "Single endpoint URL to probe.")
	fs.StringVar(&cfg.SpecPath, "spec", "", "OpenAPI 3.x JSON or YAML file/URL to exercise.")
	fs.StringVar(&cfg.BaseURL, "base-url", "", "Base URL override for OpenAPI specs.")
	fs.StringVar(&methodsText, "methods", "all", "Comma-separated operations to test, or all.")
	fs.Var(&headers, "H", "Request header, repeated. Example: -H 'Authorization: Bearer token'.")
	fs.Var(&headers, "header", "Request header, repeated. Example: -header 'Accept: application/json'.")
	fs.Var(&params, "param", "Path/query parameter value, repeated. Example: -param id=123.")
	fs.StringVar(&expectedStatusText, "expect-status", "2XX,3XX", "Fallback accepted statuses, comma-separated. Supports 200, 2XX, 200-299, default.")
	fs.StringVar(&bodyText, "body", "", "Request body to send when no spec body is generated.")
	fs.StringVar(&bodyFile, "body-file", "", "File containing request body to send when no spec body is generated.")
	fs.StringVar(&cfg.RequestContentType, "content-type", "application/json", "Content-Type used with -body or -body-file.")
	fs.IntVar(&cfg.Concurrency, "concurrency", cfg.Concurrency, "Number of concurrent request workers.")
	fs.DurationVar(&cfg.Duration, "duration", 0, "Run duration. When set, targets repeat until time expires.")
	fs.IntVar(&cfg.Iterations, "iterations", cfg.Iterations, "Passes over all targets when -duration is not set.")
	fs.Float64Var(&cfg.Rate, "rate", 0, "Global request start rate per second. Zero means unthrottled.")
	fs.DurationVar(&cfg.Timeout, "timeout", cfg.Timeout, "Per-request timeout.")
	fs.DurationVar(&cfg.ProgressInterval, "progress", cfg.ProgressInterval, "Progress print interval during runs. Set 0 to disable.")
	fs.StringVar(&cfg.ReportPath, "report", "", "Write a JSON or Markdown report to this path.")
	fs.StringVar(&cfg.ReportFormat, "report-format", cfg.ReportFormat, "Report format: auto, json, markdown, md.")
	fs.IntVar(&cfg.MaxSamples, "max-samples", cfg.MaxSamples, "Maximum sampled request results retained in the report.")
	fs.BoolVar(&cfg.IncludeSuccessSamples, "include-success-samples", false, "Include successful request samples, not only variances.")
	fs.BoolVar(&cfg.FailOnVariance, "fail-on-variance", false, "Exit with code 2 when variances are found.")
	fs.BoolVar(&cfg.InsecureTLS, "insecure", false, "Skip TLS certificate verification.")
	fs.BoolVar(&cfg.ProbeUndocumented, "probe-undocumented", false, "In spec mode, probe standard methods missing from a path item.")
	fs.StringVar(&cfg.UserAgent, "user-agent", cfg.UserAgent, "User-Agent header when one is not supplied.")

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}

	if cfg.URL == "" && cfg.SpecPath == "" {
		return Config{}, errors.New("provide either -url or -spec")
	}
	if cfg.URL != "" && cfg.SpecPath != "" {
		return Config{}, errors.New("provide only one of -url or -spec; use -base-url with -spec")
	}
	if cfg.Concurrency < 1 {
		return Config{}, errors.New("-concurrency must be at least 1")
	}
	if cfg.Iterations < 1 {
		return Config{}, errors.New("-iterations must be at least 1")
	}
	if cfg.Rate < 0 {
		return Config{}, errors.New("-rate must be zero or greater")
	}
	if cfg.Timeout <= 0 {
		return Config{}, errors.New("-timeout must be greater than zero")
	}
	if cfg.MaxSamples < 0 {
		return Config{}, errors.New("-max-samples must be zero or greater")
	}

	methods, err := parseMethods(methodsText)
	if err != nil {
		return Config{}, err
	}
	cfg.Methods = methods

	expectedStatuses, err := parseStatusPatterns(expectedStatusText)
	if err != nil {
		return Config{}, fmt.Errorf("-expect-status: %w", err)
	}
	cfg.ExpectedStatuses = expectedStatuses

	cfg.Headers, err = headers.Header()
	if err != nil {
		return Config{}, err
	}
	cfg.Parameters = params.Map()

	switch {
	case bodyText != "" && bodyFile != "":
		return Config{}, errors.New("provide only one of -body or -body-file")
	case bodyText != "":
		cfg.RequestBody = []byte(bodyText)
	case bodyFile != "":
		body, err := os.ReadFile(bodyFile)
		if err != nil {
			return Config{}, fmt.Errorf("read -body-file: %w", err)
		}
		cfg.RequestBody = body
	}

	return cfg, nil
}

func newHTTPClient(cfg Config) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.InsecureTLS {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec
	}
	return &http.Client{
		Timeout:   cfg.Timeout,
		Transport: transport,
	}
}

func printUsage(out *os.File) {
	fmt.Fprintln(out, `Usage:
  openapitester -url https://api.example.com/widgets
  openapitester -spec openapi.yaml -base-url https://api.example.com -concurrency 20 -duration 1h -report report.md

The standard operation set is GET, PUT, POST, DELETE, OPTIONS, HEAD, PATCH, TRACE.`)
}

type headerFlags []string

func (h *headerFlags) String() string {
	return strings.Join(*h, ", ")
}

func (h *headerFlags) Set(value string) error {
	if _, _, ok := strings.Cut(value, ":"); !ok {
		return fmt.Errorf("header %q must be in Name: Value form", value)
	}
	*h = append(*h, value)
	return nil
}

func (h headerFlags) Header() (http.Header, error) {
	headers := make(http.Header)
	for _, item := range h {
		name, value, _ := strings.Cut(item, ":")
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if name == "" {
			return nil, fmt.Errorf("header %q has an empty name", item)
		}
		headers.Add(name, value)
	}
	return headers, nil
}

type paramFlags []string

func (p *paramFlags) String() string {
	return strings.Join(*p, ", ")
}

func (p *paramFlags) Set(value string) error {
	if _, _, ok := strings.Cut(value, "="); !ok {
		return fmt.Errorf("parameter %q must be in name=value form", value)
	}
	*p = append(*p, value)
	return nil
}

func (p paramFlags) Map() map[string]string {
	values := make(map[string]string, len(p))
	for _, item := range p {
		name, value, _ := strings.Cut(item, "=")
		values[strings.TrimSpace(name)] = strings.TrimSpace(value)
	}
	return values
}

func parseMethods(value string) ([]string, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, "all") {
		return append([]string(nil), standardMethods...), nil
	}

	seen := make(map[string]bool)
	var methods []string
	for _, part := range strings.Split(value, ",") {
		method := strings.ToUpper(strings.TrimSpace(part))
		if method == "" {
			continue
		}
		if !isStandardMethod(method) {
			return nil, fmt.Errorf("unsupported method %q; standard methods are %s", method, strings.Join(standardMethods, ", "))
		}
		if !seen[method] {
			seen[method] = true
			methods = append(methods, method)
		}
	}
	if len(methods) == 0 {
		return nil, errors.New("no methods selected")
	}
	return methods, nil
}

func isStandardMethod(method string) bool {
	for _, candidate := range standardMethods {
		if method == candidate {
			return true
		}
	}
	return false
}

func parseStatusPatterns(value string) ([]string, error) {
	var patterns []string
	for _, part := range strings.Split(value, ",") {
		pattern := strings.ToUpper(strings.TrimSpace(part))
		if pattern == "" {
			continue
		}
		if pattern == "DEFAULT" {
			patterns = append(patterns, "default")
			continue
		}
		if len(pattern) == 3 && pattern[1:] == "XX" && pattern[0] >= '1' && pattern[0] <= '5' {
			patterns = append(patterns, pattern)
			continue
		}
		if strings.Contains(pattern, "-") {
			startText, endText, ok := strings.Cut(pattern, "-")
			if !ok {
				return nil, fmt.Errorf("invalid status range %q", pattern)
			}
			start, startErr := strconv.Atoi(strings.TrimSpace(startText))
			end, endErr := strconv.Atoi(strings.TrimSpace(endText))
			if startErr != nil || endErr != nil || start < 100 || end > 599 || start > end {
				return nil, fmt.Errorf("invalid status range %q", pattern)
			}
			patterns = append(patterns, fmt.Sprintf("%03d-%03d", start, end))
			continue
		}
		code, err := strconv.Atoi(pattern)
		if err != nil || code < 100 || code > 599 {
			return nil, fmt.Errorf("invalid status pattern %q", pattern)
		}
		patterns = append(patterns, fmt.Sprintf("%03d", code))
	}
	if len(patterns) == 0 {
		return nil, errors.New("at least one status pattern is required")
	}
	return patterns, nil
}

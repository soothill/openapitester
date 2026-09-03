package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"
)

func runTargets(ctx context.Context, cfg Config, targets []Target) (*RunReport, error) {
	if len(targets) == 0 {
		return nil, fmt.Errorf("no targets to run")
	}

	startedAt := time.Now()
	client := newHTTPClient(cfg)
	jobs := make(chan Target, cfg.Concurrency*2)
	results := make(chan Result, cfg.Concurrency*2)

	var workers sync.WaitGroup
	for workerID := 0; workerID < cfg.Concurrency; workerID++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for target := range jobs {
				results <- executeTarget(ctx, client, cfg, target)
			}
		}()
	}

	go func() {
		produceJobs(ctx, cfg, targets, jobs)
		close(jobs)
		workers.Wait()
		close(results)
	}()

	aggregator := newReportAggregator(cfg, targets, startedAt)
	var progress <-chan time.Time
	var progressTicker *time.Ticker
	if cfg.ProgressInterval > 0 {
		progressTicker = time.NewTicker(cfg.ProgressInterval)
		defer progressTicker.Stop()
		progress = progressTicker.C
	}

	for {
		select {
		case result, ok := <-results:
			if !ok {
				return aggregator.Report(time.Now()), nil
			}
			aggregator.Add(result)
		case <-progress:
			aggregator.PrintProgress(os.Stderr, time.Since(startedAt))
		}
	}
}

func produceJobs(ctx context.Context, cfg Config, targets []Target, jobs chan<- Target) {
	waitForPace := paceFunc(cfg.Rate)
	if cfg.Duration > 0 {
		deadline := time.Now().Add(cfg.Duration)
		index := 0
		for time.Now().Before(deadline) {
			if !waitForPace(ctx) {
				return
			}
			if !sendJob(ctx, jobs, targets[index%len(targets)]) {
				return
			}
			index++
		}
		return
	}

	for iteration := 0; iteration < cfg.Iterations; iteration++ {
		for _, target := range targets {
			if !waitForPace(ctx) {
				return
			}
			if !sendJob(ctx, jobs, target) {
				return
			}
		}
	}
}

func paceFunc(rate float64) func(context.Context) bool {
	if rate <= 0 {
		return func(ctx context.Context) bool {
			return ctx.Err() == nil
		}
	}
	interval := time.Duration(float64(time.Second) / rate)
	if interval < time.Millisecond {
		interval = time.Millisecond
	}
	ticker := time.NewTicker(interval)
	return func(ctx context.Context) bool {
		select {
		case <-ctx.Done():
			ticker.Stop()
			return false
		case <-ticker.C:
			return true
		}
	}
}

func sendJob(ctx context.Context, jobs chan<- Target, target Target) bool {
	select {
	case <-ctx.Done():
		return false
	case jobs <- target:
		return true
	}
}

func executeTarget(ctx context.Context, client *http.Client, cfg Config, target Target) Result {
	startedAt := time.Now()
	result := Result{
		TargetID:    target.ID,
		Method:      target.Method,
		URL:         target.URL,
		Path:        target.Path,
		OperationID: target.OperationID,
		StartedAt:   startedAt,
	}

	var body io.Reader
	if len(target.RequestBody) > 0 {
		body = bytes.NewReader(target.RequestBody)
	}
	req, err := http.NewRequestWithContext(ctx, target.Method, target.URL, body)
	if err != nil {
		result.DurationMillis = millisSince(startedAt)
		result.Error = err.Error()
		result.Variances = []Variance{transportVariance(err)}
		return result
	}

	applyHeaders(req.Header, cfg.Headers)
	applyQueryParams(req.URL, cfg.QueryParams)
	applyOpenAPIAuth(req, cfg, target)
	if cfg.UserAgent != "" && req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", cfg.UserAgent)
	}
	if len(target.RequestBody) > 0 && target.RequestContentType != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", target.RequestContentType)
	}

	resp, err := client.Do(req)
	result.DurationMillis = millisSince(startedAt)
	if err != nil {
		result.Error = err.Error()
		result.Variances = []Variance{transportVariance(err)}
		return result
	}
	defer resp.Body.Close()

	result.StatusCode = resp.StatusCode
	result.Status = resp.Status
	result.ContentType = resp.Header.Get("Content-Type")
	result.ContentLength = drainBody(resp)
	result.Variances = evaluateVariances(target, result)
	return result
}

func applyHeaders(target http.Header, source http.Header) {
	for key, values := range source {
		for _, value := range values {
			target.Add(key, value)
		}
	}
}

func drainBody(resp *http.Response) int64 {
	if resp.Body == nil {
		return 0
	}
	count, err := io.Copy(io.Discard, resp.Body)
	if err != nil {
		return count
	}
	if resp.ContentLength >= 0 && count == 0 {
		return resp.ContentLength
	}
	return count
}

func transportVariance(err error) Variance {
	return Variance{
		Type:     "transport_error",
		Severity: "error",
		Actual:   err.Error(),
		Message:  "request failed before an HTTP response was received",
	}
}

func millisSince(start time.Time) float64 {
	return float64(time.Since(start).Microseconds()) / 1000
}

type reportAggregator struct {
	cfg       Config
	startedAt time.Time
	targets   []Target
	summary   RunSummary
	byTarget  map[string]*TargetSummary
	samples   []Result
}

func newReportAggregator(cfg Config, targets []Target, startedAt time.Time) *reportAggregator {
	byTarget := make(map[string]*TargetSummary, len(targets))
	summaries := make([]TargetSummary, 0, len(targets))
	for _, target := range targets {
		summary := TargetSummary{
			TargetID:       target.ID,
			Method:         target.Method,
			URL:            target.URL,
			Path:           target.Path,
			OperationID:    target.OperationID,
			StatusCounts:   make(map[string]int),
			VarianceCounts: make(map[string]int),
		}
		summaries = append(summaries, summary)
		byTarget[target.ID] = &summaries[len(summaries)-1]
	}

	return &reportAggregator{
		cfg:       cfg,
		startedAt: startedAt,
		targets:   targets,
		byTarget:  byTarget,
		summary: RunSummary{
			StartedAt:      startedAt,
			TargetCount:    len(targets),
			StatusCounts:   make(map[string]int),
			VarianceCounts: make(map[string]int),
		},
	}
}

func (a *reportAggregator) Add(result Result) {
	a.summary.TotalRequests++
	statusKey := statusCountKey(result)
	a.summary.StatusCounts[statusKey]++
	if result.Error != "" {
		a.summary.TransportErrors++
	}
	if len(result.Variances) > 0 {
		a.summary.RequestsWithVariances++
		a.summary.TotalVarianceItems += len(result.Variances)
	}
	for _, variance := range result.Variances {
		a.summary.VarianceCounts[variance.Type]++
	}

	targetSummary := a.byTarget[result.TargetID]
	if targetSummary != nil {
		targetSummary.Requests++
		targetSummary.StatusCounts[statusKey]++
		if result.Error != "" {
			targetSummary.TransportErrors++
		}
		if len(result.Variances) > 0 {
			targetSummary.RequestsWithVariances++
		}
		for _, variance := range result.Variances {
			targetSummary.VarianceCounts[variance.Type]++
		}
		if result.DurationMillis > 0 {
			if targetSummary.MinLatencyMillis == 0 || result.DurationMillis < targetSummary.MinLatencyMillis {
				targetSummary.MinLatencyMillis = result.DurationMillis
			}
			if result.DurationMillis > targetSummary.MaxLatencyMillis {
				targetSummary.MaxLatencyMillis = result.DurationMillis
			}
			targetSummary.TotalLatencyMillis += result.DurationMillis
			targetSummary.AvgLatencyMillis = targetSummary.TotalLatencyMillis / float64(targetSummary.Requests)
		}
	}

	if a.cfg.MaxSamples == 0 || len(a.samples) >= a.cfg.MaxSamples {
		return
	}
	if len(result.Variances) > 0 || a.cfg.IncludeSuccessSamples {
		a.samples = append(a.samples, result)
	}
}

func (a *reportAggregator) PrintProgress(out *os.File, elapsed time.Duration) {
	fmt.Fprintf(out, "progress elapsed=%s requests=%d variance_requests=%d transport_errors=%d\n",
		elapsed.Truncate(time.Second),
		a.summary.TotalRequests,
		a.summary.RequestsWithVariances,
		a.summary.TransportErrors,
	)
}

func (a *reportAggregator) Report(endedAt time.Time) *RunReport {
	a.summary.EndedAt = endedAt
	a.summary.DurationMillis = float64(endedAt.Sub(a.startedAt).Microseconds()) / 1000
	targetSummaries := make([]TargetSummary, 0, len(a.targets))
	for _, target := range a.targets {
		if summary := a.byTarget[target.ID]; summary != nil {
			targetSummaries = append(targetSummaries, *summary)
		}
	}

	return &RunReport{
		Config: ReportConfig{
			URL:                   a.cfg.URL,
			SpecPath:              a.cfg.SpecPath,
			BaseURL:               a.cfg.BaseURL,
			Methods:               append([]string(nil), a.cfg.Methods...),
			Concurrency:           a.cfg.Concurrency,
			Duration:              a.cfg.Duration,
			Iterations:            a.cfg.Iterations,
			Rate:                  a.cfg.Rate,
			Timeout:               a.cfg.Timeout,
			ExpectedStatuses:      append([]string(nil), a.cfg.ExpectedStatuses...),
			QueryParams:           sortedStringMapKeys(a.cfg.QueryParams),
			AuthSchemes:           sortedStringMapKeys(a.cfg.AuthCredentials),
			ProbeUndocumented:     a.cfg.ProbeUndocumented,
			MaxSamples:            a.cfg.MaxSamples,
			IncludeSuccessSamples: a.cfg.IncludeSuccessSamples,
		},
		Targets:        a.targets,
		Summary:        a.summary,
		TargetsSummary: targetSummaries,
		Samples:        a.samples,
	}
}

func statusCountKey(result Result) string {
	if result.Error != "" {
		return "transport_error"
	}
	if result.StatusCode == 0 {
		return "no_status"
	}
	return fmt.Sprintf("%d", result.StatusCode)
}

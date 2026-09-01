package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func writeConsoleReport(out io.Writer, report *RunReport) error {
	fmt.Fprintln(out, "OpenAPI endpoint test report")
	fmt.Fprintf(out, "Targets: %d  Requests: %d  Variance requests: %d  Transport errors: %d  Elapsed: %s\n",
		report.Summary.TargetCount,
		report.Summary.TotalRequests,
		report.Summary.RequestsWithVariances,
		report.Summary.TransportErrors,
		durationFromMillis(report.Summary.DurationMillis),
	)

	if len(report.Summary.VarianceCounts) > 0 {
		fmt.Fprintln(out, "\nVariance counts:")
		for _, key := range sortedCountKeys(report.Summary.VarianceCounts) {
			fmt.Fprintf(out, "  %-24s %d\n", key, report.Summary.VarianceCounts[key])
		}
	}

	fmt.Fprintln(out, "\nTargets:")
	for _, target := range report.TargetsSummary {
		fmt.Fprintf(out, "  %-7s %-48s requests=%d variance_requests=%d avg=%0.1fms min=%0.1fms max=%0.1fms statuses=%s\n",
			target.Method,
			shorten(target.URL, 48),
			target.Requests,
			target.RequestsWithVariances,
			target.AvgLatencyMillis,
			target.MinLatencyMillis,
			target.MaxLatencyMillis,
			formatCounts(target.StatusCounts),
		)
	}

	if len(report.Samples) > 0 {
		fmt.Fprintln(out, "\nSamples:")
		for index, sample := range report.Samples {
			status := sample.Status
			if status == "" {
				status = sample.Error
			}
			fmt.Fprintf(out, "  %d. %s %s -> %s (%0.1fms)\n", index+1, sample.Method, sample.URL, status, sample.DurationMillis)
			for _, variance := range sample.Variances {
				fmt.Fprintf(out, "     [%s] %s expected=%q actual=%q\n", variance.Type, variance.Message, variance.Expected, variance.Actual)
			}
		}
	}

	return nil
}

func writeReportFile(path string, format string, report *RunReport) error {
	format = normalizeReportFormat(path, format)
	var body []byte
	var err error
	switch format {
	case "json":
		body, err = json.MarshalIndent(report, "", "  ")
	case "markdown":
		body = []byte(markdownReport(report))
	default:
		return fmt.Errorf("unsupported report format %q", format)
	}
	if err != nil {
		return err
	}
	return os.WriteFile(path, body, 0o644)
}

func normalizeReportFormat(path string, format string) string {
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "auto" || format == "" {
		switch strings.ToLower(filepath.Ext(path)) {
		case ".md", ".markdown":
			return "markdown"
		default:
			return "json"
		}
	}
	if format == "md" {
		return "markdown"
	}
	return format
}

func markdownReport(report *RunReport) string {
	var b strings.Builder
	fmt.Fprintln(&b, "# OpenAPI Endpoint Test Report")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "- Started: `%s`\n", report.Summary.StartedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "- Ended: `%s`\n", report.Summary.EndedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "- Elapsed: `%s`\n", durationFromMillis(report.Summary.DurationMillis))
	fmt.Fprintf(&b, "- Targets: `%d`\n", report.Summary.TargetCount)
	fmt.Fprintf(&b, "- Requests: `%d`\n", report.Summary.TotalRequests)
	fmt.Fprintf(&b, "- Requests with variances: `%d`\n", report.Summary.RequestsWithVariances)
	fmt.Fprintf(&b, "- Transport errors: `%d`\n", report.Summary.TransportErrors)

	if len(report.Summary.VarianceCounts) > 0 {
		fmt.Fprintln(&b)
		fmt.Fprintln(&b, "## Variance Counts")
		fmt.Fprintln(&b)
		fmt.Fprintln(&b, "| Type | Count |")
		fmt.Fprintln(&b, "| --- | ---: |")
		for _, key := range sortedCountKeys(report.Summary.VarianceCounts) {
			fmt.Fprintf(&b, "| `%s` | %d |\n", escapeMarkdownTable(key), report.Summary.VarianceCounts[key])
		}
	}

	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "## Target Summary")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "| Method | URL | Requests | Variance Requests | Errors | Avg | Min | Max | Statuses |")
	fmt.Fprintln(&b, "| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |")
	for _, target := range report.TargetsSummary {
		fmt.Fprintf(&b, "| `%s` | `%s` | %d | %d | %d | %.1fms | %.1fms | %.1fms | `%s` |\n",
			target.Method,
			escapeMarkdownTable(target.URL),
			target.Requests,
			target.RequestsWithVariances,
			target.TransportErrors,
			target.AvgLatencyMillis,
			target.MinLatencyMillis,
			target.MaxLatencyMillis,
			escapeMarkdownTable(formatCounts(target.StatusCounts)),
		)
	}

	if len(report.Samples) > 0 {
		fmt.Fprintln(&b)
		fmt.Fprintln(&b, "## Samples")
		for index, sample := range report.Samples {
			status := sample.Status
			if status == "" {
				status = sample.Error
			}
			fmt.Fprintln(&b)
			fmt.Fprintf(&b, "### %d. `%s %s`\n", index+1, sample.Method, sample.URL)
			fmt.Fprintf(&b, "- Status: `%s`\n", status)
			fmt.Fprintf(&b, "- Latency: `%.1fms`\n", sample.DurationMillis)
			fmt.Fprintf(&b, "- Content-Type: `%s`\n", sample.ContentType)
			if len(sample.Variances) > 0 {
				fmt.Fprintln(&b, "- Variances:")
				for _, variance := range sample.Variances {
					fmt.Fprintf(&b, "  - `%s`: %s", variance.Type, variance.Message)
					if variance.Expected != "" {
						fmt.Fprintf(&b, " Expected `%s`.", variance.Expected)
					}
					if variance.Actual != "" {
						fmt.Fprintf(&b, " Actual `%s`.", variance.Actual)
					}
					fmt.Fprintln(&b)
				}
			}
		}
	}
	return b.String()
}

func sortedCountKeys(values map[string]int) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func formatCounts(values map[string]int) string {
	if len(values) == 0 {
		return "-"
	}
	keys := sortedCountKeys(values)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s:%d", key, values[key]))
	}
	return strings.Join(parts, ",")
}

func durationFromMillis(millis float64) time.Duration {
	return time.Duration(millis * float64(time.Millisecond))
}

func shorten(value string, max int) string {
	if len(value) <= max {
		return value
	}
	if max <= 3 {
		return value[:max]
	}
	return value[:max-3] + "..."
}

func escapeMarkdownTable(value string) string {
	return strings.ReplaceAll(value, "|", "\\|")
}

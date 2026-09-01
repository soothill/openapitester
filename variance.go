package main

import (
	"fmt"
	"mime"
	"strconv"
	"strings"
)

func evaluateVariances(target Target, result Result) []Variance {
	if result.Error != "" {
		return result.Variances
	}

	var variances []Variance
	if len(target.ExpectedStatuses) > 0 && !statusAllowed(result.StatusCode, target.ExpectedStatuses) {
		varianceType := "status_mismatch"
		message := "response status did not match the documented or configured expectation"
		if result.StatusCode == 405 || result.StatusCode == 501 {
			varianceType = "method_not_supported"
			message = "operation appears unsupported by the endpoint"
		}
		variances = append(variances, Variance{
			Type:     varianceType,
			Severity: "warning",
			Expected: strings.Join(target.ExpectedStatuses, ","),
			Actual:   fmt.Sprintf("%d", result.StatusCode),
			Message:  message,
		})
	}

	expectedContentTypes := expectedContentTypesForStatus(target, result.StatusCode)
	if len(expectedContentTypes) > 0 && result.StatusCode != 204 && target.Method != "HEAD" {
		if result.ContentType == "" {
			variances = append(variances, Variance{
				Type:     "missing_content_type",
				Severity: "warning",
				Expected: strings.Join(expectedContentTypes, ","),
				Message:  "response did not include a Content-Type header",
			})
		} else if !contentTypeAllowed(result.ContentType, expectedContentTypes) {
			variances = append(variances, Variance{
				Type:     "content_type_mismatch",
				Severity: "warning",
				Expected: strings.Join(expectedContentTypes, ","),
				Actual:   result.ContentType,
				Message:  "response Content-Type did not match the OpenAPI response content",
			})
		}
	}

	return variances
}

func statusAllowed(status int, patterns []string) bool {
	for _, pattern := range patterns {
		pattern = strings.ToUpper(strings.TrimSpace(pattern))
		if pattern == "DEFAULT" {
			return true
		}
		if len(pattern) == 3 && pattern[1:] == "XX" {
			if status/100 == int(pattern[0]-'0') {
				return true
			}
			continue
		}
		if strings.Contains(pattern, "-") {
			startText, endText, ok := strings.Cut(pattern, "-")
			if !ok {
				continue
			}
			start, startErr := strconv.Atoi(startText)
			end, endErr := strconv.Atoi(endText)
			if startErr == nil && endErr == nil && status >= start && status <= end {
				return true
			}
			continue
		}
		code, err := strconv.Atoi(pattern)
		if err == nil && code == status {
			return true
		}
	}
	return false
}

func expectedContentTypesForStatus(target Target, status int) []string {
	if len(target.ExpectedContentTypesByCode) == 0 {
		return nil
	}
	keys := []string{
		fmt.Sprintf("%03d", status),
		fmt.Sprintf("%dXX", status/100),
		"default",
	}
	for _, key := range keys {
		if values := target.ExpectedContentTypesByCode[key]; len(values) > 0 {
			return values
		}
	}
	return nil
}

func contentTypeAllowed(actual string, expected []string) bool {
	actualBase := mediaTypeBase(actual)
	for _, expectedType := range expected {
		expectedBase := mediaTypeBase(expectedType)
		if expectedBase == "*/*" || actualBase == expectedBase {
			return true
		}
		if strings.HasSuffix(expectedBase, "/*") {
			prefix := strings.TrimSuffix(expectedBase, "*")
			if strings.HasPrefix(actualBase, prefix) {
				return true
			}
		}
	}
	return false
}

func mediaTypeBase(value string) string {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		mediaType = strings.TrimSpace(strings.Split(value, ";")[0])
	}
	return strings.ToLower(mediaType)
}

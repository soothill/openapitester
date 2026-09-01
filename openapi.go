package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

var pathParameterPattern = regexp.MustCompile(`\{([^}/]+)\}`)

func discoverTargets(ctx context.Context, cfg Config) ([]Target, error) {
	if cfg.URL != "" {
		return discoverSingleURLTargets(cfg), nil
	}
	return discoverOpenAPITargets(ctx, cfg)
}

func discoverSingleURLTargets(cfg Config) []Target {
	targets := make([]Target, 0, len(cfg.Methods))
	for _, method := range cfg.Methods {
		body := requestBodyForManualTarget(method, cfg)
		targets = append(targets, Target{
			ID:                 method + " " + cfg.URL,
			Method:             method,
			URL:                cfg.URL,
			Source:             "url",
			ExpectedStatuses:   append([]string(nil), cfg.ExpectedStatuses...),
			RequestBody:        body,
			RequestBodyBytes:   len(body),
			RequestContentType: contentTypeForManualTarget(body, cfg),
		})
	}
	return targets
}

func requestBodyForManualTarget(method string, cfg Config) []byte {
	if len(cfg.RequestBody) == 0 {
		return nil
	}
	switch method {
	case "POST", "PUT", "PATCH", "DELETE":
		return append([]byte(nil), cfg.RequestBody...)
	default:
		return nil
	}
}

func contentTypeForManualTarget(body []byte, cfg Config) string {
	if len(body) == 0 {
		return ""
	}
	return cfg.RequestContentType
}

func discoverOpenAPITargets(ctx context.Context, cfg Config) ([]Target, error) {
	root, err := loadOpenAPIDocument(ctx, cfg.SpecPath)
	if err != nil {
		return nil, err
	}

	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = firstServerURL(root, cfg.Parameters)
	}
	if baseURL == "" {
		baseURL = "http://localhost"
	}

	paths := asMap(root["paths"])
	if len(paths) == 0 {
		return nil, fmt.Errorf("%s does not contain any OpenAPI paths", cfg.SpecPath)
	}

	pathNames := sortedKeys(paths)
	targets := make([]Target, 0)
	for _, pathName := range pathNames {
		pathItem := asMap(dereference(root, paths[pathName], 0))
		if len(pathItem) == 0 {
			continue
		}
		pathLevelParameters := collectParameters(root, pathItem["parameters"])

		for _, method := range cfg.Methods {
			operationRaw, documented := pathItem[strings.ToLower(method)]
			if !documented {
				if !cfg.ProbeUndocumented {
					continue
				}
				targetURL, err := buildOperationURL(root, baseURL, pathName, pathLevelParameters, cfg.Parameters)
				if err != nil {
					return nil, fmt.Errorf("%s %s: %w", method, pathName, err)
				}
				targets = append(targets, Target{
					ID:               method + " " + pathName,
					Method:           method,
					URL:              targetURL,
					Path:             pathName,
					Source:           "openapi-undocumented",
					ExpectedStatuses: append([]string(nil), cfg.ExpectedStatuses...),
				})
				continue
			}

			operation := asMap(dereference(root, operationRaw, 0))
			if len(operation) == 0 {
				continue
			}
			allParameters := append([]map[string]any{}, pathLevelParameters...)
			allParameters = append(allParameters, collectParameters(root, operation["parameters"])...)

			targetURL, err := buildOperationURL(root, baseURL, pathName, allParameters, cfg.Parameters)
			if err != nil {
				return nil, fmt.Errorf("%s %s: %w", method, pathName, err)
			}

			expectedStatuses, expectedContentTypes := expectedResponses(root, operation, cfg.ExpectedStatuses)
			body, contentType, err := requestBodyForOperation(root, operation, cfg)
			if err != nil {
				return nil, fmt.Errorf("%s %s request body: %w", method, pathName, err)
			}

			operationID, _ := operation["operationId"].(string)
			targetID := method + " " + pathName
			if operationID != "" {
				targetID = operationID + " " + method + " " + pathName
			}

			targets = append(targets, Target{
				ID:                         targetID,
				Method:                     method,
				URL:                        targetURL,
				Path:                       pathName,
				OperationID:                operationID,
				Source:                     "openapi",
				ExpectedStatuses:           expectedStatuses,
				ExpectedContentTypesByCode: expectedContentTypes,
				RequestBody:                body,
				RequestBodyBytes:           len(body),
				RequestContentType:         contentType,
			})
		}
	}

	if len(targets) == 0 {
		return nil, fmt.Errorf("no targets discovered from %s for methods %s", cfg.SpecPath, strings.Join(cfg.Methods, ", "))
	}
	return targets, nil
}

func loadOpenAPIDocument(ctx context.Context, path string) (map[string]any, error) {
	var data []byte
	var err error
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetch spec: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("fetch spec: %s", resp.Status)
		}
		data, err = io.ReadAll(resp.Body)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, err
	}

	var root map[string]any
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse OpenAPI document: %w", err)
	}
	return root, nil
}

func firstServerURL(root map[string]any, overrides map[string]string) string {
	servers := asSlice(root["servers"])
	for _, serverRaw := range servers {
		server := asMap(serverRaw)
		serverURL, _ := server["url"].(string)
		if serverURL == "" {
			continue
		}
		return applyServerVariables(serverURL, server["variables"], overrides)
	}
	return ""
}

func applyServerVariables(serverURL string, rawVariables any, overrides map[string]string) string {
	variables := asMap(rawVariables)
	return pathParameterPattern.ReplaceAllStringFunc(serverURL, func(match string) string {
		name := strings.TrimSuffix(strings.TrimPrefix(match, "{"), "}")
		if value, ok := overrides[name]; ok {
			return url.PathEscape(value)
		}
		variable := asMap(variables[name])
		if value, ok := variable["default"].(string); ok {
			return url.PathEscape(value)
		}
		return match
	})
}

func buildOperationURL(root map[string]any, baseURL string, pathName string, parameters []map[string]any, overrides map[string]string) (string, error) {
	pathValues := make(map[string]string)
	queryValues := make(url.Values)

	for _, parameter := range parameters {
		name, _ := parameter["name"].(string)
		location, _ := parameter["in"].(string)
		if name == "" {
			continue
		}
		value := sampleParameterValue(root, parameter, overrides)
		switch location {
		case "path":
			pathValues[name] = value
		case "query":
			if required, _ := parameter["required"].(bool); required {
				queryValues.Set(name, value)
			}
		}
	}

	path := pathParameterPattern.ReplaceAllStringFunc(pathName, func(match string) string {
		name := strings.TrimSuffix(strings.TrimPrefix(match, "{"), "}")
		if value, ok := overrides[name]; ok {
			return url.PathEscape(value)
		}
		if value, ok := pathValues[name]; ok {
			return url.PathEscape(value)
		}
		return "1"
	})

	joined := joinBaseURLAndPath(baseURL, path)
	parsed, err := url.Parse(joined)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	for key, values := range queryValues {
		if _, exists := query[key]; exists {
			continue
		}
		for _, value := range values {
			query.Add(key, value)
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func joinBaseURLAndPath(baseURL string, path string) string {
	if baseURL == "" {
		baseURL = "http://localhost"
	}
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	if strings.HasPrefix(baseURL, "http://") || strings.HasPrefix(baseURL, "https://") {
		return strings.TrimRight(baseURL, "/") + "/" + strings.TrimLeft(path, "/")
	}
	return "http://localhost" + "/" + strings.Trim(strings.TrimRight(baseURL, "/")+"/"+strings.TrimLeft(path, "/"), "/")
}

func collectParameters(root map[string]any, raw any) []map[string]any {
	items := asSlice(raw)
	parameters := make([]map[string]any, 0, len(items))
	for _, item := range items {
		parameter := asMap(dereference(root, item, 0))
		if len(parameter) > 0 {
			parameters = append(parameters, parameter)
		}
	}
	return parameters
}

func sampleParameterValue(root map[string]any, parameter map[string]any, overrides map[string]string) string {
	name, _ := parameter["name"].(string)
	if value, ok := overrides[name]; ok {
		return value
	}
	if value, ok := parameter["example"]; ok {
		return sampleToString(value)
	}
	if value, ok := parameter["default"]; ok {
		return sampleToString(value)
	}
	schema := asMap(dereference(root, parameter["schema"], 0))
	if value, ok := schema["example"]; ok {
		return sampleToString(value)
	}
	if value, ok := schema["default"]; ok {
		return sampleToString(value)
	}
	sample := sampleFromSchema(root, schema, name, 0)
	return sampleToString(sample)
}

func expectedResponses(root map[string]any, operation map[string]any, fallback []string) ([]string, map[string][]string) {
	responses := asMap(dereference(root, operation["responses"], 0))
	if len(responses) == 0 {
		return append([]string(nil), fallback...), nil
	}

	expectedStatuses := make([]string, 0, len(responses))
	expectedContentTypes := make(map[string][]string)
	for _, key := range sortedKeys(responses) {
		statusKey, ok := normalizeResponseStatusKey(key)
		if !ok {
			continue
		}
		expectedStatuses = append(expectedStatuses, statusKey)

		response := asMap(dereference(root, responses[key], 0))
		content := asMap(response["content"])
		if len(content) == 0 {
			continue
		}
		for _, mediaType := range sortedKeys(content) {
			expectedContentTypes[statusKey] = append(expectedContentTypes[statusKey], mediaType)
		}
	}
	if len(expectedStatuses) == 0 {
		expectedStatuses = append([]string(nil), fallback...)
	}
	return expectedStatuses, expectedContentTypes
}

func requestBodyForOperation(root map[string]any, operation map[string]any, cfg Config) ([]byte, string, error) {
	if len(cfg.RequestBody) > 0 {
		return append([]byte(nil), cfg.RequestBody...), cfg.RequestContentType, nil
	}

	requestBody := asMap(dereference(root, operation["requestBody"], 0))
	if len(requestBody) == 0 {
		return nil, "", nil
	}

	content := asMap(requestBody["content"])
	if len(content) == 0 {
		return nil, "", nil
	}

	mediaType := chooseMediaType(content)
	if mediaType == "" {
		return nil, "", nil
	}
	media := asMap(content[mediaType])

	if value, ok := media["example"]; ok {
		body, err := marshalBody(mediaType, value)
		return body, mediaType, err
	}

	examples := asMap(media["examples"])
	for _, key := range sortedKeys(examples) {
		example := asMap(dereference(root, examples[key], 0))
		if value, ok := example["value"]; ok {
			body, err := marshalBody(mediaType, value)
			return body, mediaType, err
		}
	}

	schema := dereference(root, media["schema"], 0)
	if schema == nil {
		return nil, mediaType, nil
	}
	body, err := marshalBody(mediaType, sampleFromSchema(root, schema, "", 0))
	return body, mediaType, err
}

func chooseMediaType(content map[string]any) string {
	if _, ok := content["application/json"]; ok {
		return "application/json"
	}
	keys := sortedKeys(content)
	for _, key := range keys {
		if strings.Contains(key, "+json") || strings.Contains(key, "json") {
			return key
		}
	}
	if len(keys) > 0 {
		return keys[0]
	}
	return ""
}

func marshalBody(mediaType string, value any) ([]byte, error) {
	if value == nil {
		return nil, nil
	}
	normalized := strings.ToLower(mediaType)
	if strings.Contains(normalized, "x-www-form-urlencoded") {
		form := url.Values{}
		if values := asMap(value); len(values) > 0 {
			for _, key := range sortedKeys(values) {
				form.Set(key, sampleToString(values[key]))
			}
			return []byte(form.Encode()), nil
		}
	}
	if text, ok := value.(string); ok && !strings.Contains(normalized, "json") {
		return []byte(text), nil
	}
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return body, nil
}

func sampleFromSchema(root map[string]any, rawSchema any, name string, depth int) any {
	if depth > 8 {
		return fallbackSampleForName(name)
	}
	schema := asMap(dereference(root, rawSchema, depth+1))
	if len(schema) == 0 {
		return fallbackSampleForName(name)
	}

	if value, ok := schema["example"]; ok {
		return value
	}
	if value, ok := schema["default"]; ok {
		return value
	}
	enum := asSlice(schema["enum"])
	if len(enum) > 0 {
		return enum[0]
	}

	for _, keyword := range []string{"oneOf", "anyOf"} {
		options := asSlice(schema[keyword])
		if len(options) > 0 {
			return sampleFromSchema(root, options[0], name, depth+1)
		}
	}
	if allOf := asSlice(schema["allOf"]); len(allOf) > 0 {
		merged := make(map[string]any)
		for _, part := range allOf {
			if value := sampleFromSchema(root, part, name, depth+1); value != nil {
				if object, ok := value.(map[string]any); ok {
					for key, item := range object {
						merged[key] = item
					}
				}
			}
		}
		if len(merged) > 0 {
			return merged
		}
	}

	schemaType, _ := schema["type"].(string)
	properties := asMap(schema["properties"])
	if schemaType == "object" || len(properties) > 0 {
		object := make(map[string]any)
		required := make(map[string]bool)
		for _, item := range asSlice(schema["required"]) {
			if key, ok := item.(string); ok {
				required[key] = true
			}
		}
		propertyNames := sortedKeys(properties)
		for _, propertyName := range propertyNames {
			if len(required) > 0 && !required[propertyName] {
				continue
			}
			object[propertyName] = sampleFromSchema(root, properties[propertyName], propertyName, depth+1)
		}
		if len(object) == 0 {
			for _, propertyName := range propertyNames {
				object[propertyName] = sampleFromSchema(root, properties[propertyName], propertyName, depth+1)
				if len(object) >= 3 {
					break
				}
			}
		}
		if len(object) == 0 {
			object["value"] = "test"
		}
		return object
	}

	switch schemaType {
	case "array":
		return []any{sampleFromSchema(root, schema["items"], name, depth+1)}
	case "integer":
		return 1
	case "number":
		return 1.1
	case "boolean":
		return true
	case "string":
		return sampleString(schema, name)
	default:
		if _, ok := schema["items"]; ok {
			return []any{sampleFromSchema(root, schema["items"], name, depth+1)}
		}
		return fallbackSampleForName(name)
	}
}

func sampleString(schema map[string]any, name string) string {
	format, _ := schema["format"].(string)
	switch format {
	case "date-time":
		return "2026-01-01T00:00:00Z"
	case "date":
		return "2026-01-01"
	case "uuid":
		return "00000000-0000-0000-0000-000000000001"
	case "email":
		return "tester@example.com"
	case "uri", "url":
		return "https://example.com"
	default:
		return fallbackSampleForName(name)
	}
}

func fallbackSampleForName(name string) string {
	lower := strings.ToLower(name)
	switch {
	case lower == "id" || strings.HasSuffix(lower, "id"):
		return "1"
	case strings.Contains(lower, "email"):
		return "tester@example.com"
	case strings.Contains(lower, "name"):
		return "test"
	case strings.Contains(lower, "date"):
		return "2026-01-01"
	default:
		return "test"
	}
}

func sampleToString(value any) string {
	switch typed := value.(type) {
	case nil:
		return "test"
	case string:
		return typed
	case int:
		return fmt.Sprintf("%d", typed)
	case int64:
		return fmt.Sprintf("%d", typed)
	case float64:
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%f", typed), "0"), ".")
	case bool:
		if typed {
			return "true"
		}
		return "false"
	default:
		body, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprint(typed)
		}
		return string(body)
	}
}

func normalizeResponseStatusKey(key string) (string, bool) {
	patterns, err := parseStatusPatterns(key)
	if err != nil || len(patterns) != 1 {
		return "", false
	}
	return patterns[0], true
}

func dereference(root map[string]any, value any, depth int) any {
	if depth > 12 {
		return value
	}
	object := asMap(value)
	ref, _ := object["$ref"].(string)
	if ref == "" {
		return value
	}
	resolved, ok := resolveJSONPointer(root, ref)
	if !ok {
		return value
	}
	return dereference(root, resolved, depth+1)
}

func resolveJSONPointer(root map[string]any, ref string) (any, bool) {
	if !strings.HasPrefix(ref, "#/") {
		return nil, false
	}
	var current any = root
	for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		object := asMap(current)
		if object == nil {
			return nil, false
		}
		next, ok := object[part]
		if !ok {
			return nil, false
		}
		current = next
	}
	return current, true
}

func asMap(value any) map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		return typed
	case map[interface{}]interface{}:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[fmt.Sprint(key)] = item
		}
		return out
	default:
		return nil
	}
}

func asSlice(value any) []any {
	switch typed := value.(type) {
	case []any:
		return typed
	default:
		return nil
	}
}

func sortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

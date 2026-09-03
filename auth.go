package main

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

const (
	securitySourceRoot      = "root"
	securitySourceOperation = "operation"
)

func discoverSecuritySchemes(root map[string]any) map[string]SecurityScheme {
	components := asMap(root["components"])
	rawSchemes := asMap(components["securitySchemes"])
	if len(rawSchemes) == 0 {
		return nil
	}

	schemes := make(map[string]SecurityScheme, len(rawSchemes))
	for _, name := range sortedKeys(rawSchemes) {
		rawScheme := asMap(dereference(root, rawSchemes[name], 0))
		if len(rawScheme) == 0 {
			continue
		}
		schemeType, _ := rawScheme["type"].(string)
		location, _ := rawScheme["in"].(string)
		paramName, _ := rawScheme["name"].(string)
		httpScheme, _ := rawScheme["scheme"].(string)
		bearerFormat, _ := rawScheme["bearerFormat"].(string)
		schemes[name] = SecurityScheme{
			Name:         name,
			Type:         schemeType,
			In:           location,
			ParamName:    paramName,
			Scheme:       httpScheme,
			BearerFormat: bearerFormat,
		}
	}
	return schemes
}

func validateAuthCredentials(credentials map[string]string, schemes map[string]SecurityScheme) error {
	if len(credentials) == 0 {
		return nil
	}
	if len(schemes) == 0 {
		return fmt.Errorf("-auth was provided, but the OpenAPI spec does not define components.securitySchemes")
	}

	var unknown []string
	var empty []string
	for name, value := range credentials {
		if _, ok := schemes[name]; !ok {
			unknown = append(unknown, name)
		}
		if value == "" {
			empty = append(empty, name)
		}
	}
	sort.Strings(unknown)
	sort.Strings(empty)
	if len(unknown) > 0 {
		return fmt.Errorf("unknown -auth scheme(s): %s; available schemes: %s", strings.Join(unknown, ", "), strings.Join(sortedSecuritySchemeNames(schemes), ", "))
	}
	if len(empty) > 0 {
		return fmt.Errorf("empty -auth credential for scheme(s): %s", strings.Join(empty, ", "))
	}
	return nil
}

func parseSecurityRequirements(raw any) []SecurityRequirement {
	items := asSlice(raw)
	requirements := make([]SecurityRequirement, 0, len(items))
	for _, item := range items {
		requirementObject := asMap(item)
		if requirementObject == nil {
			continue
		}
		requirements = append(requirements, SecurityRequirement{
			SchemeNames: sortedKeys(requirementObject),
		})
	}
	return requirements
}

func targetSecurity(operation map[string]any, rootSecurity []SecurityRequirement, hasRootSecurity bool) ([]SecurityRequirement, string) {
	if raw, ok := operation["security"]; ok {
		return parseSecurityRequirements(raw), securitySourceOperation
	}
	if hasRootSecurity {
		return rootSecurity, securitySourceRoot
	}
	return nil, ""
}

func securitySchemesForTarget(allSchemes map[string]SecurityScheme, requirements []SecurityRequirement, source string, credentials map[string]string) map[string]SecurityScheme {
	if len(allSchemes) == 0 {
		return nil
	}

	names := make(map[string]bool)
	if source != "" {
		for _, requirement := range requirements {
			for _, name := range requirement.SchemeNames {
				names[name] = true
			}
		}
	} else {
		for name := range credentials {
			names[name] = true
		}
	}

	schemes := make(map[string]SecurityScheme)
	for name := range names {
		if scheme, ok := allSchemes[name]; ok {
			schemes[name] = scheme
		}
	}
	if len(schemes) == 0 {
		return nil
	}
	return schemes
}

func sortedSecuritySchemeNames(schemes map[string]SecurityScheme) []string {
	names := make([]string, 0, len(schemes))
	for name := range schemes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sortedStringMapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func applyQueryParams(rawURL *url.URL, params map[string]string) {
	if rawURL == nil || len(params) == 0 {
		return
	}
	query := rawURL.Query()
	for key, value := range params {
		query.Set(key, value)
	}
	rawURL.RawQuery = query.Encode()
}

func applyOpenAPIAuth(req *http.Request, cfg Config, target Target) {
	if len(cfg.AuthCredentials) == 0 || len(target.SecuritySchemes) == 0 {
		return
	}

	for _, schemeName := range authSchemeNamesForRequest(target, cfg.AuthCredentials) {
		scheme, ok := target.SecuritySchemes[schemeName]
		if !ok {
			continue
		}
		credential, ok := cfg.AuthCredentials[schemeName]
		if !ok || credential == "" {
			continue
		}
		applySecurityScheme(req, scheme, credential)
	}
}

func authSchemeNamesForRequest(target Target, credentials map[string]string) []string {
	if target.SecurityRequirementSource != "" {
		for _, requirement := range target.SecurityRequirements {
			if len(requirement.SchemeNames) == 0 {
				continue
			}
			if securityRequirementSatisfied(requirement, target.SecuritySchemes, credentials) {
				return append([]string(nil), requirement.SchemeNames...)
			}
		}
		return nil
	}

	var names []string
	for name := range credentials {
		if _, ok := target.SecuritySchemes[name]; ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func securityRequirementSatisfied(requirement SecurityRequirement, schemes map[string]SecurityScheme, credentials map[string]string) bool {
	for _, name := range requirement.SchemeNames {
		if _, ok := schemes[name]; !ok {
			return false
		}
		if credentials[name] == "" {
			return false
		}
	}
	return true
}

func applySecurityScheme(req *http.Request, scheme SecurityScheme, credential string) {
	switch strings.ToLower(scheme.Type) {
	case "apikey":
		applyAPIKey(req, scheme, credential)
	case "http":
		applyHTTPSecurity(req, scheme, credential)
	case "oauth2", "openidconnect":
		setHeaderIfAbsent(req.Header, "Authorization", bearerHeaderValue(credential))
	}
}

func applyAPIKey(req *http.Request, scheme SecurityScheme, credential string) {
	switch strings.ToLower(scheme.In) {
	case "header":
		if scheme.ParamName != "" {
			setHeaderIfAbsent(req.Header, scheme.ParamName, credential)
		}
	case "query":
		if scheme.ParamName != "" {
			setQueryParamIfAbsent(req.URL, scheme.ParamName, credential)
		}
	case "cookie":
		if scheme.ParamName != "" {
			req.AddCookie(&http.Cookie{Name: scheme.ParamName, Value: credential})
		}
	}
}

func applyHTTPSecurity(req *http.Request, scheme SecurityScheme, credential string) {
	switch strings.ToLower(scheme.Scheme) {
	case "bearer":
		setHeaderIfAbsent(req.Header, "Authorization", bearerHeaderValue(credential))
	case "basic":
		setHeaderIfAbsent(req.Header, "Authorization", basicHeaderValue(credential))
	default:
		if scheme.Scheme != "" {
			setHeaderIfAbsent(req.Header, "Authorization", authHeaderValue(scheme.Scheme, credential))
		}
	}
}

func setHeaderIfAbsent(headers http.Header, name string, value string) {
	if headers.Get(name) != "" {
		return
	}
	headers.Set(name, value)
}

func setQueryParamIfAbsent(rawURL *url.URL, name string, value string) {
	if rawURL == nil {
		return
	}
	query := rawURL.Query()
	if _, exists := query[name]; exists {
		return
	}
	query.Set(name, value)
	rawURL.RawQuery = query.Encode()
}

func bearerHeaderValue(credential string) string {
	if strings.HasPrefix(strings.ToLower(credential), "bearer ") {
		return credential
	}
	return "Bearer " + credential
}

func basicHeaderValue(credential string) string {
	if strings.HasPrefix(strings.ToLower(credential), "basic ") {
		return credential
	}
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(credential))
}

func authHeaderValue(scheme string, credential string) string {
	if strings.HasPrefix(strings.ToLower(credential), strings.ToLower(scheme)+" ") {
		return credential
	}
	return scheme + " " + credential
}

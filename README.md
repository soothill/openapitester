# openapitester

`openapitester` is a Go command-line utility for investigating OpenAI API compatibility and testing HTTP endpoints against OpenAPI contracts.

**OpenAI compatibility and OpenAPI are different things.** To evaluate a company claiming an OpenAI-compatible API, use `-openai`. This runs actual inference API checks without requiring the provider to supply an OpenAPI spec. The original `-spec` and `-url` modes remain available for generic HTTP checks.

It can test a single endpoint against the standard HTTP operations used by OpenAPI, or it can read an OpenAPI 3.x document and exercise the operations defined under `paths`. During a run, it records response status codes, response content types, latency, transport failures, and any behavioral variances from the documented or configured expectations.

The tool is designed for quick endpoint smoke checks, repeatable contract checks in CI, and longer-running concurrency tests where you want a concise variance report at the end.

## Features

- Runs 15 named OpenAI compatibility checks with per-check outcomes and diagnostic evidence.
- Validates inference response JSON, chat streaming, usage counts, function calls, and structured output behavior.
- Separates failed checks from authentication/quota blocks, request rejections, unavailable routes, and inconclusive results.
- Keeps skipped and untested checks visible and retains the first variance for each target independently of request samples.
- Tests the standard OpenAPI HTTP operation set: `GET`, `PUT`, `POST`, `DELETE`, `OPTIONS`, `HEAD`, `PATCH`, and `TRACE`.
- Supports single-endpoint probing with configurable methods and fallback expected statuses.
- Supports OpenAPI 3.x JSON and YAML specs from local files or HTTP URLs.
- Discovers documented operations from `paths`.
- Optionally probes undocumented operations for every path with `-probe-undocumented`.
- Replaces required path parameters and required query parameters using provided or generated sample values.
- Reads OpenAPI `components.securitySchemes` and injects configured credentials with `-auth`.
- Supports OpenAPI `apiKey` auth in headers, query parameters, and cookies.
- Supports common HTTP auth schemes such as bearer and basic auth.
- Supports explicit global query parameters with `-query`.
- Generates simple request bodies from OpenAPI examples, named examples, or schemas.
- Sends concurrent requests with a configurable worker count.
- Runs for a fixed number of iterations or for an extended duration.
- Supports optional global request-start rate limiting.
- Captures per-target status counts, latency summaries, transport errors, and variance counts.
- Writes bounded JSON or Markdown reports for review, audit trails, and CI artifacts.
- Can exit non-zero when variances are found.

## Installation

Install the latest version directly with Go:

```sh
go install github.com/soothill/openapitester@latest
```

Or build from a local checkout:

```sh
git clone https://github.com/soothill/openapitester.git
cd openapitester
go build -o openapitester .
```

Run the local binary:

```sh
./openapitester -h
```

## Quick Start

Check a provider's OpenAI compatibility:

```sh
openapitester \
  -openai \
  -base-url https://provider.example/v1 \
  -model provider-chat-model \
  -api-key-env API_KEY \
  -timeout 60s \
  -report compatibility.md
```

`API_KEY` must already be set in the process environment. The utility reads it directly; the key value is not placed in command-line arguments. Use the exact API root and model ID supplied by the provider. The utility appends endpoint paths such as `/chat/completions`; it does not add `/v1` automatically.

Probe one endpoint with every standard method:

```sh
openapitester \
  -url https://api.example.com/widgets \
  -methods all \
  -concurrency 8 \
  -iterations 3 \
  -report report.md
```

Run from an OpenAPI document for 30 minutes:

```sh
openapitester \
  -spec openapi.yaml \
  -base-url https://api.example.com \
  -concurrency 20 \
  -duration 30m \
  -rate 50 \
  -report report.json
```

Use CI-friendly failure behavior:

```sh
openapitester \
  -spec openapi.yaml \
  -base-url https://staging-api.example.com \
  -auth BearerAuth="$API_TOKEN" \
  -fail-on-variance
```

## How It Works

`openapitester` builds a list of request targets, sends requests to those targets, evaluates each response, and aggregates the results.

There are three target discovery modes:

- OpenAI compatibility mode: `-openai` creates named inference API checks with predefined request fixtures and response validators.
- Single-endpoint mode: `-url` creates one target per selected HTTP method.
- OpenAPI mode: `-spec` reads an OpenAPI document and creates targets from documented path operations.

For every response, the tool checks:

- Whether an HTTP response was received at all.
- Whether the status code matches the expected status patterns.
- Whether the response `Content-Type` matches the documented OpenAPI response media type, when a media type is documented for the matching status.

Authentication is applied at request time. Credentials passed with `-auth` and `-query` are not added to the stored target URL, so generated reports record the scheme or query parameter names without serializing the secret values. Secrets embedded directly in `-url` are part of the target URL and may appear in reports.

Generic `-spec` mode does not validate full response body schemas. OpenAI compatibility mode additionally validates the response structures and behaviors described below.

## OpenAI Compatibility

The suite tests the interface exposed by a provider for the configured model and credentials. It does not certify a provider or measure general model intelligence. A successful HTTP response alone is insufficient: malformed JSON, incorrect fields, missing stream termination, ignored forced tool calls, and incorrect structured output produce variances.

### Checks

| Check | Request and validation |
| --- | --- |
| `models` | `GET /models`: list envelope, model metadata, and unique IDs. An empty list is structurally valid; discovery does not prove inference support. |
| `model` | `GET /models/{model}`: model metadata. |
| `chat` | Chat completion envelope, one indexed assistant choice, nonempty text, and completion reason. |
| `system` | System-message input and an exact-output instruction (`COMPAT_OK`). |
| `multi-turn` | User/assistant/user history and recall of a supplied word (`violet`). |
| `usage` | Nonnegative integer token counts and arithmetic consistency. Optional usage is also checked whenever returned by other chat checks. |
| `stream` | SSE framing, JSON chunks, stable completion identity, assistant role, content deltas, completion reason, and `[DONE]`. Records time to first text delta. |
| `stream-usage` | `stream_options.include_usage=true`: also requires a final usage chunk with empty choices. |
| `tools` | Forced `add` function call: ID, function name, JSON arguments `a=19,b=23`, and `tool_calls` completion reason. No code or external tool is executed. |
| `tool-result` | A predefined assistant tool-call/tool-result conversation, expecting the supplied result `42`. This checks acceptance and use of tool-result messages, not a live tool execution loop. |
| `json` | `response_format.type=json_object`: generated content must be a JSON object containing exactly `answer: 42`. |
| `structured` | Strict `json_schema` output: integer `answer=42`, required property, and no additional properties. |
| `responses` | `POST /responses` with `store=false`: response envelope, completed assistant text output, and usage consistency when present. |
| `embeddings` | Two-input batch: indexed numeric vectors of equal dimension and usage counts. Requires a separate `-embedding-model`; otherwise skipped. |
| `invalid-request` | Chat request without required `messages`: expects HTTP 400 and an error envelope with message/type and string-or-null param/code. |

The request and response contracts follow the official [Chat Completions reference](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create), [streaming reference](https://developers.openai.com/api/reference/resources/chat/subresources/completions/streaming-events), [Responses reference](https://platform.openai.com/docs/api-reference/responses/create), [embeddings reference](https://developers.openai.com/api/reference/resources/embeddings/methods/create), and [models reference](https://platform.openai.com/docs/api-reference/models). The implemented subset is explicitly listed above; optional API extensions are not rejected just because they are unfamiliar.

Select checks appropriate to the provider's claim:

```sh
openapitester \
  -openai -base-url https://provider.example/v1 \
  -model provider-chat-model -api-key-env API_KEY \
  -checks chat,system,multi-turn,usage,stream,tools,json,structured \
  -iterations 3 -concurrency 2 -timeout 60s \
  -fail-on-variance -report compatibility.json
```

Unselected checks remain in the report as skipped. Add `-embedding-model provider-embedding-model` to test embeddings; a chat model is not assumed to support them. Model aliases may resolve to canonical model IDs, so an exact response model-name match is not required.

For a longer run:

```sh
openapitester \
  -openai -base-url https://provider.example/v1 \
  -model provider-chat-model -api-key-env API_KEY \
  -checks chat,stream,tools,structured \
  -duration 1h -concurrency 4 -rate 1 -timeout 60s \
  -report compatibility-soak.md
```

Each selected check runs once per iteration or repeats until the duration expires. Inference requests may incur provider charges. `-max-tokens` defaults to 256 and bounds each generation request. Reasoning models may need a higher budget to produce visible output; truncation/refusal is reported as inconclusive. Chat requests use `max_completion_tokens` by default. To assess older compatibility implementations, explicitly select `-token-limit-field max_tokens`; the report records this choice. There is no silent fallback that could conceal a rejected parameter.

### Outcomes

| Outcome | Interpretation |
| --- | --- |
| `passed` | This request satisfied the check's implemented assertions. |
| `failed` | An observed response violated the expected structure or behavior. Behavioral failures are separately tagged `behavior_mismatch`. |
| `blocked` | HTTP 401/403 or 429 prevented checking capability. Verify credentials, permissions, quotas, and rate limits. |
| `unavailable` | HTTP 404/405/501: route or model is unavailable for this request. This does not prove a model lacks a capability. |
| `rejected` | HTTP 400/422 for a positive check: the request was rejected. The bounded provider error helps identify a parameter, model, or configuration mismatch. |
| `inconclusive` | Provider server error, transport/body failure, body limit, truncation, or refusal prevented a useful conclusion. |
| `skipped` | Not selected or missing a required model configuration. No request sent. |
| `not tested` | Selected but no request started before cancellation/deadline. |

There is deliberately no single compatibility percentage. A provider can pass chat and fail tools, or offer only Chat Completions and no Responses API. Repeated runs retain counts of every outcome rather than allowing a later success to erase a failure. `-fail-on-variance` exits 2 for any observed variance, including blocked or inconclusive attempts, or any selected target that was never tested. Skipped checks do not fail a run. A run stopped before any selected check starts is shown as not tested, never as passed.

Reports retain per-check outcome counts, reported model IDs, and the first variance even with `-max-samples 0`. Request samples additionally include `firstTokenMillis` for streams. Raw response bodies and authorization headers are not stored. Known credential values are redacted from diagnostic strings; a provider's error message can still contain unrelated sensitive information.

### Coverage Limits

These are bounded conformance smoke checks, not exhaustive certification. Exact-output checks also depend on model instruction-following. A model can produce compliant JSON from its prompt even if it ignores `response_format`; this suite checks observed output, not the provider's internal enforcement. Token-count arithmetic does not verify tokenization accuracy, and embedding shape does not establish embedding quality.

Not currently tested: vision/audio/video, Realtime/WebSockets, legacy Completions, Files/Batch/Fine-tuning, Responses streaming/state management, streamed or parallel tool calls, live tool execution round trips, logprobs, every sampling parameter, maximum context length, and all possible schema/parameter combinations. Run the same selected checks and token budget against each provider/model for a meaningful comparison. Generic `-spec` checks are complementary and do not replace these inference checks.

## Standard Operation Set

When `-methods all` is used, the following operations are tested:

```text
GET, PUT, POST, DELETE, OPTIONS, HEAD, PATCH, TRACE
```

You can test a smaller set:

```sh
openapitester -url https://api.example.com/widgets -methods GET,POST,PATCH
```

## Single-Endpoint Mode

Single-endpoint mode is useful when you want to quickly understand which methods an endpoint accepts and how it responds.

```sh
openapitester \
  -url https://api.example.com/widgets \
  -methods all \
  -expect-status 2XX,3XX
```

By default, single-endpoint mode expects `2XX` or `3XX` responses. Responses such as `405 Method Not Allowed` and `501 Not Implemented` are recorded as `method_not_supported`.

Override fallback status expectations:

```sh
openapitester \
  -url https://api.example.com/widgets/123 \
  -methods GET,DELETE \
  -expect-status 200,204,404
```

Send headers:

```sh
openapitester \
  -url https://api.example.com/widgets \
  -H "Authorization: Bearer $TOKEN" \
  -H "Accept: application/json"
```

Append a query parameter to every request:

```sh
openapitester \
  -url https://api.example.com/widgets \
  -query api_key="$API_KEY"
```

Send a request body for body-capable methods:

```sh
openapitester \
  -url https://api.example.com/widgets \
  -methods POST,PUT,PATCH \
  -body '{"name":"smoke-test"}' \
  -content-type application/json
```

Read the request body from a file:

```sh
openapitester \
  -url https://api.example.com/widgets \
  -methods POST \
  -body-file payload.json
```

Manual bodies are sent only for `POST`, `PUT`, `PATCH`, and `DELETE`.

## OpenAPI Spec Mode

OpenAPI mode is useful when you want to test behavior against the documented contract.

```sh
openapitester \
  -spec openapi.yaml \
  -base-url https://api.example.com
```

The spec can also be loaded from an HTTP URL:

```sh
openapitester \
  -spec https://api.example.com/openapi.yaml \
  -base-url https://api.example.com
```

When a spec is used, the tool:

- Reads the `paths` object.
- Creates request targets for documented operations that match `-methods`.
- Uses `servers[0].url` when `-base-url` is not provided.
- Replaces server variables from their defaults or `-param` overrides.
- Replaces path parameters such as `{id}` with values from `-param id=123` or generated samples.
- Adds required query parameters with values from `-param` overrides or generated samples.
- Uses OpenAPI operation `responses` as the expected response status set.
- Uses documented response `content` media types for content-type comparison.
- Uses operation-level `security` requirements, or root-level `security` requirements when an operation does not override them.
- Injects credentials supplied with `-auth` according to `components.securitySchemes`.
- Generates request bodies from media-type examples, named examples, or schemas when possible.

Provide parameter values:

```sh
openapitester \
  -spec openapi.yaml \
  -base-url https://api.example.com \
  -param tenantId=acme \
  -param widgetId=123 \
  -param verbose=true
```

Probe methods that are not documented for each path:

```sh
openapitester \
  -spec openapi.yaml \
  -base-url https://api.example.com \
  -probe-undocumented
```

Undocumented operation probes use fallback `-expect-status` patterns because the OpenAPI document does not provide operation-level responses for them.

## Authentication

There are three ways to pass security values to an endpoint:

- `-H` sends an explicit request header.
- `-query` appends an explicit query parameter to every request.
- `-auth` maps a credential to an OpenAPI security scheme name and lets the tool inject it in the documented place.

Use `-H` when you already know the header:

```sh
openapitester \
  -url https://api.example.com/widgets \
  -H "Authorization: Bearer $API_TOKEN"
```

Use `-query` for an arbitrary query-string key:

```sh
openapitester \
  -url https://api.example.com/widgets \
  -query api_key="$API_KEY"
```

Use `-auth` with OpenAPI specs:

```sh
openapitester \
  -spec openapi.yaml \
  -base-url https://api.example.com \
  -auth ApiKeyAuth="$API_KEY"
```

`-auth` uses the security scheme name, not the actual header or query parameter name. For example, this OpenAPI security scheme:

```yaml
components:
  securitySchemes:
    ApiKeyAuth:
      type: apiKey
      in: query
      name: api_key
security:
  - ApiKeyAuth: []
```

is invoked like this:

```sh
openapitester \
  -spec openapi.yaml \
  -base-url https://api.example.com \
  -auth ApiKeyAuth="$API_KEY"
```

The request will include `?api_key=...`, but the report will only record that `ApiKeyAuth` was configured.

Header API keys work the same way:

```yaml
components:
  securitySchemes:
    AdminKey:
      type: apiKey
      in: header
      name: x-admin-key
security:
  - AdminKey: []
```

```sh
openapitester \
  -spec openapi.yaml \
  -base-url https://api.example.com \
  -auth AdminKey="$ADMIN_KEY"
```

Bearer tokens use HTTP security schemes:

```yaml
components:
  securitySchemes:
    BearerAuth:
      type: http
      scheme: bearer
security:
  - BearerAuth: []
```

```sh
openapitester \
  -spec openapi.yaml \
  -base-url https://api.example.com \
  -auth BearerAuth="$API_TOKEN"
```

The tool sends `Authorization: Bearer ...`. If the credential already starts with `Bearer `, it is used as-is.

Basic auth accepts a `username:password` value:

```yaml
components:
  securitySchemes:
    BasicAuth:
      type: http
      scheme: basic
security:
  - BasicAuth: []
```

```sh
openapitester \
  -spec openapi.yaml \
  -base-url https://api.example.com \
  -auth BasicAuth="user:password"
```

OAuth2 and OpenID Connect schemes are treated as bearer-token credentials:

```sh
openapitester \
  -spec openapi.yaml \
  -base-url https://api.example.com \
  -auth OAuth2="$ACCESS_TOKEN"
```

When an OpenAPI operation offers multiple security alternatives, `openapitester` uses the first alternative for which all required `-auth` credentials were supplied. If the spec defines `components.securitySchemes` but does not attach root-level or operation-level `security` requirements, supplied `-auth` credentials for known schemes are applied to discovered targets. If an operation explicitly declares `security: []`, no OpenAPI-derived authentication is injected for that operation.

Explicit `-H` values are applied before `-auth`; OpenAPI-derived auth does not overwrite an existing header. Explicit `-query` values are applied before query-based `-auth`; OpenAPI-derived query auth does not overwrite an existing query parameter.

## Request Body Generation

In OpenAPI mode, the tool tries to generate a body when an operation has a `requestBody`.

Generation order:

1. CLI body from `-body` or `-body-file`.
2. Media type-level `example`.
3. First named example with a `value`.
4. A simple sample generated from the media type schema.

For schemas, the generator supports common OpenAPI/JSON Schema shapes:

- `type: object`
- `type: array`
- `type: string`
- `type: integer`
- `type: number`
- `type: boolean`
- `enum`
- `default`
- `example`
- `oneOf`
- `anyOf`
- `allOf`
- local `$ref` pointers

The generated body is intentionally simple. For workflows that require realistic domain data, provide explicit examples in the OpenAPI document or use `-body-file`.

## Long-Running Tests

Run each target once:

```sh
openapitester -spec openapi.yaml -base-url https://api.example.com
```

Run multiple passes over all targets:

```sh
openapitester \
  -spec openapi.yaml \
  -base-url https://api.example.com \
  -iterations 10
```

Run for a fixed duration:

```sh
openapitester \
  -spec openapi.yaml \
  -base-url https://api.example.com \
  -duration 2h \
  -concurrency 50
```

Throttle request starts globally:

```sh
openapitester \
  -spec openapi.yaml \
  -base-url https://api.example.com \
  -duration 10m \
  -rate 100
```

Progress is printed every 10 seconds by default:

```text
progress elapsed=30s requests=1472 variance_requests=3 transport_errors=0
```

Disable progress output:

```sh
openapitester \
  -url https://api.example.com/widgets \
  -progress 0
```

Stop a long run with `Ctrl+C`. Completed requests are still summarized.

Duration and cancellation also stop in-flight requests and rate-limit waits. A request interrupted while receiving a body is reported as `response_read_error`, so deadline-related errors should be distinguished from provider failures. Latency includes the response body; streaming latency ends at `[DONE]`. Redirects are not followed, preserving the status returned by the endpoint being tested. Bodies are limited to 4 MiB per request by default; increase `-max-response-bytes` for larger expected responses.

## Reports

The terminal report includes:

- Target count.
- Total request count.
- Requests with variances.
- Transport error count.
- Variance counts by type.
- Per-target latency summary.
- Per-target status counts.
- Sampled request-level variance details.

Write JSON:

```sh
openapitester \
  -spec openapi.yaml \
  -base-url https://api.example.com \
  -report report.json
```

Write Markdown:

```sh
openapitester \
  -spec openapi.yaml \
  -base-url https://api.example.com \
  -report report.md
```

Choose the report format explicitly:

```sh
openapitester \
  -spec openapi.yaml \
  -base-url https://api.example.com \
  -report results.out \
  -report-format json
```

Long runs can produce many results, so samples are bounded:

```sh
openapitester \
  -spec openapi.yaml \
  -base-url https://api.example.com \
  -duration 1h \
  -max-samples 500
```

By default, samples include only requests with variances. Include success samples too:

```sh
openapitester \
  -url https://api.example.com/health \
  -include-success-samples \
  -max-samples 100
```

## Variance Types

`transport_error`

: The request failed before an HTTP response was received. Examples include DNS errors, connection refusals, TLS failures, and request timeouts.

`status_mismatch`

: The endpoint returned a status code outside the expected status patterns.

`method_not_supported`

: The endpoint returned `405 Method Not Allowed` or `501 Not Implemented` when that status was outside the expected status patterns.

`missing_content_type`

: The OpenAPI response documents content media types, but the response did not include a `Content-Type` header.

`content_type_mismatch`

: The response `Content-Type` did not match the media types documented for the matching OpenAPI response.

`response_read_error`

: The response body could not be read completely, including truncation, timeout, or cancellation after headers arrived.

`response_too_large`

: The response exceeded `-max-response-bytes`; complete body validation was not possible.

OpenAI mode also reports specific contract/behavior variances such as `chat_shape`, `usage_total`, `tool_call_missing`, `structured_output_mismatch`, `stream_incomplete`, and `error_shape`, plus the outcome categories documented above.

## Status Patterns

`-expect-status` and OpenAPI response keys support these patterns:

| Pattern | Meaning | Example |
| --- | --- | --- |
| Exact status | One specific HTTP status | `200` |
| Status class | Any code in the class | `2XX` |
| Range | Inclusive status range | `200-299` |
| Default | Accept any status | `default` |

Examples:

```sh
openapitester -url https://api.example.com/health -expect-status 200
openapitester -url https://api.example.com/widgets -expect-status 200,201,204
openapitester -url https://api.example.com/widgets -expect-status 2XX,404
openapitester -url https://api.example.com/widgets -expect-status 200-299,304
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `-openai` | `false` | Run the OpenAI compatibility suite. Requires `-base-url` and `-model`; excludes `-url` and `-spec`. |
| `-model` | none | Provider's chat model ID for OpenAI mode. |
| `-embedding-model` | none | Separate embedding model ID; omitted embeddings check is skipped. |
| `-checks` | `all` | All 15 compatibility checks, or comma-separated names from the Checks table. |
| `-api-key-env` | none | Read a bearer key from this environment variable in OpenAI mode. Explicit `-H Authorization:...` takes precedence. |
| `-max-tokens` | `256` | Generation output budget in OpenAI mode. |
| `-token-limit-field` | `max_completion_tokens` | Chat budget parameter; accepts `max_completion_tokens` or `max_tokens`. |
| `-max-response-bytes` | `4194304` | Maximum response body bytes read per request. Oversized bodies produce a variance. |
| `-url` | none | Single endpoint URL to probe. Mutually exclusive with `-spec`. |
| `-spec` | none | OpenAPI 3.x JSON or YAML file/URL to exercise. Mutually exclusive with `-url`. |
| `-base-url` | spec server or `http://localhost` | Base URL override for specs; required API root (including `/v1` if needed) in OpenAI mode. |
| `-methods` | `all` | Comma-separated operations to test, or `all`. |
| `-H`, `-header` | none | Request header. Can be repeated. Example: `-H "Authorization: Bearer token"`. |
| `-param` | none | Path/query/server parameter value. Can be repeated. Example: `-param id=123`. |
| `-query` | none | Query parameter appended to every request. Can be repeated. Example: `-query api_key=secret`. |
| `-auth` | none | OpenAPI security credential by security scheme name. Can be repeated. Example: `-auth ApiKeyAuth=secret`. Requires `-spec`. |
| `-expect-status` | `2XX,3XX` | Fallback accepted statuses. Supports exact codes, classes, ranges, and `default`. |
| `-body` | none | Request body to send when no spec body is generated. |
| `-body-file` | none | File containing request body to send when no spec body is generated. |
| `-content-type` | `application/json` | Content-Type used with `-body` or `-body-file`. |
| `-concurrency` | `4` | Number of concurrent request workers. |
| `-duration` | `0` | Run duration. When set, targets repeat until time expires. |
| `-iterations` | `1` | Passes over all targets when `-duration` is not set. |
| `-rate` | `0` | Global request start rate per second. Zero means unthrottled. |
| `-timeout` | `10s` | Per-request timeout. |
| `-progress` | `10s` | Progress print interval during runs. Set `0` to disable. |
| `-report` | none | Write a JSON or Markdown report to this path. |
| `-report-format` | `auto` | Report format: `auto`, `json`, `markdown`, or `md`. |
| `-max-samples` | `1000` | Maximum sampled request results retained in the report. |
| `-include-success-samples` | `false` | Include successful request samples, not only variances. |
| `-fail-on-variance` | `false` | Exit with code `2` when variances are found. |
| `-insecure` | `false` | Skip TLS certificate verification. |
| `-probe-undocumented` | `false` | In spec mode, probe standard methods missing from a path item. |
| `-user-agent` | `openapitester/0.1` | User-Agent header when one is not supplied. |

## Exit Codes

| Code | Meaning |
| --- | --- |
| `0` | Run completed. No fatal tool error occurred. |
| `1` | CLI usage, spec loading, report writing, or execution setup failed. |
| `2` | `-fail-on-variance` was set and variances, transport errors, or selected targets that were never tested were found. |

## CI Example

Example GitHub Actions step:

```yaml
- name: Run OpenAPI endpoint contract smoke test
  run: |
    go install github.com/soothill/openapitester@latest
    openapitester \
      -spec openapi.yaml \
      -base-url "$API_BASE_URL" \
      -H "Authorization: Bearer $API_TOKEN" \
      -concurrency 10 \
      -iterations 3 \
      -fail-on-variance \
      -report openapitester-report.json
```

Store `openapitester-report.json` as a build artifact if you want historical variance reports.

## Safety Notes

`openapitester` sends real HTTP requests. Be careful when using mutating methods such as `POST`, `PUT`, `PATCH`, and `DELETE`.

Recommended practice:

- Prefer staging, development, or disposable environments.
- Use test tenants and test credentials.
- Provide safe fixture IDs with `-param`.
- Review generated request bodies before using the tool against stateful APIs.
- Use lower `-concurrency` and `-rate` values first.
- Avoid `-probe-undocumented` against production systems unless you have explicit approval.

## Current Limitations

- OpenAPI 3.x is supported; Swagger/OpenAPI 2.0 is not a primary target.
- Generic OpenAPI response body schema validation is not implemented; OpenAI mode validates its named inference contracts and behaviors.
- OAuth, browser login, token refresh, and other interactive auth flows are not automated.
- Request body generation is intentionally simple and may not satisfy business-specific validation.
- Remote `$ref` resolution is not implemented; local document `$ref` pointers are supported.
- Required headers and cookies from OpenAPI parameter definitions are not synthesized yet.
- The rate limiter controls request starts globally, not per target.

## Development

Run tests:

```sh
go test ./...
```

Run vet:

```sh
go vet ./...
```

Build:

```sh
go build -o openapitester .
```

Format:

```sh
gofmt -w *.go
```

## Project Status

This is an early multifunction utility. The first version focuses on endpoint operation coverage, concurrency, duration-based runs, authentication injection, and variance documentation. Good next additions would be response schema validation, per-operation data fixtures, and export formats for test management systems.

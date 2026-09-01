# openapitester

`openapitester` is a Go command-line utility for testing whether HTTP endpoints behave the way their OpenAPI contract says they should.

It can test a single endpoint against the standard HTTP operations used by OpenAPI, or it can read an OpenAPI 3.x document and exercise the operations defined under `paths`. During a run, it records response status codes, response content types, latency, transport failures, and any behavioral variances from the documented or configured expectations.

The tool is designed for quick endpoint smoke checks, repeatable contract checks in CI, and longer-running concurrency tests where you want a concise variance report at the end.

## Features

- Tests the standard OpenAPI HTTP operation set: `GET`, `PUT`, `POST`, `DELETE`, `OPTIONS`, `HEAD`, `PATCH`, and `TRACE`.
- Supports single-endpoint probing with configurable methods and fallback expected statuses.
- Supports OpenAPI 3.x JSON and YAML specs from local files or HTTP URLs.
- Discovers documented operations from `paths`.
- Optionally probes undocumented operations for every path with `-probe-undocumented`.
- Replaces required path parameters and required query parameters using provided or generated sample values.
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
  -fail-on-variance
```

## How It Works

`openapitester` builds a list of request targets, sends requests to those targets, evaluates each response, and aggregates the results.

There are two target discovery modes:

- Single-endpoint mode: `-url` creates one target per selected HTTP method.
- OpenAPI mode: `-spec` reads an OpenAPI document and creates targets from documented path operations.

For every response, the tool checks:

- Whether an HTTP response was received at all.
- Whether the status code matches the expected status patterns.
- Whether the response `Content-Type` matches the documented OpenAPI response media type, when a media type is documented for the matching status.

The tool does not attempt to validate full response schemas yet. Its current focus is broad endpoint operation coverage, availability, documented status behavior, content type behavior, concurrency, and longer-run variance capture.

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
| `-url` | none | Single endpoint URL to probe. Mutually exclusive with `-spec`. |
| `-spec` | none | OpenAPI 3.x JSON or YAML file/URL to exercise. Mutually exclusive with `-url`. |
| `-base-url` | spec server or `http://localhost` | Base URL override for OpenAPI specs. |
| `-methods` | `all` | Comma-separated operations to test, or `all`. |
| `-H`, `-header` | none | Request header. Can be repeated. Example: `-H "Authorization: Bearer token"`. |
| `-param` | none | Path/query/server parameter value. Can be repeated. Example: `-param id=123`. |
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
| `2` | Run completed and `-fail-on-variance` was set, but variances or transport errors were found. |

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
- Response body schema validation is not implemented yet.
- Authentication is supplied through headers; OAuth or other auth flows are not automated.
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

This is an early multifunction utility. The first version focuses on endpoint operation coverage, concurrency, duration-based runs, and variance documentation. Good next additions would be response schema validation, richer auth helpers, per-operation data fixtures, and export formats for test management systems.

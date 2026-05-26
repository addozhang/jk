// Package output renders schema values as YAML, JSON, or raw bytes.
//
// Every command funnels its response through [Render]. The rendering layer is
// the single place that:
//
//   - selects the output format (yaml | json | raw),
//   - injects the top-level schemaVersion key (yaml and json only — never
//     raw), so each value type doesn't have to remember to include it,
//   - guarantees that yaml and json encodings of the same value decode to
//     equivalent maps (the "identical output" guarantee from
//     openspec/changes/init-jk-jenkins-cli/specs/output/spec.md).
//
// Format conventions:
//
//   - "" or "yaml": YAML document, schemaVersion first line, trailing newline.
//   - "json": compact JSON object, schemaVersion is the first key.
//   - "raw": v MUST be []byte; returned verbatim. schemaVersion is NOT
//     injected. This is the escape hatch for callers that already hold the
//     bytes they want to emit (e.g. `jk build logs`, or `-o raw` passthrough
//     of a Jenkins API response).
package output

import (
	"bytes"
	"encoding/json"
	"fmt"

	"sigs.k8s.io/yaml"
)

// schemaVersion is the current major version of the jk output schema.
// Documented in docs/schema.md §1. Bumping requires an OpenSpec change.
const schemaVersion = "1"

// FormatYAML, FormatJSON, FormatRaw are the canonical names accepted by
// [Render]. The empty string is treated as FormatYAML.
const (
	FormatYAML = "yaml"
	FormatJSON = "json"
	FormatRaw  = "raw"
)

// Render encodes v according to format. See the package doc for semantics.
//
// For FormatRaw, v MUST be []byte; any other type is rejected with an error
// (callers can always pre-marshal to bytes and use raw if they need it).
//
// For FormatYAML and FormatJSON, v is first marshaled via encoding/json
// (sigs.k8s.io/yaml routes YAML through JSON, so a single set of `json:`
// struct tags drives both encodings — this is what guarantees the two
// encodings stay in sync).
func Render(v any, format string) ([]byte, error) {
	switch format {
	case "", FormatYAML:
		return renderYAML(v)
	case FormatJSON:
		return renderJSON(v)
	case FormatRaw:
		return renderRaw(v)
	default:
		return nil, fmt.Errorf("unknown output format %q (want one of: yaml, json, raw)", format)
	}
}

// renderJSON marshals v to compact JSON and prepends the schemaVersion key
// so it is the first member of the top-level object. The injection is done
// at the byte level (after the opening `{`) to preserve the field order of
// the marshaled struct; using a map would lose ordering.
func renderJSON(v any) ([]byte, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal json: %w", err)
	}
	return injectJSONSchemaVersion(body)
}

// renderYAML marshals v to YAML via sigs.k8s.io/yaml (which goes through
// JSON internally, so json tags drive the field names) and prepends a
// `schemaVersion: "1"` line so it appears first in the document. Prepending
// is safe because the document is always a mapping at the top level — none
// of our schema types are slices or scalars at the root.
func renderYAML(v any) ([]byte, error) {
	body, err := yaml.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal yaml: %w", err)
	}
	// yaml.Marshal always terminates with a newline; the prepended line
	// adds another so the result is `schemaVersion: "1"\n<body>\n`.
	out := make([]byte, 0, len(body)+24)
	out = append(out, []byte(`schemaVersion: "`+schemaVersion+"\"\n")...)
	out = append(out, body...)
	return out, nil
}

// renderRaw asserts that v is []byte and returns it untouched. schemaVersion
// is never injected for raw output (see the package doc).
func renderRaw(v any) ([]byte, error) {
	b, ok := v.([]byte)
	if !ok {
		return nil, fmt.Errorf("raw output requires []byte, got %T", v)
	}
	return b, nil
}

// injectJSONSchemaVersion inserts `"schemaVersion":"1",` immediately after
// the opening `{` of the top-level JSON object. The input MUST be a JSON
// object (jk schema types are always objects at the root); arrays, scalars,
// and `null` are rejected because they cannot carry a schemaVersion.
func injectJSONSchemaVersion(body []byte) ([]byte, error) {
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, fmt.Errorf("cannot inject schemaVersion: top-level value is not a JSON object")
	}
	prefix := []byte(`{"schemaVersion":"` + schemaVersion + `"`)
	// Empty object `{}` becomes `{"schemaVersion":"1"}`; non-empty objects
	// get a comma separator before the original first key.
	if bytes.Equal(trimmed, []byte("{}")) {
		return append(prefix, '}'), nil
	}
	out := make([]byte, 0, len(prefix)+len(trimmed))
	out = append(out, prefix...)
	out = append(out, ',')
	out = append(out, trimmed[1:]...) // skip the original '{'
	return out, nil
}

package schema

// This file owns the Jenkins-JSON -> schema.* mapping for build-level
// responses (status, stages, pending input). Mappers are pure
// functions of []byte: they do no IO, take no context, and emit a
// fully-populated schema struct (or a wrapped json error).
//
// Design notes (see openspec/changes/init-jk-jenkins-cli/design.md §D7
// and tasks.md §12):
//
//   - Build state is *derived*, not read directly. Jenkins's api/json
//     surfaces `building` (bool) and `result` (string|null); we
//     combine those with the presence of an InputAction in `actions[]`
//     to compute one of QUEUED / RUNNING / PENDING_INPUT / DONE.
//     QUEUED is not produced here (no build number yet means the
//     caller has the queue response, not the build response); that
//     state is synthesized by the trigger/watch command.
//
//   - Jenkins reports milliseconds-since-epoch; the schema wants
//     RFC 3339 UTC strings. We convert via time.UnixMilli(...).UTC()
//     so the encoder emits the trailing `Z`.
//
//   - estimatedDuration of -1 means "Jenkins has no historical
//     baseline yet". We surface that as null and never divide by it.
//
//   - wfapi stage statuses use verb forms ("SUCCESS", "FAILED",
//     "IN_PROGRESS", "PAUSED_PENDING_INPUT") that we normalize to the
//     StageStatus enum. Unknown values fall through verbatim so
//     consumers can still see them (per schema §4 "unknown values
//     SHOULD be rendered as-is").
//
//   - Duplicate top-level stage names are disambiguated by suffixing
//     "#N" on every occurrence (including the first). This matches the
//     contract in docs/schema.md §3.8 and avoids the ambiguity of
//     "which one is the unsuffixed Deploy?". Scope is the immediate
//     slice the caller is processing; nested parallel branches are
//     disambiguated independently within their own parent.

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// MapBuildStatus converts a build's `api/json` response into a
// schema.BuildStatus. Computes BuildState from (building,
// actions[]._class containing InputAction) where `building == false`
// short-circuits to DONE regardless of any historical InputAction
// marker (Jenkins keeps those in actions[] after submission); and
// ProgressPercent from (durationMs / estimatedDurationMs) clamped to
// [0,100].
//
// The mapper does NOT populate BuildStatus.PendingInput from core
// JSON: core's actions[] only carries the `_class` discriminator for
// the input step, never the id/message/ok/parameters fields. Those
// live on /wfapi/pendingInputActions and are mapped by MapPendingInput.
// The CLI layer is responsible for making the secondary fetch when
// HasPendingInputMarker reports true on a still-building build, and
// assigning the result into BuildStatus.PendingInput. See openspec
// change add-input-parameter-submission Decisions 6 and 7.
func MapBuildStatus(raw []byte) (BuildStatus, error) {
	var src struct {
		Number            int          `json:"number"`
		URL               string       `json:"url"`
		QueueID           *int         `json:"queueId"`
		Result            *BuildResult `json:"result"`
		Building          bool         `json:"building"`
		TimestampMs       int64        `json:"timestamp"`
		DurationMs        int64        `json:"duration"`
		EstimatedDuration int64        `json:"estimatedDuration"`
		Actions           []struct {
			Class string `json:"_class"`
		} `json:"actions"`
	}
	if err := json.Unmarshal(raw, &src); err != nil {
		return BuildStatus{}, fmt.Errorf("MapBuildStatus: %w", err)
	}

	out := BuildStatus{
		BuildURL:     src.URL,
		BuildNumber:  src.Number,
		QueueID:      src.QueueID,
		Result:       src.Result,
		Building:     src.Building,
		TimestampUtc: msToRFC3339UTC(src.TimestampMs),
		DurationMs:   src.DurationMs,
	}

	// estimatedDuration of -1 (or any non-positive) means
	// "unavailable" per Jenkins core; surface as nil and skip the
	// progress calculation entirely.
	if src.EstimatedDuration > 0 {
		est := int(src.EstimatedDuration)
		out.EstimatedDurationMs = &est
	}

	hasMarker := false
	for _, a := range src.Actions {
		if strings.Contains(a.Class, "InputAction") {
			hasMarker = true
			break
		}
	}

	// Derive state. Order matters: building==false ALWAYS wins (a
	// finished build cannot be pending anything, even if Jenkins
	// left the historical InputAction marker in actions[]). Only
	// when the build is still in flight does the marker promote
	// RUNNING to PENDING_INPUT.
	switch {
	case !src.Building:
		out.State = BuildStateDone
	case hasMarker:
		out.State = BuildStatePendingInput
	default:
		out.State = BuildStateRunning
	}

	// ProgressPercent: 100 once DONE, otherwise computed and clamped
	// to [0,100]. When estimate is unavailable, leave at 0 (zero value).
	switch {
	case out.State == BuildStateDone:
		out.ProgressPercent = 100
	case out.EstimatedDurationMs != nil && *out.EstimatedDurationMs > 0:
		pct := int(100 * src.DurationMs / int64(*out.EstimatedDurationMs))
		if pct > 100 {
			pct = 100
		}
		if pct < 0 {
			pct = 0
		}
		out.ProgressPercent = pct
	}

	return out, nil
}

// HasPendingInputMarker reports whether the core /api/json response
// for a build carries an InputAction entry in its actions[] array.
// This is the *presence bit* only — the populated fields
// (id/message/ok/parameters) only live on /wfapi/pendingInputActions.
// Callers use this to decide whether to make the wfapi enrichment
// call. Returns an error only for malformed JSON.
func HasPendingInputMarker(raw []byte) (bool, error) {
	var src struct {
		Actions []struct {
			Class string `json:"_class"`
		} `json:"actions"`
	}
	if err := json.Unmarshal(raw, &src); err != nil {
		return false, fmt.Errorf("HasPendingInputMarker: %w", err)
	}
	for _, a := range src.Actions {
		if strings.Contains(a.Class, "InputAction") {
			return true, nil
		}
	}
	return false, nil
}

// msToRFC3339UTC converts a Jenkins millisecond-since-epoch timestamp
// to an RFC 3339 UTC string. The zero value is preserved as
// "1970-01-01T00:00:00Z" (callers checking for "not yet" must guard
// at the source struct, not on this string).
func msToRFC3339UTC(ms int64) string {
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

// rawStage mirrors the wfapi `describe` shape for a single stage.
// Pulled into its own type so MapBuildStages can recurse via the
// Parallel field.
type rawStage struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Status          string     `json:"status"`
	StartTimeMillis int64      `json:"startTimeMillis"`
	DurationMillis  int64      `json:"durationMillis"`
	Parallel        []rawStage `json:"parallel"`
}

// MapBuildStages converts a wfapi `runs/{id}/describe` response into
// a schema.BuildStages. Handles sequential, parallel, and duplicate-
// name cases per docs/schema.md §3.8.
//
// Stability: experimental — fixtures are derived from the
// pipeline-stage-view-plugin README and will be re-verified against
// real samples once spike 1.1 lands.
func MapBuildStages(raw []byte) (BuildStages, error) {
	var src struct {
		Stages []rawStage `json:"stages"`
	}
	if err := json.Unmarshal(raw, &src); err != nil {
		return BuildStages{}, fmt.Errorf("MapBuildStages: %w", err)
	}
	return BuildStages{Stages: convertStages(src.Stages)}, nil
}

// convertStages turns a slice of rawStage into schema.Stage values,
// disambiguating duplicate names within this slice. Recurses into
// each stage's Parallel children (each parallel group is its own
// disambiguation scope).
func convertStages(in []rawStage) []Stage {
	out := make([]Stage, 0, len(in))

	// First pass: count name occurrences so we know which need
	// suffixing.
	counts := make(map[string]int, len(in))
	for _, s := range in {
		counts[s.Name]++
	}

	// Second pass: convert + assign suffixes per-occurrence.
	seen := make(map[string]int, len(in))
	for _, s := range in {
		stage := Stage{
			Name:   s.Name,
			Status: normalizeStageStatus(s.Status),
		}
		if counts[s.Name] > 1 {
			seen[s.Name]++
			stage.DisplayName = fmt.Sprintf("%s #%d", s.Name, seen[s.Name])
		} else {
			stage.DisplayName = s.Name
		}
		if s.StartTimeMillis > 0 {
			t := msToRFC3339UTC(s.StartTimeMillis)
			stage.StartTimeUtc = &t
		}
		if s.DurationMillis > 0 {
			d := s.DurationMillis
			stage.DurationMs = &d
		}
		if len(s.Parallel) > 0 {
			stage.Parallel = convertStages(s.Parallel)
		}
		out = append(out, stage)
	}
	return out
}

// normalizeStageStatus maps wfapi verb forms onto the StageStatus
// enum. Unknown values are returned verbatim so consumers can still
// see what Jenkins reported (per docs/schema.md §4).
func normalizeStageStatus(s string) StageStatus {
	switch strings.ToUpper(s) {
	case "SUCCESS":
		return StageStatusSuccess
	case "FAILED", "FAILURE":
		return StageStatusFailure
	case "ABORTED":
		return StageStatusAborted
	case "UNSTABLE":
		return StageStatusUnstable
	case "IN_PROGRESS", "RUNNING":
		return StageStatusRunning
	case "NOT_EXECUTED", "SKIPPED":
		return StageStatusNotExecuted
	case "PAUSED_PENDING_INPUT":
		return StageStatusPausedPendingInput
	case "QUEUED":
		return StageStatusQueued
	default:
		return StageStatus(s)
	}
}

// MapPendingInput converts a wfapi `pendingInputActions` response
// into a *schema.PendingInput. Returns nil + nil error when no inputs
// are pending; this lets callers emit `pendingInput: null` without
// wrapping in an error path.
//
// The wfapi shape differs from /api/json `parameterDefinitions`:
//   - top-level uses `proceedText` rather than `ok`,
//   - each input has `type` (simple class) but no `_class`,
//   - defaults and choices live under a nested `definition` object,
//     with the field name `defaultVal` (not `defaultParameterValue`).
//
// This was captured from a live Jenkins instance running the
// deploy-input harness pipeline; see
// Test_MapPendingInput_RealWfapiShape for the reference fixture.
//
// Stability: stable (consumed by `jk build input -p` validation).
func MapPendingInput(raw []byte) (*PendingInput, error) {
	var src []struct {
		ID          string               `json:"id"`
		ProceedText string               `json:"proceedText"`
		Message     string               `json:"message"`
		Inputs      []rawWfapiInputParam `json:"inputs"`
	}
	if err := json.Unmarshal(raw, &src); err != nil {
		return nil, fmt.Errorf("MapPendingInput: %w", err)
	}
	if len(src) == 0 {
		return nil, nil //nolint:nilnil // documented contract: nil means "no pending input"
	}
	first := src[0]
	params := make([]Parameter, 0, len(first.Inputs))
	for _, def := range first.Inputs {
		params = append(params, def.toSchema())
	}
	return &PendingInput{
		ID:         first.ID,
		Message:    first.Message,
		OK:         first.ProceedText,
		Parameters: params,
	}, nil
}

// rawWfapiInputParam mirrors the shape Jenkins returns under
// `/wfapi/pendingInputActions[].inputs[]`. Unlike rawParameterDefinition
// (which mirrors /api/json `parameterDefinitions`), wfapi nests the
// default value and choices inside a `definition` object.
type rawWfapiInputParam struct {
	Type        string  `json:"type"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	Definition  *struct {
		DefaultVal any      `json:"defaultVal"`
		Choices    []string `json:"choices"`
	} `json:"definition"`
}

func (d rawWfapiInputParam) toSchema() Parameter {
	p := Parameter{
		Name:        d.Name,
		Type:        classifyParameterType(d.Type, ""),
		Description: d.Description,
	}
	if d.Definition != nil {
		if d.Definition.DefaultVal != nil {
			p.Default = d.Definition.DefaultVal
		}
		// Choices only meaningful for CHOICE; leave nil for everything
		// else so JSON renders as `null` rather than `[]`.
		if p.Type == ParameterTypeChoice && d.Definition.Choices != nil {
			p.Choices = d.Definition.Choices
		}
	}
	return p
}

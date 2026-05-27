// Package schema defines the public output types emitted by every jk command.
//
// These types are the wire contract documented in docs/schema.md. They are
// marshaled as both JSON (via encoding/json) and YAML (via sigs.k8s.io/yaml,
// which routes through json tags). Field names, casing, and nullability MUST
// match docs/schema.md §3 exactly; any change requires an OpenSpec change per
// SPEC.md §Schema Review Workflow.
//
// Conventions enforced by this package:
//
//   - All field tags use `json:"name"` only. sigs.k8s.io/yaml honors json
//     tags, so yaml and json output are guaranteed identical.
//   - No `omitempty`: nullable fields use pointer types and marshal to
//     explicit `null`. The output spec requires explicit nulls so that
//     consumers can distinguish "absent" from "empty".
//   - Enums are typed string constants. Unknown values from a future Jenkins
//     server are rendered verbatim by the marshaler; consumers MUST handle
//     unknown enum values gracefully (see docs/schema.md §4).
//   - Collection fields (slices) are emitted as `[]` when empty, never as
//     `null`. Callers MUST initialize slices to a non-nil empty value before
//     marshaling.
//
// TODO(tasks 10.5): add a compile-time / generation-time check that every
// exported field is referenced by exactly one CLI command. Deferred until the
// mappers package exists so the cross-reference has both sides to verify.
package schema

// ---------------------------------------------------------------------------
// Enums
// ---------------------------------------------------------------------------

// BuildResult is the terminal outcome of a build. See docs/schema.md §4.
// Stability: stable.
type BuildResult string

// BuildResult values. See docs/schema.md §4.
const (
	BuildResultSuccess  BuildResult = "SUCCESS"
	BuildResultFailure  BuildResult = "FAILURE"
	BuildResultUnstable BuildResult = "UNSTABLE"
	BuildResultAborted  BuildResult = "ABORTED"
	BuildResultNotBuilt BuildResult = "NOT_BUILT"
)

// BuildState is the lifecycle state of a build. Unlike BuildResult this is
// always non-null. See docs/schema.md §4. Stability: stable.
type BuildState string

// BuildState values. See docs/schema.md §4.
const (
	BuildStateQueued       BuildState = "QUEUED"
	BuildStateRunning      BuildState = "RUNNING"
	BuildStatePendingInput BuildState = "PENDING_INPUT"
	BuildStateDone         BuildState = "DONE"
)

// ParameterType classifies a pipeline parameter definition. See
// docs/schema.md §3.4. UNKNOWN is emitted for parameter classes jk does not
// recognize (typically plugin-specific). Stability: stable.
type ParameterType string

// ParameterType values. See docs/schema.md §3.4.
const (
	ParameterTypeString   ParameterType = "STRING"
	ParameterTypeBoolean  ParameterType = "BOOLEAN"
	ParameterTypeChoice   ParameterType = "CHOICE"
	ParameterTypeText     ParameterType = "TEXT"
	ParameterTypePassword ParameterType = "PASSWORD"
	ParameterTypeUnknown  ParameterType = "UNKNOWN"
)

// ItemType distinguishes pipelines from folders in `jk pipeline list`. See
// docs/schema.md §3.5. Multibranch pipelines are reported as FOLDER because
// their children are branch jobs. Stability: stable.
type ItemType string

// ItemType values. See docs/schema.md §3.5.
const (
	ItemTypePipeline ItemType = "PIPELINE"
	ItemTypeFolder   ItemType = "FOLDER"
)

// StageStatus is the status of a pipeline stage. See docs/schema.md §3.8.
// Stability: experimental — entire stages schema is pending spike 1.1.
type StageStatus string

// StageStatus values. See docs/schema.md §3.8.
const (
	StageStatusSuccess            StageStatus = "SUCCESS"
	StageStatusFailure            StageStatus = "FAILURE"
	StageStatusAborted            StageStatus = "ABORTED"
	StageStatusUnstable           StageStatus = "UNSTABLE"
	StageStatusRunning            StageStatus = "RUNNING"
	StageStatusNotExecuted        StageStatus = "NOT_EXECUTED"
	StageStatusPausedPendingInput StageStatus = "PAUSED_PENDING_INPUT"
	StageStatusQueued             StageStatus = "QUEUED"
)

// InputAction is the action submitted to a paused pipeline input step. See
// docs/schema.md §3.9. Stability: experimental.
type InputAction string

// InputAction values. See docs/schema.md §3.9.
const (
	InputActionProceed InputAction = "PROCEED"
	InputActionAbort   InputAction = "ABORT"
)

// ---------------------------------------------------------------------------
// Auth (docs/schema.md §3.1)
// ---------------------------------------------------------------------------

// AuthList is the response of `jk auth list`. Stability: stable.
type AuthList struct {
	// Hosts is the array of configured host URLs in `scheme://host[:port]`
	// form, in insertion order. Empty array when no credentials are
	// configured; never null.
	Hosts []string `json:"hosts"`
}

// ---------------------------------------------------------------------------
// Pipeline info (docs/schema.md §3.3)
// ---------------------------------------------------------------------------

// PipelineInfo is the response of `jk pipeline info`. Stability: stable
// (Branches field is experimental).
type PipelineInfo struct {
	// Name is the pipeline's short name (the last `/job/<name>` segment).
	Name string `json:"name"`
	// FullName is the slash-joined folder path plus name, e.g.
	// `team/platform/svc`.
	FullName string `json:"fullName"`
	// URL is the canonical Jenkins URL of the pipeline.
	URL string `json:"url"`
	// Description is the user-provided description; null when unset.
	Description *string `json:"description"`
	// Buildable reports whether new builds can be triggered.
	Buildable bool `json:"buildable"`
	// LastBuild is the most recent build, or null if the pipeline has never
	// run.
	LastBuild *BuildRef `json:"lastBuild"`
	// Branches lists branches for multibranch pipelines; null for
	// single-branch pipelines. Stability: experimental.
	Branches []BranchRef `json:"branches"`
}

// BuildRef is a lightweight reference to a build. Stability: stable.
type BuildRef struct {
	// Number is the Jenkins build number.
	Number int `json:"number"`
	// URL is the canonical Jenkins URL of the build.
	URL string `json:"url"`
	// Result is the terminal outcome; null while the build is running.
	Result *BuildResult `json:"result"`
}

// BranchRef is a lightweight reference to a branch within a multibranch
// pipeline. Stability: experimental.
type BranchRef struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// ---------------------------------------------------------------------------
// Pipeline params (docs/schema.md §3.4)
// ---------------------------------------------------------------------------

// PipelineParams is the response of `jk pipeline params`. Stability: stable.
type PipelineParams struct {
	// Parameters is the parameter definitions in Jenkins-declared order.
	// Empty array if none; never null.
	Parameters []Parameter `json:"parameters"`
}

// Parameter is a single pipeline parameter definition. Stability: stable.
type Parameter struct {
	// Name is the parameter name.
	Name string `json:"name"`
	// Type classifies the parameter; UNKNOWN for unrecognized plugin types.
	Type ParameterType `json:"type"`
	// Default is the parameter's default value; null when there is no
	// default. Per docs/schema.md §3.4 this is `string | boolean | null`,
	// modeled as `any` so the marshaler emits the natural JSON shape.
	Default any `json:"default"`
	// Description is the parameter description; null if unset.
	Description *string `json:"description"`
	// Choices lists allowed values; non-null only when Type == CHOICE.
	Choices []string `json:"choices"`
}

// ---------------------------------------------------------------------------
// Pipeline list (docs/schema.md §3.5)
// ---------------------------------------------------------------------------

// PipelineList is the response of `jk pipeline list`. Stability: stable.
type PipelineList struct {
	// Items lists child pipelines and sub-folders in Jenkins's natural
	// order. Empty array when the folder is empty; never null.
	Items []Item `json:"items"`
}

// Item is a single child of a folder. Stability: stable.
type Item struct {
	Name string   `json:"name"`
	Type ItemType `json:"type"`
	URL  string   `json:"url"`
	// LastBuild is null for folders and never-built pipelines.
	LastBuild *BuildRef `json:"lastBuild"`
}

// ---------------------------------------------------------------------------
// Build trigger (docs/schema.md §3.6)
// ---------------------------------------------------------------------------

// BuildTrigger is the response of `jk build trigger` (without --watch).
// Stability: stable.
type BuildTrigger struct {
	// QueueID is the Jenkins queue item ID.
	QueueID int `json:"queueId"`
	// BuildURL is the URL of the created build; null if the queue item has
	// not yet been assigned a build.
	BuildURL *string `json:"buildUrl"`
	// BuildNumber is the build number; null until the queue item is
	// assigned.
	BuildNumber *int `json:"buildNumber"`
}

// ---------------------------------------------------------------------------
// Build status (docs/schema.md §3.7)
// ---------------------------------------------------------------------------

// BuildStatus is the response of `jk build status`. Stability: stable
// (PendingInput is experimental).
type BuildStatus struct {
	// BuildURL is the canonical Jenkins URL of the build.
	BuildURL string `json:"buildUrl"`
	// BuildNumber is the build number.
	BuildNumber int `json:"buildNumber"`
	// QueueID is the originating queue item, when known; null otherwise.
	QueueID *int `json:"queueId"`
	// Result is the final result; null while building.
	Result *BuildResult `json:"result"`
	// State is the lifecycle state (always non-null).
	State BuildState `json:"state"`
	// Building is true iff Jenkins reports the build as in-progress.
	Building bool `json:"building"`
	// TimestampUtc is the build start time as RFC 3339 UTC.
	TimestampUtc string `json:"timestampUtc"`
	// DurationMs is elapsed time so far for running builds, or final
	// duration for completed builds.
	DurationMs int64 `json:"durationMs"`
	// EstimatedDurationMs is Jenkins's estimate from historical runs; null
	// if unavailable.
	EstimatedDurationMs *int `json:"estimatedDurationMs"`
	// ProgressPercent is 0-100, computed as
	// `min(100, 100 * durationMs / estimatedDurationMs)`; equals 100 once
	// State == DONE.
	ProgressPercent int `json:"progressPercent"`
	// PendingInput is non-null iff State == PENDING_INPUT. Stability:
	// experimental.
	PendingInput *PendingInput `json:"pendingInput"`
}

// PendingInput describes a paused pipeline input step. Stability:
// experimental, EXCEPT `Parameters` which is stable as of v0.2.
type PendingInput struct {
	// ID is the input step identifier.
	ID string `json:"id"`
	// Message is the prompt presented to humans.
	Message string `json:"message"`
	// OK is the label of the "proceed" button.
	OK string `json:"ok"`
	// Parameters are the input-step parameters (same shape as §3.4).
	// Stability: stable — consumed by `jk build input -p` validation
	// and the v0.2 parameterized submit endpoint.
	Parameters []Parameter `json:"parameters"`
}

// ---------------------------------------------------------------------------
// Build stages (docs/schema.md §3.8)
// ---------------------------------------------------------------------------

// BuildStages is the response of `jk build stages`. Stability: experimental
// — entire schema pending spike 1.1.
type BuildStages struct {
	Stages []Stage `json:"stages"`
}

// Stage is a single pipeline stage. Stability: experimental.
type Stage struct {
	// Name is the stage's declared name.
	Name string `json:"name"`
	// DisplayName equals Name unless duplicated within the run, in which
	// case it carries a "#1", "#2", … suffix.
	DisplayName string `json:"displayName"`
	// Status is the stage status.
	Status StageStatus `json:"status"`
	// StartTimeUtc is RFC 3339 UTC; null if the stage has not started.
	StartTimeUtc *string `json:"startTimeUtc"`
	// DurationMs is the stage's duration in milliseconds; null if the
	// stage has not finished.
	DurationMs *int64 `json:"durationMs"`
	// Parallel lists child stages running in parallel under this stage;
	// null for sequential stages.
	Parallel []Stage `json:"parallel"`
}

// ---------------------------------------------------------------------------
// Build input result (docs/schema.md §3.9)
// ---------------------------------------------------------------------------

// BuildInputResult is the response of `jk build input`. Stability: stable
// (InputID + Action are experimental).
type BuildInputResult struct {
	// InputID is the ID of the input that was responded to.
	InputID string `json:"inputId"`
	// Action is the action submitted.
	Action InputAction `json:"action"`
	// BuildURL is the URL of the build that received the input.
	BuildURL string `json:"buildUrl"`
	// State is the build state immediately after submission.
	State BuildState `json:"state"`
}

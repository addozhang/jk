package cli

// build.go wires the `jk build {trigger,status,stages,input,logs}`
// subcommands. Each subcommand follows the same four-step pattern used
// by pipeline.go (parse URL → client call → mapper → render); the
// extra mechanics live in dedicated helpers:
//
//   - parseParamFlags (common.go)            — shared -p KEY=VALUE parser.
//   - extractQueueID                         — pulls the numeric queue ID
//                                              out of the Location URL
//                                              returned by TriggerBuild.
//   - watchBuild                             — adaptive 2s → 10s poller
//                                              used by `--watch`; emits
//                                              progress lines to stderr
//                                              and returns a
//                                              BuildResultExitError so
//                                              main()'s ExitCode sees
//                                              the per-result code.
//
// The build commands intentionally accept either a pipeline URL (for
// `trigger`) or a build URL (for `status`, `stages`, `input`, `logs`).
// `jenkinsurl.Parse` carries the optional build number through; the
// `requireBuildRef` helper rejects build commands invoked against a URL
// that lacks one with a clear "append the build number" hint.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	jkerrors "github.com/addozhang/jk/internal/errors"
	"github.com/addozhang/jk/internal/jenkins"
	"github.com/addozhang/jk/internal/jenkinsurl"
	"github.com/addozhang/jk/internal/schema"
)

// newBuildCommand returns the `jk build` parent. The parent itself has
// no RunE; running `jk build` alone prints the subcommand list.
func newBuildCommand(flags *GlobalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "build",
		Short: "Trigger and inspect Jenkins builds",
		Long:  "Pipeline-run lifecycle commands: trigger new builds, query status, stage tree, respond to input steps, and stream logs.",
	}
	cmd.AddCommand(
		newBuildTriggerCommand(flags),
		newBuildStatusCommand(flags),
		newBuildParamsCommand(flags),
		newBuildStagesCommand(flags),
		newBuildInputCommand(flags),
		newBuildLogsCommand(flags),
	)
	return cmd
}

// ---------------------------------------------------------------------------
// jk build trigger <pipeline-url> [-p K=V ...] [--watch]
// ---------------------------------------------------------------------------

func newBuildTriggerCommand(flags *GlobalFlags) *cobra.Command {
	var (
		params []string
		watch  bool
	)
	cmd := &cobra.Command{
		Use:   "trigger <pipeline-url>",
		Short: "Trigger a new build of a pipeline",
		Long: `Trigger a build of the pipeline at <pipeline-url>. Use -p KEY=VALUE
(repeatable) to pass parameters; a value of the form @path/to/file is
read from the named file. With --watch, the command polls the running
build and exits with a code derived from the terminal state:

  0 SUCCESS    1 FAILURE    2 UNSTABLE    3 ABORTED    4 PENDING_INPUT

See docs/schema.md §3.6 and specs/build.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBuildTrigger(cmd, flags, args[0], params, watch)
		},
	}
	cmd.Flags().StringArrayVarP(&params, "param", "p", nil, "parameter as KEY=VALUE (repeatable); use KEY=@file to read value from a file")
	cmd.Flags().BoolVar(&watch, "watch", false, "poll the triggered build and exit with a code per terminal state")
	return cmd
}

func runBuildTrigger(cmd *cobra.Command, flags *GlobalFlags, rawURL string, paramFlags []string, watch bool) error {
	ref, err := resolveRef(rawURL)
	if err != nil {
		return err
	}
	paramMap, err := parseParamFlags(paramFlags)
	if err != nil {
		return err
	}
	cc, err := newCommandContext(cmd, flags)
	if err != nil {
		return err
	}

	// Validate parameter names against the pipeline definition before
	// triggering. The spec scenario "Triggering an unknown parameter"
	// requires an actionable error pointing at `jk pipeline params`;
	// we'd rather burn one extra round-trip than start a build whose
	// only failure mode is "Jenkins silently dropped the param".
	if len(paramMap) > 0 {
		if vErr := validateParamNames(cmd.Context(), cc, ref, rawURL, flags.Timeout, paramMap); vErr != nil {
			return vErr
		}
	}

	// The trigger POST itself uses its own bounded context; the queue
	// poll has its own deadline argument so we don't accidentally let
	// `--timeout` shorten the queue wait.
	triggerCtx, cancelTrigger := cc.withTimeout(cmd.Context())
	queueLoc, err := cc.client.TriggerBuild(triggerCtx, ref, paramMap)
	cancelTrigger()
	if err != nil {
		return translateClientError(ref.Host, rawURL, flags.Timeout, err)
	}

	queueID, err := extractQueueID(queueLoc)
	if err != nil {
		return jkerrors.NewMalformedResponse(ref.Host, err)
	}

	// Resolve queue → build URL/number. We give this its own timeout
	// (10× the per-request timeout, capped at 5 minutes) because
	// queue-resolution latency is dominated by executor availability,
	// not network round-trip.
	queueTimeout := flags.Timeout * 10
	if max := 5 * time.Minute; queueTimeout > max {
		queueTimeout = max
	}
	buildURL, buildNumber, err := cc.client.ResolveQueueItem(cmd.Context(), queueLoc, queueTimeout)
	if err != nil {
		return translateClientError(ref.Host, rawURL, queueTimeout, err)
	}

	out := schema.BuildTrigger{
		QueueID:     queueID,
		BuildURL:    &buildURL,
		BuildNumber: &buildNumber,
	}
	if err = cc.render(out); err != nil {
		return err
	}

	if !watch {
		return nil
	}
	// For --watch we need a Ref pointing at the new build. Re-parse
	// the resolved build URL so APIPath includes the build number.
	buildRef, err := jenkinsurl.Parse(buildURL)
	if err != nil {
		return jkerrors.NewMalformedResponse(ref.Host, fmt.Errorf("re-parse build URL %q: %w", buildURL, err))
	}
	return watchBuild(cmd.Context(), cc, buildRef, buildURL, flags.Timeout)
}

// validateParamNames fetches the pipeline's parameter definitions and
// rejects any flag key that does not appear there. The first unknown
// name surfaces a JKError suggesting `jk pipeline params <url>`.
func validateParamNames(ctx context.Context, cc *commandContext, ref *jenkinsurl.Ref, rawURL string, timeout time.Duration, params map[string]string) error {
	probeCtx, cancel := cc.withTimeout(ctx)
	defer cancel()
	body, err := cc.client.GetPipelineParams(probeCtx, ref)
	if err != nil {
		return translateClientError(ref.Host, rawURL, timeout, err)
	}
	defs, err := schema.MapPipelineParams(body)
	if err != nil {
		return jkerrors.NewMalformedResponse(ref.Host, err)
	}
	known := make(map[string]struct{}, len(defs.Parameters))
	for _, p := range defs.Parameters {
		known[p.Name] = struct{}{}
	}
	for k := range params {
		if _, ok := known[k]; !ok {
			return &jkerrors.JKError{
				Code:       "unknown_parameter",
				Message:    fmt.Sprintf("Parameter %q is not defined on this pipeline.", k),
				Suggestion: fmt.Sprintf("List valid names with: jk pipeline params %s", rawURL),
			}
		}
	}
	return nil
}

// queueItemPathRegex captures the numeric ID from a Jenkins queue
// Location URL of the form `<base>/queue/item/<N>/`. The trailing
// slash is optional because some Jenkins versions omit it.
var queueItemPathRegex = regexp.MustCompile(`/queue/item/(\d+)/?$`)

// extractQueueID parses a queue item URL and returns its numeric ID.
// Returns a wrapped error when the URL does not match the expected
// shape; callers should surface that as malformed_response.
func extractQueueID(queueURL string) (int, error) {
	trimmed := strings.TrimRight(queueURL, "/")
	m := queueItemPathRegex.FindStringSubmatch(trimmed + "/")
	if len(m) != 2 {
		return 0, fmt.Errorf("unexpected queue Location URL %q (no /queue/item/<id>/ suffix)", queueURL)
	}
	id, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, fmt.Errorf("queue id in %q is not numeric: %w", queueURL, err)
	}
	return id, nil
}

// ---------------------------------------------------------------------------
// jk build status <build-url>
// ---------------------------------------------------------------------------

func newBuildStatusCommand(flags *GlobalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "status <build-url>",
		Short: "Show the current state of a build",
		Long: `Fetch the build at <build-url> and emit its lifecycle state, result,
duration, progress percent, and any pending input step.

See docs/schema.md §3.7 for the response shape.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBuildStatus(cmd, flags, args[0])
		},
	}
}

func runBuildStatus(cmd *cobra.Command, flags *GlobalFlags, rawURL string) error {
	ref, err := resolveBuildRef(rawURL)
	if err != nil {
		return err
	}
	cc, err := newCommandContext(cmd, flags)
	if err != nil {
		return err
	}
	ctx, cancel := cc.withTimeout(cmd.Context())
	defer cancel()

	body, err := cc.client.GetBuildStatus(ctx, ref)
	if err != nil {
		return translateClientError(ref.Host, rawURL, flags.Timeout, err)
	}
	status, err := schema.MapBuildStatus(body)
	if err != nil {
		return jkerrors.NewMalformedResponse(ref.Host, err)
	}

	// Enrich PENDING_INPUT state from wfapi when the build is still
	// live. Per openspec change add-input-parameter-submission §6:
	// Jenkins core /api/json only carries the InputAction _class
	// marker; the populated id/message/parameters live on
	// /wfapi/pendingInputActions. We only call wfapi when the build
	// is building AND the marker is present, and we degrade
	// gracefully when wfapi is unavailable (RUNNING + no
	// pendingInput, never fail the whole status call).
	if status.State == schema.BuildStatePendingInput {
		hasMarker, markerErr := schema.HasPendingInputMarker(body)
		if markerErr != nil {
			// Cannot happen — MapBuildStatus already consumed the
			// same bytes. Fall through defensively.
			hasMarker = true
		}
		if hasMarker && status.Building {
			enriched, enrichErr := enrichPendingInput(ctx, cc, ref)
			switch {
			case enrichErr != nil:
				if flags.Debug {
					//nolint:errcheck // best-effort debug log to stderr
					fmt.Fprintf(cc.stderr, "jk debug: wfapi pendingInputActions enrichment failed for %s: %v\n", rawURL, enrichErr)
				}
				// Downgrade to RUNNING and emit no pendingInput
				// block. We do NOT fail the status call: a
				// missing wfapi response shouldn't black out the
				// whole status read.
				status.State = schema.BuildStateRunning
				status.PendingInput = nil
			case enriched == nil:
				// Race: marker was present in /api/json but the
				// input was submitted before our wfapi call.
				// Downgrade to RUNNING.
				status.State = schema.BuildStateRunning
				status.PendingInput = nil
			default:
				status.PendingInput = enriched
			}
		}
	}

	return cc.render(status)
}

// enrichPendingInput fetches /<n>/wfapi/pendingInputActions and
// returns the first decoded entry, or nil if the array is empty
// (race: input was submitted between the two HTTP calls). Errors are
// returned to the caller, which decides whether to degrade or
// propagate.
func enrichPendingInput(ctx context.Context, cc *commandContext, ref *jenkinsurl.Ref) (*schema.PendingInput, error) {
	raw, err := cc.client.GetPendingInputs(ctx, ref)
	if err != nil {
		return nil, err
	}
	return schema.MapPendingInput(raw)
}

// ---------------------------------------------------------------------------
// jk build params <build-url>
// ---------------------------------------------------------------------------

func newBuildParamsCommand(flags *GlobalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "params <build-url>",
		Short: "Show the trigger-time parameter values of a build",
		Long: `Fetch the parameter values used to trigger the build at <build-url>
and emit them as a BuildParams payload.

Builds with no parameters return an empty parameters array (not an
error). Password / credentials parameters surface as value: null
because Jenkins redacts those server-side and never returns the
actual value, regardless of caller permissions.

The <build-url> may use a numeric build number or any Jenkins
permalink (lastBuild, lastSuccessfulBuild, etc.); the resolved
numeric build number appears in the output's buildNumber field.

Example:
  jk build params https://jenkins.example.com/job/svc/lastSuccessfulBuild/`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBuildParams(cmd, flags, args[0])
		},
	}
}

func runBuildParams(cmd *cobra.Command, flags *GlobalFlags, rawURL string) error {
	ref, err := resolveBuildRef(rawURL)
	if err != nil {
		return err
	}
	cc, err := newCommandContext(cmd, flags)
	if err != nil {
		return err
	}
	ctx, cancel := cc.withTimeout(cmd.Context())
	defer cancel()

	body, err := cc.client.GetBuildParams(ctx, ref)
	if err != nil {
		return translateClientError(ref.Host, rawURL, flags.Timeout, err)
	}
	params, err := schema.MapBuildParams(body)
	if err != nil {
		return jkerrors.NewMalformedResponse(ref.Host, err)
	}
	return cc.render(params)
}

// ---------------------------------------------------------------------------
// jk build stages <build-url>
// ---------------------------------------------------------------------------

func newBuildStagesCommand(flags *GlobalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "stages <build-url>",
		Short: "Show the stage tree of a build",
		Long: `Fetch the pipeline-run stage tree (sequential and parallel) for the
build at <build-url>.

See docs/schema.md §3.8 for the response shape.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBuildStages(cmd, flags, args[0])
		},
	}
}

func runBuildStages(cmd *cobra.Command, flags *GlobalFlags, rawURL string) error {
	ref, err := resolveBuildRef(rawURL)
	if err != nil {
		return err
	}
	cc, err := newCommandContext(cmd, flags)
	if err != nil {
		return err
	}
	ctx, cancel := cc.withTimeout(cmd.Context())
	defer cancel()

	body, err := cc.client.GetBuildStages(ctx, ref)
	if err != nil {
		return translateClientError(ref.Host, rawURL, flags.Timeout, err)
	}
	stages, err := schema.MapBuildStages(body)
	if err != nil {
		return jkerrors.NewMalformedResponse(ref.Host, err)
	}
	return cc.render(stages)
}

// ---------------------------------------------------------------------------
// jk build input <build-url> proceed|abort [--input-id ID]
// ---------------------------------------------------------------------------

func newBuildInputCommand(flags *GlobalFlags) *cobra.Command {
	var (
		inputID    string
		paramFlags []string
	)
	cmd := &cobra.Command{
		Use:   "input <build-url> proceed|abort",
		Short: "Respond to a paused pipeline input step",
		Long: `Submit a proceed or abort response to a paused Pipeline input step on
the build at <build-url>. When the build has exactly one pending
input, the command operates on that input by default. When two or
more inputs are pending, --input-id <id> is required.

Pass input-step parameters with repeatable -p KEY=VALUE flags. Use
@PATH to load a value from a file (the file is read verbatim, including
trailing newlines). Values are validated client-side against the
pending input's declared parameter shape:

  - CHOICE   : value must appear in the declared choices list.
  - BOOLEAN  : true/True/TRUE/false/False/1/0 (case-insensitive).
  - STRING / TEXT / PASSWORD / UNKNOWN : any string.

Unknown keys, invalid choices, or missing required parameters exit
with code 10 without contacting Jenkins. -p is ignored (with a stderr
warning) when the action is abort.

See docs/schema.md §3.9 and specs/build §"Respond to a pending input step".`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBuildInput(cmd, flags, args[0], args[1], inputID, paramFlags)
		},
	}
	cmd.Flags().StringVar(&inputID, "input-id", "", "select a specific pending input by id (required when multiple are pending)")
	cmd.Flags().StringSliceVarP(&paramFlags, "param", "p", nil, "input-step parameter as KEY=VALUE (repeatable). Use @PATH to load value from a file. Ignored for the abort action.")
	return cmd
}

func runBuildInput(cmd *cobra.Command, flags *GlobalFlags, rawURL, action, inputID string, paramFlags []string) error {
	var proceed bool
	switch strings.ToLower(action) {
	case "proceed":
		proceed = true
	case "abort":
		proceed = false
	default:
		return fmt.Errorf("invalid action %q: expected proceed or abort", action)
	}

	ref, err := resolveBuildRef(rawURL)
	if err != nil {
		return err
	}
	cc, err := newCommandContext(cmd, flags)
	if err != nil {
		return err
	}

	// Parse -p flags up front so file-read failures surface before
	// any HTTP call.
	supplied, err := parseParamFlags(paramFlags)
	if err != nil {
		return err
	}

	// For abort: -p is ignored. Warn once if supplied, then continue
	// without enforcing validation against the declared params.
	if !proceed && len(supplied) > 0 {
		fmt.Fprintln(cmd.ErrOrStderr(), "warning: -p flags are ignored for 'abort'") //nolint:errcheck
		supplied = nil
	}

	// Fetch pending inputs first to (a) pick the default ID when only
	// one is pending and (b) reject ambiguous invocations.
	probeCtx, cancelProbe := cc.withTimeout(cmd.Context())
	pendingBody, err := cc.client.GetPendingInputs(probeCtx, ref)
	cancelProbe()
	if err != nil {
		return translateClientError(ref.Host, rawURL, flags.Timeout, err)
	}
	pending, err := decodePendingInputList(pendingBody)
	if err != nil {
		return jkerrors.NewMalformedResponse(ref.Host, err)
	}
	resolvedID, err := pickInputID(pending, inputID, rawURL)
	if err != nil {
		return err
	}

	// Validate -p values against the declared parameter shape (proceed
	// path only). validateInputParameters fills in defaults for declared
	// params that were not supplied; an empty result means the v0.1
	// proceedEmpty endpoint should be used.
	var submitParams []jenkins.InputParameterValue
	var proceedText, proceedURL string
	if proceed {
		declared := declaredParametersFor(pending, resolvedID)
		submitParams, err = validateInputParameters(declared, supplied)
		if err != nil {
			return err
		}
		proceedText = proceedTextFor(pending, resolvedID)
		proceedURL = proceedURLFor(pending, resolvedID)
	}

	submitCtx, cancelSubmit := cc.withTimeout(cmd.Context())
	defer cancelSubmit()
	if err = cc.client.SubmitInput(submitCtx, ref, resolvedID, proceed, proceedText, proceedURL, submitParams); err != nil {
		return translateClientError(ref.Host, rawURL, flags.Timeout, err)
	}

	// After submission, the build state is briefly indeterminate. The
	// spec asks us to "return a YAML document confirming the new build
	// state" — we re-fetch /api/json so the state field is authoritative
	// rather than synthesized.
	statusCtx, cancelStatus := cc.withTimeout(cmd.Context())
	defer cancelStatus()
	statusBody, err := cc.client.GetBuildStatus(statusCtx, ref)
	state := schema.BuildStateRunning // sensible default if status fetch fails
	if err == nil {
		if st, mErr := schema.MapBuildStatus(statusBody); mErr == nil {
			state = st.State
		}
	}

	result := schema.BuildInputResult{
		InputID:  resolvedID,
		Action:   schema.InputActionProceed,
		BuildURL: rawURL,
		State:    state,
	}
	if !proceed {
		result.Action = schema.InputActionAbort
	}
	return cc.render(result)
}

// pendingInputItem is the minimal shape we need to disambiguate and
// validate -p values; the schema mapper's PendingInput returns only
// the first entry which is insufficient for the multi-input case.
//
// The `Inputs` field mirrors the wfapi shape (`type` + nested
// `definition.defaultVal` / `definition.choices`); see
// internal/schema/mapper_build.go rawWfapiInputParam.
type pendingInputItem struct {
	ID          string                  `json:"id"`
	ProceedText string                  `json:"proceedText"`
	Message     string                  `json:"message"`
	Inputs      []pendingInputParameter `json:"inputs"`
	// ProceedURL is the server-rooted path Jenkins itself advertises
	// for parameterized submission, e.g.
	// `/job/svc/42/wfapi/inputSubmit?inputId=Deploy`. When non-empty
	// the client POSTs to <Host>+<ProceedURL>; when empty it falls
	// back to the legacy `/input/<id>/submit` endpoint. The wfapi
	// path is the only one Jenkins's workflow plugin accepts cleanly
	// for parameterized input — the legacy `/input/<id>/submit`
	// records "Rejected by <user>" and aborts the build.
	ProceedURL string `json:"proceedUrl"`
}

// pendingInputParameter mirrors one entry under wfapi pendingInputActions[].inputs[].
type pendingInputParameter struct {
	Type        string  `json:"type"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	Definition  *struct {
		DefaultVal any      `json:"defaultVal"`
		Choices    []string `json:"choices"`
	} `json:"definition"`
}

// decodePendingInputList parses the wfapi pendingInputActions array.
// Returns an empty (non-nil) slice when no inputs are pending.
func decodePendingInputList(raw []byte) ([]pendingInputItem, error) {
	var items []pendingInputItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	if items == nil {
		items = []pendingInputItem{}
	}
	return items, nil
}

// pickInputID applies the disambiguation rules from specs/build
// §"Respond to a pending input step":
//
//   - 0 pending  → error: nothing to respond to.
//   - 1 pending  → use it, unless --input-id was supplied and mismatches.
//   - 2+ pending → require --input-id; error and list the choices when
//     omitted; require an exact match when supplied.
func pickInputID(pending []pendingInputItem, requested, rawURL string) (string, error) {
	switch len(pending) {
	case 0:
		return "", &jkerrors.JKError{
			Code:       "no_pending_input",
			Message:    fmt.Sprintf("Build %s has no pending input.", rawURL),
			Suggestion: "Check the build status with: jk build status " + rawURL,
		}
	case 1:
		only := pending[0].ID
		if requested != "" && requested != only {
			return "", &jkerrors.JKError{
				Code:       "input_id_mismatch",
				Message:    fmt.Sprintf("Requested --input-id %q does not match the pending input %q.", requested, only),
				Suggestion: fmt.Sprintf("Omit --input-id or use --input-id %s", only),
			}
		}
		return only, nil
	default:
		if requested == "" {
			return "", &jkerrors.JKError{
				Code:       "ambiguous_input",
				Message:    fmt.Sprintf("Build %s has multiple pending inputs:\n%s", rawURL, formatPendingList(pending)),
				Suggestion: "Re-run with --input-id <id> to select one.",
			}
		}
		for _, it := range pending {
			if it.ID == requested {
				return requested, nil
			}
		}
		return "", &jkerrors.JKError{
			Code:       "input_id_unknown",
			Message:    fmt.Sprintf("Pending input %q not found on build %s.", requested, rawURL),
			Suggestion: "Available IDs:\n" + formatPendingList(pending),
		}
	}
}

// formatPendingList renders pending inputs as "  - <id>: <message>"
// lines for embedding into error messages.
func formatPendingList(pending []pendingInputItem) string {
	var b strings.Builder
	for _, it := range pending {
		fmt.Fprintf(&b, "  - %s: %s\n", it.ID, it.Message)
	}
	return strings.TrimRight(b.String(), "\n")
}

// declaredParametersFor returns the parameter definitions of the
// pending input identified by id. Returns nil when id is not found
// or the input declares no parameters.
func declaredParametersFor(pending []pendingInputItem, id string) []pendingInputParameter {
	for _, it := range pending {
		if it.ID == id {
			return it.Inputs
		}
	}
	return nil
}

// proceedTextFor returns the `proceedText` (the input step's `ok`
// label) for the pending input identified by id. Jenkins requires
// this value in the form body of /input/<id>/submit; submitting
// without it (or with a value that does not match the declared
// label) is rejected as "Rejected by <user>", aborting the build.
//
// Returns the empty string when id is not found, which causes
// Client.SubmitInput to error before any HTTP call when parameters
// are supplied.
func proceedTextFor(pending []pendingInputItem, id string) string {
	for _, it := range pending {
		if it.ID == id {
			return it.ProceedText
		}
	}
	return ""
}

// proceedURLFor returns the server-rooted submission path Jenkins
// advertises in wfapi pendingInputActions[].proceedUrl. Empty when
// the id is not found or wfapi did not include it (older Jenkins or
// non-workflow input). Callers fall back to /input/<id>/submit when
// empty.
func proceedURLFor(pending []pendingInputItem, id string) string {
	for _, it := range pending {
		if it.ID == id {
			return it.ProceedURL
		}
	}
	return ""
}

// validateInputParameters walks the declared parameter list and the
// user-supplied -p map, returning the final []jenkins.InputParameterValue
// to submit. Behavior:
//
//   - Unknown supplied key (not present in declared)        -> JKError exit 10
//   - Declared param with no value supplied and no default  -> JKError exit 10
//   - Declared param with no value supplied and a default   -> use default
//   - CHOICE value not in declared choices                  -> JKError exit 10
//   - BOOLEAN value not in {true,false,1,0} (case-insens.)  -> JKError exit 10
//   - STRING / TEXT / PASSWORD / UNKNOWN                    -> accepted as-is
//
// When the declared list is empty and the supplied map is empty,
// returns (nil, nil) so the caller falls through to the v0.1
// proceedEmpty path.
func validateInputParameters(declared []pendingInputParameter, supplied map[string]string) ([]jenkins.InputParameterValue, error) {
	// Build a set of declared names for the "unknown key" check.
	declaredByName := make(map[string]pendingInputParameter, len(declared))
	for _, d := range declared {
		declaredByName[d.Name] = d
	}

	// Reject any supplied key that the pending input does not declare.
	for k := range supplied {
		if _, ok := declaredByName[k]; !ok {
			validNames := make([]string, 0, len(declared))
			for _, d := range declared {
				validNames = append(validNames, d.Name)
			}
			suggestion := "No parameters are declared by this input."
			if len(validNames) > 0 {
				suggestion = "Valid parameters: " + strings.Join(validNames, ", ")
			}
			return nil, &jkerrors.JKError{
				Code:       "invalid_input_parameter",
				Message:    fmt.Sprintf("Unknown parameter %q for this input.", k),
				Suggestion: suggestion,
			}
		}
	}

	// When neither side has anything, fall through to proceedEmpty.
	if len(declared) == 0 && len(supplied) == 0 {
		return nil, nil
	}

	// Validate each declared param, filling in defaults where needed.
	out := make([]jenkins.InputParameterValue, 0, len(declared))
	for _, d := range declared {
		raw, present := supplied[d.Name]
		if !present {
			if d.Definition == nil || d.Definition.DefaultVal == nil {
				return nil, &jkerrors.JKError{
					Code:       "invalid_input_parameter",
					Message:    fmt.Sprintf("Missing required parameter %q (no default value).", d.Name),
					Suggestion: fmt.Sprintf("Pass -p %s=VALUE.", d.Name),
				}
			}
			raw = formatDefaultVal(d.Definition.DefaultVal)
		}

		value, err := validateOneParameter(d, raw)
		if err != nil {
			return nil, err
		}
		out = append(out, jenkins.InputParameterValue{Name: d.Name, Value: value})
	}
	return out, nil
}

// validateOneParameter applies type-specific value rules and returns
// the normalized wire-format string. Unknown / STRING / TEXT / PASSWORD
// accept any value verbatim.
func validateOneParameter(d pendingInputParameter, raw string) (string, error) {
	pt := schemaClassifyParameter(d.Type)
	switch pt {
	case schema.ParameterTypeChoice:
		if d.Definition != nil {
			for _, c := range d.Definition.Choices {
				if c == raw {
					return raw, nil
				}
			}
		}
		choices := []string{}
		if d.Definition != nil {
			choices = d.Definition.Choices
		}
		return "", &jkerrors.JKError{
			Code:       "invalid_input_parameter",
			Message:    fmt.Sprintf("Parameter %q value %q is not a valid choice.", d.Name, raw),
			Suggestion: "Valid choices: " + strings.Join(choices, ", "),
		}
	case schema.ParameterTypeBoolean:
		switch strings.ToLower(raw) {
		case "true", "false", "1", "0":
			return strings.ToLower(raw), nil
		default:
			return "", &jkerrors.JKError{
				Code:       "invalid_input_parameter",
				Message:    fmt.Sprintf("Parameter %q value %q is not a boolean.", d.Name, raw),
				Suggestion: "Use true, false, 1, or 0 (case-insensitive).",
			}
		}
	default:
		// STRING / TEXT / PASSWORD / UNKNOWN: pass through.
		return raw, nil
	}
}

// schemaClassifyParameter is a local reuse of schema.classifyParameterType
// keyed off the wfapi `type` field (simple class name). Kept private
// to internal/cli to avoid exporting the helper from internal/schema.
func schemaClassifyParameter(typeName string) schema.ParameterType {
	switch {
	case strings.Contains(typeName, "StringParameter"):
		return schema.ParameterTypeString
	case strings.Contains(typeName, "BooleanParameter"):
		return schema.ParameterTypeBoolean
	case strings.Contains(typeName, "ChoiceParameter"):
		return schema.ParameterTypeChoice
	case strings.Contains(typeName, "TextParameter"):
		return schema.ParameterTypeText
	case strings.Contains(typeName, "PasswordParameter"):
		return schema.ParameterTypePassword
	default:
		return schema.ParameterTypeUnknown
	}
}

// formatDefaultVal renders a JSON-decoded default value as the string
// Jenkins expects on the wire. Bools become "true"/"false" rather than
// Go's "%v" output (which would also be correct, but we keep it
// explicit). Numbers go through fmt.Sprintf("%v", ...).
func formatDefaultVal(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprintf("%v", t)
	}
}

// ---------------------------------------------------------------------------
// jk build logs <build-url> [-f] [--stage NAME]
// ---------------------------------------------------------------------------

func newBuildLogsCommand(flags *GlobalFlags) *cobra.Command {
	var (
		follow bool
		stage  string
	)
	cmd := &cobra.Command{
		Use:   "logs <build-url>",
		Short: "Print or stream a build's console log",
		Long: `Print the console log of the build at <build-url> to stdout. With -f,
stream new content until the build reaches a terminal state. With
--stage NAME, print only the log of the named stage.

This command bypasses --output formatting: the log is the payload and
the schema wrapper would only obscure it.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBuildLogs(cmd, flags, args[0], follow, stage)
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "stream new log content until the build finishes")
	cmd.Flags().StringVar(&stage, "stage", "", "print only the log of the named stage")
	return cmd
}

func runBuildLogs(cmd *cobra.Command, flags *GlobalFlags, rawURL string, follow bool, stageName string) error {
	ref, err := resolveBuildRef(rawURL)
	if err != nil {
		return err
	}
	cc, err := newCommandContext(cmd, flags)
	if err != nil {
		return err
	}

	if stageName != "" {
		return runBuildStageLog(cmd, cc, ref, rawURL, flags.Timeout, stageName)
	}

	// Full console log path. We deliberately do NOT bound this with
	// cc.withTimeout(): follow mode is expected to outlast the per-
	// request timeout, and the streamer already honors ctx.Done().
	stdout := cmd.OutOrStdout()
	if !follow {
		streamCtx, cancel := cc.withTimeout(cmd.Context())
		defer cancel()
		return translateClientError(ref.Host, rawURL, flags.Timeout,
			cc.client.StreamConsoleLog(streamCtx, ref, stdout, false))
	}
	// Follow mode: stream until the build is no longer in-flight.
	// StreamConsoleLog(follow=true) polls /logText/progressiveText
	// until X-More-Data is absent — Jenkins clears that header at
	// the moment of completion, so a single call suffices.
	return translateClientError(ref.Host, rawURL, flags.Timeout,
		cc.client.StreamConsoleLog(cmd.Context(), ref, stdout, true))
}

// runBuildStageLog implements the `--stage NAME` path: it pulls the
// stage tree to map NAME → flowNodeID, fetches the stage log, and
// writes the text portion to stdout. On miss, it lists the actual
// stage names so the user can re-run with the correct one.
func runBuildStageLog(cmd *cobra.Command, cc *commandContext, ref *jenkinsurl.Ref, rawURL string, timeout time.Duration, stageName string) error {
	stagesCtx, cancelStages := cc.withTimeout(cmd.Context())
	stagesBody, err := cc.client.GetBuildStages(stagesCtx, ref)
	cancelStages()
	if err != nil {
		return translateClientError(ref.Host, rawURL, timeout, err)
	}
	var src struct {
		Stages []stageNode `json:"stages"`
	}
	if err = json.Unmarshal(stagesBody, &src); err != nil {
		return jkerrors.NewMalformedResponse(ref.Host, err)
	}
	flowID, names := findStageID(src.Stages, stageName)
	if flowID == "" {
		return &jkerrors.JKError{
			Code:    "stage_not_found",
			Message: fmt.Sprintf("Stage %q not found on build %s.", stageName, rawURL),
			Suggestion: fmt.Sprintf("Available stages:\n  - %s",
				strings.Join(names, "\n  - ")),
		}
	}

	// Real Jenkins reports length:0 on a stage node's own /wfapi/log;
	// the actual log text lives on the stage's child step nodes
	// (stageFlowNodes). We fetch the stage's wfapi/describe to
	// enumerate those children, then concatenate their log text.
	nodeCtx, cancelNode := cc.withTimeout(cmd.Context())
	nodeBody, err := cc.client.GetNodeDescribe(nodeCtx, ref, flowID)
	cancelNode()
	if err != nil {
		return translateClientError(ref.Host, rawURL, timeout, err)
	}
	var nodeSrc struct {
		StageFlowNodes []struct {
			ID string `json:"id"`
		} `json:"stageFlowNodes"`
	}
	if err = json.Unmarshal(nodeBody, &nodeSrc); err != nil {
		return jkerrors.NewMalformedResponse(ref.Host, err)
	}

	// childIDs: when no child step nodes are reported, fall back to
	// the stage node itself for compatibility with older Jenkins
	// versions where the stage node's /wfapi/log carries the text.
	childIDs := make([]string, 0, len(nodeSrc.StageFlowNodes))
	for _, n := range nodeSrc.StageFlowNodes {
		if n.ID != "" {
			childIDs = append(childIDs, n.ID)
		}
	}
	if len(childIDs) == 0 {
		childIDs = []string{flowID}
	}

	out := cmd.OutOrStdout()
	for _, id := range childIDs {
		logCtx, cancelLog := cc.withTimeout(cmd.Context())
		logBody, lerr := cc.client.GetStageLog(logCtx, ref, id)
		cancelLog()
		if lerr != nil {
			return translateClientError(ref.Host, rawURL, timeout, lerr)
		}
		// wfapi log responses wrap the text in {"text": "..."} along
		// with metadata. Extract the text field; if the response is
		// plain bytes (older Jenkins), fall back to writing as-is.
		var wrapped struct {
			Text string `json:"text"`
		}
		if jErr := json.Unmarshal(logBody, &wrapped); jErr == nil && wrapped.Text != "" {
			if _, werr := io.WriteString(out, wrapped.Text); werr != nil {
				return werr
			}
		} else if jErr != nil {
			// Body is not JSON — write raw.
			if _, werr := out.Write(logBody); werr != nil {
				return werr
			}
		}
	}
	return nil
}

// stageNode mirrors the wfapi describe shape just enough to recover
// the flowNodeID and walk the parallel tree. The schema.Stage view
// intentionally drops the `id` field; we re-decode here rather than
// add it to the public schema.
type stageNode struct {
	ID       string      `json:"id"`
	Name     string      `json:"name"`
	Parallel []stageNode `json:"parallel"`
}

// findStageID performs a depth-first search of the stage tree looking
// for the first node whose Name equals target. Returns the flowNodeID
// on hit and, on miss, the flat list of all discovered names so the
// caller can render a helpful "available stages" message.
func findStageID(stages []stageNode, target string) (string, []string) {
	var names []string
	var walk func(in []stageNode) string
	walk = func(in []stageNode) string {
		for _, s := range in {
			names = append(names, s.Name)
			if s.Name == target {
				return s.ID
			}
			if id := walk(s.Parallel); id != "" {
				return id
			}
		}
		return ""
	}
	id := walk(stages)
	return id, names
}

// ---------------------------------------------------------------------------
// --watch implementation
// ---------------------------------------------------------------------------

const (
	watchPollInitial = 2 * time.Second
	watchPollMax     = 10 * time.Second
	watchBackoffAt   = 60 * time.Second
)

// watchPollIntervalFor returns the interval to wait before the next
// status poll, given how long the watch has been running. Exposed as a
// package-level function so tests can swap in a faster cadence without
// reaching for global mutable timer state.
var watchPollIntervalFor = func(elapsed time.Duration) time.Duration {
	if elapsed > watchBackoffAt {
		return watchPollMax
	}
	return watchPollInitial
}

// watchBuild polls /api/json on buildRef until State becomes terminal
// (DONE or PENDING_INPUT) and returns a [*BuildResultExitError]
// carrying the per-result exit code. The poll cadence starts at 2s
// and steps up to 10s once the build has been polled for 60s — this
// matches the spec ("interval MUST start at 2 seconds and back off to
// a maximum of 10 seconds after one minute of polling").
//
// Progress lines go to stderr; stdout is reserved for the BuildTrigger
// document that was already rendered, plus any future structured
// status output we may add.
func watchBuild(ctx context.Context, cc *commandContext, buildRef *jenkinsurl.Ref, buildURL string, timeout time.Duration) error {
	start := time.Now()
	for {
		pollCtx, cancel := context.WithTimeout(ctx, timeout)
		body, err := cc.client.GetBuildStatus(pollCtx, buildRef)
		cancel()
		if err != nil {
			return translateClientError(buildRef.Host, buildURL, timeout, err)
		}
		status, err := schema.MapBuildStatus(body)
		if err != nil {
			return jkerrors.NewMalformedResponse(buildRef.Host, err)
		}
		// When the mapper reports PENDING_INPUT, enrich from wfapi
		// so the user-facing line below can show the input id and
		// message. The mapper only carries the marker presence-bit;
		// see runBuildStatus for the rationale. On enrichment
		// failure (or race), fall back to a generic notice rather
		// than failing the whole watch.
		if status.State == schema.BuildStatePendingInput && status.Building {
			enrichCtx, enrichCancel := context.WithTimeout(ctx, timeout)
			enriched, eerr := enrichPendingInput(enrichCtx, cc, buildRef)
			enrichCancel()
			if eerr == nil && enriched != nil {
				status.PendingInput = enriched
			}
		}
		//nolint:errcheck // best-effort progress write to stderr
		fmt.Fprintf(cc.stderr, "build %s: state=%s progress=%d%%\n",
			buildURL, status.State, status.ProgressPercent)

		switch status.State {
		case schema.BuildStateDone:
			return buildResultToExit(status.Result)
		case schema.BuildStatePendingInput:
			if status.PendingInput != nil {
				//nolint:errcheck // best-effort pending-input notice to stderr
				fmt.Fprintf(cc.stderr, "build paused for input: id=%s message=%q\n",
					status.PendingInput.ID, status.PendingInput.Message)
			}
			return &jkerrors.BuildResultExitError{Code: 4, Reason: "PENDING_INPUT"}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(watchPollIntervalFor(time.Since(start))):
			// next iteration
		}
	}
}

// buildResultToExit maps a terminal BuildResult to the matching exit
// code error. nil or unrecognized results fall back to FAILURE (1)
// since "Jenkins reports done but produced no result" is an error
// state from the user's perspective and 1 is the most defensible
// approximation.
func buildResultToExit(result *schema.BuildResult) error {
	if result == nil {
		return &jkerrors.BuildResultExitError{Code: 1, Reason: "FAILURE (no result reported)"}
	}
	switch *result {
	case schema.BuildResultSuccess:
		return &jkerrors.BuildResultExitError{Code: 0, Reason: "SUCCESS"}
	case schema.BuildResultFailure:
		return &jkerrors.BuildResultExitError{Code: 1, Reason: "FAILURE"}
	case schema.BuildResultUnstable:
		return &jkerrors.BuildResultExitError{Code: 2, Reason: "UNSTABLE"}
	case schema.BuildResultAborted:
		return &jkerrors.BuildResultExitError{Code: 3, Reason: "ABORTED"}
	default:
		return &jkerrors.BuildResultExitError{Code: 1, Reason: "FAILURE (" + string(*result) + ")"}
	}
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// resolveBuildRef is like resolveRef but also rejects URLs that do
// not address a specific build — either by numeric build number or
// by a Jenkins permalink (lastBuild, lastSuccessfulBuild, etc.).
// Build commands operating on a specific run MUST receive an
// addressable build, not a bare pipeline URL — silently falling back
// to lastBuild would mask "I forgot the build number" typos with
// stale data.
func resolveBuildRef(rawURL string) (*jenkinsurl.Ref, error) {
	ref, err := resolveRef(rawURL)
	if err != nil {
		return nil, err
	}
	if ref.BuildNumber == 0 && ref.BuildPermalink == "" {
		return nil, &jkerrors.JKError{
			Code:       "missing_build_number",
			Message:    fmt.Sprintf("URL %s does not include a build number or permalink.", rawURL),
			Suggestion: "Append the build number (.../job/svc/42/) or a permalink (.../job/svc/lastSuccessfulBuild/).",
		}
	}
	return ref, nil
}

// Compile-time interface assertion: ensure jenkins.Client carries the
// methods build.go invokes. Catching a signature drift here yields a
// readable error rather than a confusing call-site failure.
var _ buildClientSurface = (*jenkins.Client)(nil)

type buildClientSurface interface {
	TriggerBuild(ctx context.Context, ref *jenkinsurl.Ref, params map[string]string) (string, error)
	ResolveQueueItem(ctx context.Context, queueURL string, timeout time.Duration) (string, int, error)
	GetBuildStatus(ctx context.Context, ref *jenkinsurl.Ref) ([]byte, error)
	GetBuildParams(ctx context.Context, ref *jenkinsurl.Ref) ([]byte, error)
	GetBuildStages(ctx context.Context, ref *jenkinsurl.Ref) ([]byte, error)
	GetPendingInputs(ctx context.Context, ref *jenkinsurl.Ref) ([]byte, error)
	SubmitInput(ctx context.Context, ref *jenkinsurl.Ref, inputID string, proceed bool, proceedText, proceedURL string, parameters []jenkins.InputParameterValue) error
	StreamConsoleLog(ctx context.Context, ref *jenkinsurl.Ref, w io.Writer, follow bool) error
	GetStageLog(ctx context.Context, ref *jenkinsurl.Ref, flowNodeID string) ([]byte, error)
	GetNodeDescribe(ctx context.Context, ref *jenkinsurl.Ref, flowNodeID string) ([]byte, error)
}

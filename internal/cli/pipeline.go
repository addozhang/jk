package cli

// pipeline.go wires the `jk pipeline info|params|list` subcommands.
// Each subcommand follows the same four-step pattern documented in
// design.md §D7:
//
//   1. Parse the user's URL into a Ref (resolveRef).
//   2. Build a Jenkins client honoring global flags (newCommandContext).
//   3. Call the client; map raw JSON via internal/schema.
//   4. Render the schema value through internal/output.
//
// Errors from any step are translated via translateClientError so the
// user sees a JKError with a human-actionable suggestion instead of a
// stack-trace-like wrapped chain.

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	jkerrors "github.com/addozhang/jk/internal/errors"
	"github.com/addozhang/jk/internal/schema"
)

// newPipelineCommand returns the `jk pipeline` parent and registers its
// three subcommands. Parent has no RunE — invoking `jk pipeline` alone
// prints the usage list.
func newPipelineCommand(flags *GlobalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pipeline",
		Short: "Inspect Jenkins pipelines and folders",
		Long:  "Read-only commands for pipeline metadata, parameter definitions, and folder listings.",
	}
	cmd.AddCommand(
		newPipelineInfoCommand(flags),
		newPipelineParamsCommand(flags),
		newPipelineListCommand(flags),
	)
	return cmd
}

// ---------------------------------------------------------------------------
// jk pipeline info <url>
// ---------------------------------------------------------------------------

func newPipelineInfoCommand(flags *GlobalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "info <url>",
		Short: "Show metadata for a pipeline",
		Long: `Fetch the pipeline at <url> and emit its name, description, buildable flag,
last build reference, and (for multibranch pipelines) the branch list.

See docs/schema.md §3.3 for the response shape.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPipelineInfo(cmd, flags, args[0])
		},
	}
}

func runPipelineInfo(cmd *cobra.Command, flags *GlobalFlags, rawURL string) error {
	ref, err := resolveRef(rawURL)
	if err != nil {
		return err
	}
	cc, err := newCommandContext(cmd, flags)
	if err != nil {
		return err
	}
	ctx, cancel := cc.withTimeout(cmd.Context())
	defer cancel()

	body, err := cc.client.GetPipelineInfo(ctx, ref)
	if err != nil {
		return translateClientError(ref.Host, rawURL, flags.Timeout, err)
	}
	info, err := schema.MapPipelineInfo(body)
	if err != nil {
		return jkerrors.NewMalformedResponse(ref.Host, err)
	}
	return cc.render(info)
}

// ---------------------------------------------------------------------------
// jk pipeline params <url>
// ---------------------------------------------------------------------------

func newPipelineParamsCommand(flags *GlobalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "params <url>",
		Short: "List parameter definitions for a pipeline",
		Long: `Fetch the parameter definitions for the pipeline at <url>. Returns an
empty parameters array (not an error) for pipelines with no parameters.

See docs/schema.md §3.4 for the response shape.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPipelineParams(cmd, flags, args[0])
		},
	}
}

func runPipelineParams(cmd *cobra.Command, flags *GlobalFlags, rawURL string) error {
	ref, err := resolveRef(rawURL)
	if err != nil {
		return err
	}
	cc, err := newCommandContext(cmd, flags)
	if err != nil {
		return err
	}
	ctx, cancel := cc.withTimeout(cmd.Context())
	defer cancel()

	body, err := cc.client.GetPipelineParams(ctx, ref)
	if err != nil {
		return translateClientError(ref.Host, rawURL, flags.Timeout, err)
	}
	params, err := schema.MapPipelineParams(body)
	if err != nil {
		return jkerrors.NewMalformedResponse(ref.Host, err)
	}
	return cc.render(params)
}

// ---------------------------------------------------------------------------
// jk pipeline list <folder-url>
// ---------------------------------------------------------------------------

func newPipelineListCommand(flags *GlobalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "list <folder-url>",
		Short: "List children of a Jenkins folder",
		Long: `List pipelines and sub-folders directly under <folder-url>. The URL MUST
point at a folder (or organization folder, or multibranch project); pointing
it at a leaf pipeline returns an actionable error suggesting "jk pipeline info"
instead.

See docs/schema.md §3.5 for the response shape.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPipelineList(cmd, flags, args[0])
		},
	}
}

func runPipelineList(cmd *cobra.Command, flags *GlobalFlags, rawURL string) error {
	ref, err := resolveRef(rawURL)
	if err != nil {
		return err
	}
	cc, err := newCommandContext(cmd, flags)
	if err != nil {
		return err
	}
	ctx, cancel := cc.withTimeout(cmd.Context())
	defer cancel()

	// Folder-shape check: we issue a class probe FIRST so we can emit
	// a clear "this URL is a pipeline, not a folder" error before the
	// list call (whose response would mention `jobs[]` regardless of
	// shape — Jenkins returns an empty jobs array for WorkflowJob too,
	// which would silently produce an empty list and confuse the user).
	classBody, err := cc.client.GetPipelineInfo(ctx, ref)
	if err != nil {
		return translateClientError(ref.Host, rawURL, flags.Timeout, err)
	}
	if folderErr := assertIsFolderURL(classBody, rawURL); folderErr != nil {
		return folderErr
	}

	body, err := cc.client.ListPipelinesInFolder(ctx, ref)
	if err != nil {
		return translateClientError(ref.Host, rawURL, flags.Timeout, err)
	}
	list, err := schema.MapPipelineList(body)
	if err != nil {
		return jkerrors.NewMalformedResponse(ref.Host, err)
	}
	return cc.render(list)
}

// assertIsFolderURL returns a JKError when the api/json response of
// `<url>` reports a `_class` that is neither a folder nor a multibranch
// project. The error suggests `jk pipeline info` as the right command
// for inspecting a single pipeline.
//
// We deliberately reuse the same _class catalogue the mapper uses
// (via schema.IsFolderLike) to keep one source of truth for "what
// counts as a folder".
func assertIsFolderURL(apiJSONBody []byte, rawURL string) error {
	var probe struct {
		Class string `json:"_class"`
	}
	if err := json.Unmarshal(apiJSONBody, &probe); err != nil {
		// A response we cannot even parse to extract _class is far more
		// likely a non-Jenkins endpoint than a folder; surface as
		// malformed so the user re-checks the URL.
		return jkerrors.NewMalformedResponse("", err)
	}
	if schema.IsFolderLikeClass(probe.Class) {
		return nil
	}
	return &jkerrors.JKError{
		Code: "not_a_folder",
		Message: fmt.Sprintf(
			"URL %s is a pipeline, not a folder.", rawURL),
		Suggestion: fmt.Sprintf(
			"Use: jk pipeline info %s", rawURL),
	}
}

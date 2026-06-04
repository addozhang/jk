package jenkins

// Client is the Jenkins REST API surface used by every jk command. It
// owns request composition (URL building, query parameters, HTTP
// methods) and raw-response retrieval; mapping the JSON to schema.*
// types is the job of internal/schema (group 12).
//
// Design choices (see openspec/changes/init-jk-jenkins-cli/design.md
// §D7 and tasks.md §11):
//
//   1. Methods return raw []byte plus error. Callers feed the bytes
//      into schema mappers. Keeping the client mapping-free means
//      fixtures are easy to record/replay and the mapping logic stays
//      a pure function of the JSON.
//
//   2. The client takes an *http.Client at construction time. Tests
//      pass a plain client pointed at httptest.Server; production
//      passes the transport built by [New] (with auth + CSRF + TLS).
//
//   3. Error translation is intentionally NOT done here. Non-2xx
//      responses become httpStatusError errors that internal/errors
//      can classify into JKErrors with the right user-facing message.
//
// Endpoint inventory:
//
//   - /api/json[?tree=...]                   pipeline info, params, list, lastBuild
//   - /<n>/api/json                          build status
//   - /buildWithParameters or /build         trigger
//   - /queue/item/<id>/api/json              queue resolution
//   - /<n>/wfapi/describe                    build stages (TBD: spike 1.2)
//   - /<n>/wfapi/pendingInputActions         pending inputs (TBD)
//   - /<n>/input/<id>/submit                 input submission (TBD)
//   - /<n>/execution/node/<n>/wfapi/log      per-stage log (TBD)
//   - /<n>/logText/progressiveText           console log streaming

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/addozhang/jk/internal/jenkinsurl"
)

// Client is safe for concurrent use; the underlying *http.Client owns
// connection pooling.
type Client struct {
	http *http.Client
}

// NewClient wraps an *http.Client (produced by [New] in production, or
// a plain client in tests) with the Jenkins-specific API methods.
func NewClient(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{http: httpClient}
}

// HTTPStatusError is returned for non-2xx responses. The API client
// surfaces these unchanged so callers (typically the CLI layer via
// internal/errors.Classify) can map them to JKError codes with
// host- and context-specific messages.
type HTTPStatusError struct {
	URL        string
	StatusCode int
	Status     string
	Body       []byte
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("jenkins: %s returned %s", e.URL, e.Status)
}

// ---------------------------------------------------------------------------
// Read-only /api/json methods
// ---------------------------------------------------------------------------

// GetPipelineInfo fetches `<ref>/api/json` and returns the raw JSON
// body. Mapping to schema.PipelineInfo is group 12's responsibility.
func (c *Client) GetPipelineInfo(ctx context.Context, ref *jenkinsurl.Ref) ([]byte, error) {
	return c.getJSON(ctx, ref.APIPath("api/json"), "")
}

// GetPipelineParams fetches the property/parameter definitions for a
// pipeline. The Jenkins REST API embeds parameters inside the
// pipeline's `property` array, so we issue a tree-filtered request to
// shrink the payload to just what the mapper needs.
func (c *Client) GetPipelineParams(ctx context.Context, ref *jenkinsurl.Ref) ([]byte, error) {
	// tree filter selects the ParametersDefinitionProperty entries; the
	// nested fields cover name, description, default, and choices.
	const tree = "property[parameterDefinitions[name,description,type,defaultParameterValue[value],choices]]"
	return c.getJSON(ctx, ref.APIPath("api/json"), tree)
}

// ListPipelinesInFolder fetches the jobs collection of a folder URL.
// The classification of each job (folder vs pipeline vs multibranch)
// is the mapper's concern; we simply pull every job's name + _class +
// url so the mapper can decide.
func (c *Client) ListPipelinesInFolder(ctx context.Context, ref *jenkinsurl.Ref) ([]byte, error) {
	const tree = "jobs[name,url,_class]"
	return c.getJSON(ctx, ref.APIPath("api/json"), tree)
}

// GetBuildStatus fetches `<ref>/<build>/api/json` where `<build>` is
// either a numeric build number or a Jenkins permalink name
// (e.g. `lastSuccessfulBuild`). The caller MUST supply one of the
// two; we refuse a Ref with neither because silently falling back
// to `lastBuild` would hide a "the build I just triggered hasn't
// started" race from the caller of build status commands.
//
// When a permalink is supplied we skip the `lastBuild[number]`
// pre-flight entirely and let Jenkins resolve the permalink
// server-side; a 404 (e.g. `lastSuccessfulBuild` on a pipeline that
// has only failed builds) surfaces as the underlying HTTP error
// rather than being masked as "has no builds yet".
func (c *Client) GetBuildStatus(ctx context.Context, ref *jenkinsurl.Ref) ([]byte, error) {
	if ref.BuildNumber == 0 && ref.BuildPermalink == "" {
		return nil, errors.New("jenkins: GetBuildStatus requires a Ref with a non-zero BuildNumber or a BuildPermalink; call ResolveLastBuild first")
	}
	return c.getJSON(ctx, ref.APIPath("api/json"), "")
}

// GetBuildParams fetches the trigger-time parameter values of a
// specific build via `<ref>/<build>/api/json?tree=number,url,actions[parameters[name,value]]`.
// The tree filter scopes the response to exactly what MapBuildParams
// reads, keeping the payload small even on builds with massive
// actions[] arrays from chatty plugins.
//
// Like GetBuildStatus, this method refuses a Ref that addresses
// neither a numeric build nor a permalink, and skips the
// `tree=lastBuild[number]` pre-flight entirely when a permalink is
// supplied (Jenkins resolves permalinks server-side).
func (c *Client) GetBuildParams(ctx context.Context, ref *jenkinsurl.Ref) ([]byte, error) {
	if ref.BuildNumber == 0 && ref.BuildPermalink == "" {
		return nil, errors.New("jenkins: GetBuildParams requires a Ref with a non-zero BuildNumber or a BuildPermalink")
	}
	const tree = "number,url,actions[parameters[name,value]]"
	return c.getJSON(ctx, ref.APIPath("api/json"), tree)
}

// ResolveLastBuild returns the build number of the pipeline's most
// recent build, regardless of result. Returns an error if the pipeline
// has never been built (lastBuild is null in the API response).
func (c *Client) ResolveLastBuild(ctx context.Context, ref *jenkinsurl.Ref) (int, error) {
	const tree = "lastBuild[number]"
	body, err := c.getJSON(ctx, ref.APIPath("api/json"), tree)
	if err != nil {
		return 0, err
	}
	var payload struct {
		LastBuild *struct {
			Number int `json:"number"`
		} `json:"lastBuild"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return 0, fmt.Errorf("jenkins: ResolveLastBuild: parse response: %w", err)
	}
	if payload.LastBuild == nil || payload.LastBuild.Number == 0 {
		return 0, fmt.Errorf("jenkins: ResolveLastBuild: pipeline has no builds yet")
	}
	return payload.LastBuild.Number, nil
}

// ---------------------------------------------------------------------------
// Write-side methods: trigger + queue resolution
// ---------------------------------------------------------------------------

// TriggerBuild kicks off a build of the pipeline addressed by ref.
//
// Endpoint choice:
//   - empty params -> POST `<ref>/build`
//   - non-empty    -> POST `<ref>/buildWithParameters` with form body
//
// Jenkins responds with 201 Created (sometimes 200 OK) and a `Location`
// header pointing at the queue item — that's what we return so the
// caller can poll [ResolveQueueItem]. A missing Location header is
// surfaced as a clear error since the queue location is the only way
// to find the eventual build number.
func (c *Client) TriggerBuild(ctx context.Context, ref *jenkinsurl.Ref, params map[string]string) (string, error) {
	endpoint := ref.APIPath("build")
	var body io.Reader = http.NoBody
	contentType := ""
	if len(params) > 0 {
		endpoint = ref.APIPath("buildWithParameters")
		form := url.Values{}
		for k, v := range params {
			form.Set(k, v)
		}
		body = strings.NewReader(form.Encode())
		contentType = "application/x-www-form-urlencoded"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return "", fmt.Errorf("jenkins: build trigger request: %w", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	// http.NewRequestWithContext does NOT populate GetBody for arbitrary
	// io.Reader bodies; set it explicitly so the crumb RoundTripper can
	// replay this body on a 403/CSRF retry. strings.Reader is safe to
	// re-create from the same source string.
	if reader, ok := body.(*strings.Reader); ok {
		src := make([]byte, reader.Len())
		_, _ = reader.Read(src) //nolint:errcheck // re-reading a fresh strings.Reader cannot fail
		// reset original body so it can still be read for the first attempt
		req.Body = io.NopCloser(strings.NewReader(string(src)))
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(string(src))), nil
		}
		req.ContentLength = int64(len(src))
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer closeBody(resp.Body)
	// Drain the body so connection re-use isn't blocked; the response
	// payload from Jenkins for a build trigger is irrelevant once we
	// have the Location header.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20)) //nolint:errcheck // best-effort drain

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", &HTTPStatusError{
			URL:        endpoint,
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
		}
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", fmt.Errorf("jenkins: TriggerBuild: response missing Location header (status %s)", resp.Status)
	}
	return loc, nil
}

// queuePollInterval bounds how often ResolveQueueItem pings Jenkins.
// 500ms balances responsiveness against load — typical Jenkins instances
// resolve queue items within 1-3s of submission.
const queuePollInterval = 500 * time.Millisecond

// ResolveQueueItem polls `<queueURL>/api/json` until the queue item
// has either produced a build (executable.number != null) or been
// cancelled, or until the supplied timeout elapses.
//
// Returns the absolute build URL and the build number on success.
// queueURL is the value returned by [TriggerBuild]; we append `api/json`
// to fetch its JSON representation.
func (c *Client) ResolveQueueItem(ctx context.Context, queueURL string, timeout time.Duration) (string, int, error) {
	// Compose the API URL once; Jenkins queue URLs always end with `/`.
	apiURL := strings.TrimRight(queueURL, "/") + "/api/json"

	// Use a context-bound deadline so polling honors caller cancellation
	// AND the explicit timeout, whichever fires first.
	pollCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(queuePollInterval)
	defer ticker.Stop()

	for {
		body, err := c.getJSON(pollCtx, apiURL, "")
		if err != nil {
			if errors.Is(pollCtx.Err(), context.DeadlineExceeded) {
				return "", 0, fmt.Errorf("jenkins: ResolveQueueItem: timeout after %s waiting for build to start", timeout)
			}
			return "", 0, err
		}
		var payload struct {
			Cancelled  bool `json:"cancelled"`
			Executable *struct {
				Number int    `json:"number"`
				URL    string `json:"url"`
			} `json:"executable"`
			Why string `json:"why"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return "", 0, fmt.Errorf("jenkins: ResolveQueueItem: parse: %w", err)
		}
		if payload.Cancelled {
			return "", 0, fmt.Errorf("jenkins: ResolveQueueItem: queue item was cancelled (%s)", payload.Why)
		}
		if payload.Executable != nil && payload.Executable.Number > 0 {
			return payload.Executable.URL, payload.Executable.Number, nil
		}

		select {
		case <-pollCtx.Done():
			return "", 0, fmt.Errorf("jenkins: ResolveQueueItem: timeout after %s waiting for build to start", timeout)
		case <-ticker.C:
			// next iteration
		}
	}
}

// ---------------------------------------------------------------------------
// wfapi (Workflow Pipeline Stage View) methods
//
// NOTE: The exact response shapes here are documented at
// https://github.com/jenkinsci/pipeline-stage-view-plugin/blob/master/README.md
// but field names will be confirmed against a real Jenkins during
// spike 1.2. This package returns raw bytes, so any mapping changes
// are confined to internal/schema (group 12).
// ---------------------------------------------------------------------------

// GetBuildStages fetches the stage tree of an in-flight or completed
// build via /wfapi/describe. Requires a resolved build number.
func (c *Client) GetBuildStages(ctx context.Context, ref *jenkinsurl.Ref) ([]byte, error) {
	if ref.BuildNumber == 0 {
		return nil, errors.New("jenkins: GetBuildStages requires a Ref with a non-zero BuildNumber")
	}
	return c.getJSON(ctx, ref.APIPath("wfapi/describe"), "")
}

// GetPendingInputs returns the list of paused `input` steps awaiting
// user action. Empty list (`[]`) means no pending inputs.
func (c *Client) GetPendingInputs(ctx context.Context, ref *jenkinsurl.Ref) ([]byte, error) {
	if ref.BuildNumber == 0 {
		return nil, errors.New("jenkins: GetPendingInputs requires a Ref with a non-zero BuildNumber")
	}
	return c.getJSON(ctx, ref.APIPath("wfapi/pendingInputActions"), "")
}

// InputParameterValue is one key/value pair to submit to a paused
// `input` step that declares parameters. Submitted via SubmitInput's
// classic `/input/<id>/submit` endpoint (see that method's doc for
// the wire format).
type InputParameterValue struct {
	Name  string
	Value string
}

// SubmitInput either proceeds or aborts a paused input step.
//
// Endpoint selection:
//   - proceed == false              → POST /input/<id>/abort
//     (parameters, proceedText, and proceedURL IGNORED with no
//     error; the CLI layer emits a warning when a user supplies -p
//     with abort)
//   - proceed == true, no parameters → POST /input/<id>/proceedEmpty
//     (v0.1 path, unchanged)
//   - proceed == true, parameters, proceedURL non-empty
//     → POST <Host><proceedURL> (the path Jenkins's wfapi advertises,
//     typically `/job/.../wfapi/inputSubmit?inputId=<id>`)
//   - proceed == true, parameters, proceedURL empty
//     → POST /input/<id>/submit (legacy fallback)
//
// The body in both parameterized cases is form-encoded:
//
//	Content-Type: application/x-www-form-urlencoded
//	json=<URL-encoded JSON of {"parameter":[{"name":..,"value":..}]}>&proceed=<proceedText>
//
// The `proceed=<proceedText>` field is REQUIRED — without it Jenkins
// treats the submission as ambiguous and rejects it with "Rejected by
// <user>", failing the build. proceedText is the `ok` label of the
// input step (the same value surfaced as PendingInput.OK in the
// schema), captured from /wfapi/pendingInputActions.
//
// The wfapi/inputSubmit URL was identified as the only endpoint that
// cleanly accepts parameterized submission during v0.2 dogfood;
// `/input/<id>/submit` returns HTTP 200 but records "Rejected by
// <user>" and aborts the build (confirmed against the deploy-input
// harness pipeline build #19/#20).
func (c *Client) SubmitInput(ctx context.Context, ref *jenkinsurl.Ref, inputID string, proceed bool, proceedText, proceedURL string, parameters []InputParameterValue) error {
	if ref.BuildNumber == 0 {
		return errors.New("jenkins: SubmitInput requires a Ref with a non-zero BuildNumber")
	}
	if inputID == "" {
		return errors.New("jenkins: SubmitInput requires a non-empty inputID")
	}

	if !proceed {
		return c.postEmptyInput(ctx, ref, inputID, "abort")
	}
	if len(parameters) == 0 {
		return c.postEmptyInput(ctx, ref, inputID, "proceedEmpty")
	}
	if proceedText == "" {
		return errors.New("jenkins: SubmitInput requires a non-empty proceedText when parameters are supplied")
	}
	return c.postSubmitInput(ctx, ref, inputID, proceedText, proceedURL, parameters)
}

// postEmptyInput POSTs an empty body to /input/<id>/<action>. Used
// for both `abort` and `proceedEmpty`.
func (c *Client) postEmptyInput(ctx context.Context, ref *jenkinsurl.Ref, inputID, action string) error {
	endpoint := ref.APIPath("input/" + url.PathEscape(inputID) + "/" + action)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, http.NoBody)
	if err != nil {
		return fmt.Errorf("jenkins: SubmitInput: build request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer closeBody(resp.Body)
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20)) //nolint:errcheck // best-effort drain
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &HTTPStatusError{URL: endpoint, StatusCode: resp.StatusCode, Status: resp.Status}
	}
	return nil
}

// postSubmitInput POSTs the parameterized form-encoded body.
// The inner JSON shape is fixed by Jenkins:
// {"parameter":[{"name":..,"value":..}, ...]}. The `proceed` form
// field carries the input step's `ok` label (proceedText) — Jenkins
// rejects the submission ("Rejected by <user>") if it is missing or
// does not match the declared label.
//
// When proceedURL is non-empty it is treated as a server-rooted path
// (e.g. `/job/svc/42/wfapi/inputSubmit?inputId=Deploy`) and joined
// with ref.Host. Otherwise the legacy `/input/<id>/submit` path is
// used.
//
// Note: proceedURL is joined with ref.Host (scheme+host only), NOT
// ref.APIPath, on purpose. Jenkins emits proceedURL already including
// its own context path (e.g. `/jenkins/job/...` on a context-path
// instance), so prepending the bare host is correct; using APIPath
// here would double the BasePath prefix.
func (c *Client) postSubmitInput(ctx context.Context, ref *jenkinsurl.Ref, inputID, proceedText, proceedURL string, parameters []InputParameterValue) error {
	type kv struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	payload := struct {
		Parameter []kv `json:"parameter"`
	}{Parameter: make([]kv, 0, len(parameters))}
	for _, p := range parameters {
		payload.Parameter = append(payload.Parameter, kv(p))
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("jenkins: SubmitInput: marshal parameters: %w", err)
	}

	form := url.Values{}
	form.Set("json", string(encoded))
	form.Set("proceed", proceedText)
	body := form.Encode()

	var endpoint string
	if proceedURL != "" {
		endpoint = ref.Host + proceedURL
	} else {
		endpoint = ref.APIPath("input/" + url.PathEscape(inputID) + "/submit")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("jenkins: SubmitInput: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer closeBody(resp.Body)
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20)) //nolint:errcheck // best-effort drain
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &HTTPStatusError{URL: endpoint, StatusCode: resp.StatusCode, Status: resp.Status}
	}
	return nil
}

// GetStageLog returns the raw stage-log JSON for a single stage,
// addressed by its flowNodeID (extracted upstream from the
// wfapi/describe response). Returns raw bytes; the mapper extracts the
// log text.
func (c *Client) GetStageLog(ctx context.Context, ref *jenkinsurl.Ref, flowNodeID string) ([]byte, error) {
	if ref.BuildNumber == 0 {
		return nil, errors.New("jenkins: GetStageLog requires a Ref with a non-zero BuildNumber")
	}
	if flowNodeID == "" {
		return nil, errors.New("jenkins: GetStageLog requires a non-empty flowNodeID")
	}
	endpoint := ref.APIPath("execution/node/" + url.PathEscape(flowNodeID) + "/wfapi/log")
	return c.getJSON(ctx, endpoint, "")
}

// GetNodeDescribe returns the raw wfapi/describe JSON for a single
// flow node. This is used to enumerate the child step nodes
// (stageFlowNodes) of a stage so per-stage logs can be assembled: the
// top-level stage node's own /wfapi/log endpoint reports length 0 on
// real Jenkins (the actual log text lives on the child step nodes).
//
// Discovered during e2e harness validation against jenkins/jenkins:lts-jdk21
// + workflow-aggregator; documented as spike finding for task 1.1.
func (c *Client) GetNodeDescribe(ctx context.Context, ref *jenkinsurl.Ref, flowNodeID string) ([]byte, error) {
	if ref.BuildNumber == 0 {
		return nil, errors.New("jenkins: GetNodeDescribe requires a Ref with a non-zero BuildNumber")
	}
	if flowNodeID == "" {
		return nil, errors.New("jenkins: GetNodeDescribe requires a non-empty flowNodeID")
	}
	endpoint := ref.APIPath("execution/node/" + url.PathEscape(flowNodeID) + "/wfapi/describe")
	return c.getJSON(ctx, endpoint, "")
}

// ---------------------------------------------------------------------------
// Console log streaming
// ---------------------------------------------------------------------------

// logStreamPollInterval bounds how often StreamConsoleLog polls when
// in follow mode and the previous response indicated more data is
// expected. 1s matches the cadence of Jenkins' web UI console viewer.
const logStreamPollInterval = 1 * time.Second

// StreamConsoleLog writes the build's console output to w. In
// non-follow mode it performs a single fetch; in follow mode it polls
// /logText/progressiveText incrementally using the X-Text-Size offset
// header until X-More-Data is absent or the context is cancelled.
func (c *Client) StreamConsoleLog(ctx context.Context, ref *jenkinsurl.Ref, w io.Writer, follow bool) error {
	if ref.BuildNumber == 0 {
		return errors.New("jenkins: StreamConsoleLog requires a Ref with a non-zero BuildNumber")
	}
	endpoint := ref.APIPath("logText/progressiveText")

	offset := int64(0)
	for {
		nextOffset, more, err := c.streamLogChunk(ctx, endpoint, offset, w)
		if err != nil {
			return err
		}
		offset = nextOffset
		if !follow || !more {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(logStreamPollInterval):
			// continue polling
		}
	}
}

// streamLogChunk performs a single GET /logText/progressiveText request
// starting at offset, writes the body to w, and returns the new offset
// and the "more data?" flag parsed from response headers. The response
// body is always closed before this function returns, on every path.
func (c *Client) streamLogChunk(ctx context.Context, endpoint string, offset int64, w io.Writer) (int64, bool, error) {
	u := endpoint + "?start=" + fmtInt(offset)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return offset, false, fmt.Errorf("jenkins: StreamConsoleLog: build request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return offset, false, err
	}
	defer closeBody(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10)) //nolint:errcheck // best-effort body capture for error context
		return offset, false, &HTTPStatusError{URL: u, StatusCode: resp.StatusCode, Status: resp.Status, Body: body}
	}

	// Stream the chunk directly to the caller's writer; large logs must
	// not buffer in memory. We cap any single chunk at 16 MiB as a
	// safety net.
	if _, err := io.Copy(w, io.LimitReader(resp.Body, 16<<20)); err != nil {
		return offset, false, fmt.Errorf("jenkins: StreamConsoleLog: write: %w", err)
	}

	// Advance offset for the next poll. X-Text-Size is the new total log
	// size that the next ?start= should request.
	nextOffset := offset
	if size := resp.Header.Get("X-Text-Size"); size != "" {
		if n, perr := parseInt(size); perr == nil {
			nextOffset = n
		}
	}
	more := resp.Header.Get("X-More-Data") == "true"
	return nextOffset, more, nil
}

// fmtInt is a tiny strconv-free formatter for non-negative int64s; we
// avoid the strconv import here to keep client.go focused.
func fmtInt(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// parseInt parses a non-negative decimal integer. Returns an error for
// any non-digit input so we can ignore malformed X-Text-Size headers.
func parseInt(s string) (int64, error) {
	var n int64
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not a number: %q", s)
		}
		n = n*10 + int64(c-'0')
	}
	return n, nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// getJSON issues a GET, validates the response status, and returns the
// body bytes. The tree parameter is appended as a query when non-empty
// so callers can write declarative filter expressions.
func (c *Client) getJSON(ctx context.Context, url, tree string) ([]byte, error) {
	if tree != "" {
		// Use Query escape so brackets/commas survive intact; Jenkins'
		// tree= grammar requires them.
		url = url + "?tree=" + queryEscape(tree)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("jenkins: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer closeBody(resp.Body)

	// Cap reads at 16 MiB. Jenkins responses for pipeline info top out
	// in the hundreds of KB; anything larger is a sign the server is
	// misbehaving and we should not OOM the CLI.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, fmt.Errorf("jenkins: read body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &HTTPStatusError{
			URL:        url,
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Body:       body,
		}
	}
	return body, nil
}

// queryEscape escapes a value for use in the query string. We use
// url.QueryEscape via net/url indirectly to avoid importing url here
// for one call (the package is already imported via http indirectly).
func queryEscape(s string) string {
	// Use a tiny inline escaper rather than importing net/url just to
	// avoid name collisions with this package's URL helpers. The
	// characters we need to escape in Jenkins tree expressions are
	// limited to the ones below.
	var b []byte
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch == ' ':
			b = append(b, '+')
		case isUnreserved(ch):
			b = append(b, ch)
		default:
			b = append(b, '%', hexDigit(ch>>4), hexDigit(ch&0x0F))
		}
	}
	return string(b)
}

func isUnreserved(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z',
		c >= 'a' && c <= 'z',
		c >= '0' && c <= '9':
		return true
	case c == '-' || c == '_' || c == '.' || c == '~':
		return true
	}
	return false
}

func hexDigit(n byte) byte {
	if n < 10 {
		return '0' + n
	}
	return 'A' + (n - 10)
}

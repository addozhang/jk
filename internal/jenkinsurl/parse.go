// Package jenkinsurl parses Jenkins job URLs into a structured reference
// and provides helpers used by every jk command that needs to address a
// pipeline.
//
// The parser intentionally does not classify segments as folder / pipeline /
// branch — that distinction requires Jenkins API context (the _class of each
// intermediate job) that the URL alone does not provide. See design.md §D7.
//
// Reference shape:
//
//	type Ref struct {
//	    Host        string   // "https://jenkins.foo.com" (scheme + host, default ports stripped)
//	    JobSegments []string // every /job/<name> segment in order, URL-decoded
//	    BuildNumber int      // 0 means "unspecified" — commands default to lastBuild
//	}
package jenkinsurl

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Ref is the parsed form of a Jenkins job URL.
//
// JobSegments is the raw, ordered list of `/job/<name>` segments from the
// URL with each name URL-decoded. The parser deliberately makes no attempt
// to identify which segments are folders, which is the pipeline, or whether
// the last segment is a branch of a multibranch job — Jenkins API context is
// required to disambiguate.
//
// BuildNumber and BuildPermalink are mutually exclusive: at most one is set
// for any given Ref. A URL like `/job/x/42/` sets BuildNumber=42; a URL like
// `/job/x/lastBuild/` sets BuildPermalink="lastBuild" and leaves BuildNumber
// at 0; a bare job URL `/job/x/` leaves both zero-valued.
type Ref struct {
	// Host is the credential-lookup key: scheme + lowercase hostname + port,
	// with default ports (`:80` for http, `:443` for https) stripped.
	Host string
	// JobSegments holds each `/job/<name>` segment in URL order, decoded.
	// Always non-empty for a successfully parsed Ref.
	JobSegments []string
	// BuildNumber is the explicit trailing build number from the URL, or 0
	// when the URL did not include one. Build-scoped commands treat 0 as
	// "use lastBuild" only when BuildPermalink is also empty.
	BuildNumber int
	// BuildPermalink carries a Jenkins symbolic build reference
	// (lastBuild, lastSuccessfulBuild, lastFailedBuild, lastStableBuild,
	// lastUnstableBuild, lastUnsuccessfulBuild, lastCompletedBuild) when
	// the URL's trailing segment matched one of those names. Mutually
	// exclusive with BuildNumber.
	BuildPermalink string
}

// HostKey returns the credential-lookup key for this Ref. It is identical
// to Ref.Host today; the accessor exists so callers express intent ("I'm
// looking up credentials") rather than reaching into the struct.
func (r *Ref) HostKey() string {
	return r.Host
}

// buildPermalinks is the closed allowlist of Jenkins symbolic build
// references recognized as trailing URL segments. The names mirror the
// public path constants on Jenkins core's PermalinkProjectAction (see
// jenkins-core's hudson.model.PermalinkProjectAction.Permalink) and are
// matched case-sensitively because Jenkins itself serves these paths
// case-sensitively.
var buildPermalinks = map[string]struct{}{
	"lastBuild":             {},
	"lastSuccessfulBuild":   {},
	"lastFailedBuild":       {},
	"lastStableBuild":       {},
	"lastUnstableBuild":     {},
	"lastUnsuccessfulBuild": {},
	"lastCompletedBuild":    {},
}

// APIPath returns a fully-qualified Jenkins URL formed from the Ref's host,
// every JobSegment joined by `/job/`, the optional BuildNumber or
// BuildPermalink, and an optional suffix (e.g. "api/json" or
// "wfapi/describe").
//
// Segments are re-encoded with url.PathEscape so spaces or other special
// characters survive the round trip. BuildPermalink is emitted as a literal
// path segment — Jenkins resolves it to a concrete build server-side. A
// leading `/` on suffix is stripped to avoid `//` in the output.
func (r *Ref) APIPath(suffix string) string {
	var b strings.Builder
	b.WriteString(r.Host)
	for _, seg := range r.JobSegments {
		b.WriteString("/job/")
		b.WriteString(url.PathEscape(seg))
	}
	switch {
	case r.BuildNumber > 0:
		b.WriteString("/")
		b.WriteString(strconv.Itoa(r.BuildNumber))
	case r.BuildPermalink != "":
		b.WriteString("/")
		b.WriteString(r.BuildPermalink)
	}
	if suffix != "" {
		b.WriteString("/")
		b.WriteString(strings.TrimPrefix(suffix, "/"))
	}
	return b.String()
}

// Parse converts a Jenkins job URL into a Ref. See the package documentation
// and openspec/changes/init-jk-jenkins-cli/specs/url-resolution/spec.md for
// the full set of accepted shapes and rejected inputs.
func Parse(raw string) (*Ref, error) {
	if raw == "" {
		return nil, errors.New("jenkinsurl: cannot parse empty URL")
	}

	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("jenkinsurl: parse %q: %w", raw, err)
	}

	switch u.Scheme {
	case "http", "https":
		// ok
	case "":
		return nil, fmt.Errorf("jenkinsurl: %q is missing a URL scheme (expected http or https)", raw)
	default:
		return nil, fmt.Errorf("jenkinsurl: %q uses unsupported scheme %q (expected http or https)", raw, u.Scheme)
	}

	if u.Host == "" {
		return nil, fmt.Errorf("jenkinsurl: %q is missing a host", raw)
	}

	segments, buildNumber, permalink, err := extractJobSegments(u.Path)
	if err != nil {
		return nil, fmt.Errorf("jenkinsurl: %q: %w", raw, err)
	}
	if len(segments) == 0 {
		return nil, fmt.Errorf("jenkinsurl: %q is not a Jenkins job URL (no /job/ segments found)", raw)
	}

	return &Ref{
		Host:           normalizeHost(u.Scheme, u.Host),
		JobSegments:    segments,
		BuildNumber:    buildNumber,
		BuildPermalink: permalink,
	}, nil
}

// extractJobSegments walks the path, asserts the alternating `job/<name>`
// pattern, decodes each name, and recognizes an optional trailing segment
// as either a numeric build number or a known Jenkins permalink.
//
// It distinguishes three failure modes:
//   - returns (nil, 0, "", nil) when the path is simply not a job URL (no
//     /job/ keyword, or a non-job path like /view/All/); the caller turns
//     this into the "not a Jenkins job URL" error;
//   - returns a descriptive error for malformed-but-job-shaped paths (empty
//     segments, decode failure);
//   - returns (segments, buildNumber, permalink, nil) on success — exactly
//     one of buildNumber>0 or permalink!="" is populated, or neither.
func extractJobSegments(path string) (segments []string, buildNumber int, permalink string, err error) {
	// Trim surrounding slashes so "/job/svc/" and "job/svc" produce the same
	// raw parts. We keep internal empties so "/job//job/svc/" still surfaces
	// the empty job segment.
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return nil, 0, "", nil
	}
	parts := strings.Split(trimmed, "/")

	// Detect optional trailing build reference: a leaf segment that follows
	// a `job/<name>` pair (i.e. the preceding token is not "job"). Probe
	// numeric first, then the permalink allowlist. Anything else falls
	// through so the alternation walker below rejects it as a non-job URL.
	if n := len(parts); n >= 2 && parts[n-2] != "job" {
		if bn, perr := strconv.Atoi(parts[n-1]); perr == nil && bn > 0 {
			buildNumber = bn
			parts = parts[:n-1]
		} else if _, ok := buildPermalinks[parts[n-1]]; ok {
			permalink = parts[n-1]
			parts = parts[:n-1]
		}
	}

	// A non-job URL: first token must be "job" for this to be a job URL at all.
	if parts[0] != "job" {
		return nil, 0, "", nil
	}

	// Walk pairs. We expect "job" / "<name>" alternation; any deviation in
	// the "job" slot means the URL isn't a job URL, while a missing/empty
	// name slot is a malformed job URL we should report explicitly.
	for i := 0; i < len(parts); i += 2 {
		if parts[i] != "job" {
			return nil, 0, "", nil
		}
		if i+1 >= len(parts) {
			// Trailing "/job/" with no name.
			return nil, 0, "", errors.New("empty job segment in URL path")
		}
		nameRaw := parts[i+1]
		if nameRaw == "" {
			return nil, 0, "", errors.New("empty job segment in URL path")
		}
		name, derr := url.PathUnescape(nameRaw)
		if derr != nil {
			return nil, 0, "", fmt.Errorf("decoding segment %q: %w", nameRaw, derr)
		}
		if name == "" {
			return nil, 0, "", errors.New("empty job segment in URL path")
		}
		segments = append(segments, name)
	}
	return segments, buildNumber, permalink, nil
}

// normalizeHost lowercases the hostname and strips the port when it matches
// the scheme's default (80 for http, 443 for https). It returns the canonical
// `scheme://host[:port]` form used as the credential lookup key.
func normalizeHost(scheme, host string) string {
	host = strings.ToLower(host)
	// host may already contain a port; split safely.
	if h, p, ok := splitHostPort(host); ok {
		if (scheme == "http" && p == "80") || (scheme == "https" && p == "443") {
			host = h
		}
	}
	return scheme + "://" + host
}

// splitHostPort is a tiny helper that accepts host strings with or without a
// port and returns (host, port, hadPort).
func splitHostPort(hostport string) (string, string, bool) {
	idx := strings.LastIndex(hostport, ":")
	if idx < 0 {
		return hostport, "", false
	}
	// Reject IPv6 literals — we do not support them today and bracket-aware
	// splitting belongs in net.SplitHostPort if/when needed.
	if strings.Contains(hostport[:idx], "]") || strings.Contains(hostport, "[") {
		return hostport, "", false
	}
	return hostport[:idx], hostport[idx+1:], true
}

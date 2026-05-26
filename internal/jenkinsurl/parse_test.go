package jenkinsurl

import (
	"reflect"
	"strings"
	"testing"
)

// Test_Parse_ValidURLs covers every "valid URL" scenario in
// openspec/changes/init-jk-jenkins-cli/specs/url-resolution/spec.md.
// Each row maps a raw URL to the expected Ref. See package documentation for
// the rationale behind keeping JobSegments raw (no folder/pipeline/branch
// classification) — design.md D7.
func Test_Parse_ValidURLs(t *testing.T) {
	tests := []struct {
		name    string
		rawURL  string
		want    Ref
		hostKey string // for §3 Map URL host to stored credentials
	}{
		{
			name:   "top-level pipeline with trailing slash",
			rawURL: "https://jenkins.foo.com/job/svc/",
			want: Ref{
				Host:        "https://jenkins.foo.com",
				JobSegments: []string{"svc"},
				BuildNumber: 0,
			},
			hostKey: "https://jenkins.foo.com",
		},
		{
			name:   "top-level pipeline without trailing slash",
			rawURL: "https://jenkins.foo.com/job/svc",
			want: Ref{
				Host:        "https://jenkins.foo.com",
				JobSegments: []string{"svc"},
				BuildNumber: 0,
			},
			hostKey: "https://jenkins.foo.com",
		},
		{
			name:   "multi-segment pipeline (three deep)",
			rawURL: "https://jenkins.foo.com/job/team/job/platform/job/svc/",
			want: Ref{
				Host:        "https://jenkins.foo.com",
				JobSegments: []string{"team", "platform", "svc"},
				BuildNumber: 0,
			},
			hostKey: "https://jenkins.foo.com",
		},
		{
			name:   "multi-segment pipeline with explicit build number",
			rawURL: "https://jenkins.foo.com/job/team/job/svc/job/main/42/",
			want: Ref{
				Host:        "https://jenkins.foo.com",
				JobSegments: []string{"team", "svc", "main"},
				BuildNumber: 42,
			},
			hostKey: "https://jenkins.foo.com",
		},
		{
			name:   "explicit non-default port preserved",
			rawURL: "http://jenkins.local:8080/job/svc/",
			want: Ref{
				Host:        "http://jenkins.local:8080",
				JobSegments: []string{"svc"},
				BuildNumber: 0,
			},
			hostKey: "http://jenkins.local:8080",
		},
		{
			name:   "default https port stripped",
			rawURL: "https://jenkins.foo.com:443/job/svc/",
			want: Ref{
				Host:        "https://jenkins.foo.com",
				JobSegments: []string{"svc"},
				BuildNumber: 0,
			},
			hostKey: "https://jenkins.foo.com",
		},
		{
			name:   "default http port stripped",
			rawURL: "http://jenkins.foo.com:80/job/svc/",
			want: Ref{
				Host:        "http://jenkins.foo.com",
				JobSegments: []string{"svc"},
				BuildNumber: 0,
			},
			hostKey: "http://jenkins.foo.com",
		},
		{
			name:   "url-encoded segment is decoded",
			rawURL: "https://jenkins.foo.com/job/my%20pipeline/",
			want: Ref{
				Host:        "https://jenkins.foo.com",
				JobSegments: []string{"my pipeline"},
				BuildNumber: 0,
			},
			hostKey: "https://jenkins.foo.com",
		},
		{
			name:   "query string and fragment are ignored",
			rawURL: "https://jenkins.foo.com/job/svc/?foo=bar#frag",
			want: Ref{
				Host:        "https://jenkins.foo.com",
				JobSegments: []string{"svc"},
				BuildNumber: 0,
			},
			hostKey: "https://jenkins.foo.com",
		},
		{
			name:   "build number with no trailing slash",
			rawURL: "https://jenkins.foo.com/job/svc/7",
			want: Ref{
				Host:        "https://jenkins.foo.com",
				JobSegments: []string{"svc"},
				BuildNumber: 7,
			},
			hostKey: "https://jenkins.foo.com",
		},
		{
			name:   "host is case-normalized to lower",
			rawURL: "https://Jenkins.FOO.com/job/svc/",
			want: Ref{
				Host:        "https://jenkins.foo.com",
				JobSegments: []string{"svc"},
				BuildNumber: 0,
			},
			hostKey: "https://jenkins.foo.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.rawURL)
			if err != nil {
				t.Fatalf("Parse(%q) returned unexpected error: %v", tt.rawURL, err)
			}
			if !reflect.DeepEqual(*got, tt.want) {
				t.Fatalf("Parse(%q):\n  got  %+v\n  want %+v", tt.rawURL, *got, tt.want)
			}
			if k := got.HostKey(); k != tt.hostKey {
				t.Errorf("HostKey() = %q; want %q", k, tt.hostKey)
			}
		})
	}
}

// Test_Parse_InvalidURLs covers the "Invalid URL rejected" scenarios.
// We assert on a substring of the error message so the message text can evolve
// without churning the test, but the key vocabulary is verified.
func Test_Parse_InvalidURLs(t *testing.T) {
	tests := []struct {
		name        string
		rawURL      string
		wantErrFrag string
	}{
		{
			name:        "no /job/ segments — view URL",
			rawURL:      "https://jenkins.foo.com/view/All/",
			wantErrFrag: "not a Jenkins job",
		},
		{
			name:        "no /job/ segments — bare host",
			rawURL:      "https://jenkins.foo.com/",
			wantErrFrag: "not a Jenkins job",
		},
		{
			name:        "empty string",
			rawURL:      "",
			wantErrFrag: "empty",
		},
		{
			name:        "garbage",
			rawURL:      "not a url",
			wantErrFrag: "scheme",
		},
		{
			name:        "scheme-less",
			rawURL:      "jenkins.foo.com/job/svc",
			wantErrFrag: "scheme",
		},
		{
			name:        "unsupported scheme",
			rawURL:      "ftp://jenkins.foo.com/job/svc/",
			wantErrFrag: "scheme",
		},
		{
			name:        "empty job segment",
			rawURL:      "https://jenkins.foo.com/job//job/svc/",
			wantErrFrag: "empty job segment",
		},
		{
			name:        "missing host",
			rawURL:      "https:///job/svc/",
			wantErrFrag: "host",
		},
		{
			name:        "trailing /job/ with no name",
			rawURL:      "https://jenkins.foo.com/job/",
			wantErrFrag: "empty job segment",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.rawURL)
			if err == nil {
				t.Fatalf("Parse(%q) expected error, got nil", tt.rawURL)
			}
			if !strings.Contains(err.Error(), tt.wantErrFrag) {
				t.Errorf("Parse(%q) error = %q; want substring %q", tt.rawURL, err.Error(), tt.wantErrFrag)
			}
		})
	}
}

// Test_Ref_APIPath verifies the URL builder used by HTTP requests. Joining
// JobSegments must use /job/ separators and include the build number only
// when set; the optional suffix is appended without intervening normalization
// so callers can pass full Jenkins API paths like "api/json?tree=...".
func Test_Ref_APIPath(t *testing.T) {
	tests := []struct {
		name   string
		ref    Ref
		suffix string
		want   string
	}{
		{
			name:   "single segment, no build, no suffix",
			ref:    Ref{Host: "https://jenkins.foo.com", JobSegments: []string{"svc"}},
			suffix: "",
			want:   "https://jenkins.foo.com/job/svc",
		},
		{
			name:   "single segment, no build, with suffix",
			ref:    Ref{Host: "https://jenkins.foo.com", JobSegments: []string{"svc"}},
			suffix: "api/json",
			want:   "https://jenkins.foo.com/job/svc/api/json",
		},
		{
			name:   "multi segment with build number and suffix",
			ref:    Ref{Host: "https://jenkins.foo.com", JobSegments: []string{"team", "svc", "main"}, BuildNumber: 42},
			suffix: "wfapi/describe",
			want:   "https://jenkins.foo.com/job/team/job/svc/job/main/42/wfapi/describe",
		},
		{
			name:   "suffix with leading slash is not duplicated",
			ref:    Ref{Host: "https://jenkins.foo.com", JobSegments: []string{"svc"}},
			suffix: "/api/json",
			want:   "https://jenkins.foo.com/job/svc/api/json",
		},
		{
			name:   "segment with space is URL-encoded in path",
			ref:    Ref{Host: "https://jenkins.foo.com", JobSegments: []string{"my pipeline"}},
			suffix: "api/json",
			want:   "https://jenkins.foo.com/job/my%20pipeline/api/json",
		},
		{
			name:   "build number only, no suffix",
			ref:    Ref{Host: "https://jenkins.foo.com", JobSegments: []string{"svc"}, BuildNumber: 7},
			suffix: "",
			want:   "https://jenkins.foo.com/job/svc/7",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ref.APIPath(tt.suffix); got != tt.want {
				t.Errorf("APIPath(%q) = %q; want %q", tt.suffix, got, tt.want)
			}
		})
	}
}

// Test_Parse_RoundTripsThroughAPIPath asserts the parser and the API path
// builder agree: parsing a URL and re-rendering it via APIPath("") must yield
// an equivalent URL (up to default-port stripping and case normalization).
func Test_Parse_RoundTripsThroughAPIPath(t *testing.T) {
	cases := []string{
		"https://jenkins.foo.com/job/svc",
		"https://jenkins.foo.com/job/team/job/svc",
		"https://jenkins.foo.com/job/team/job/svc/job/main/42",
		"http://jenkins.local:8080/job/svc",
	}

	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			ref, err := Parse(raw)
			if err != nil {
				t.Fatalf("Parse(%q) failed: %v", raw, err)
			}
			got := ref.APIPath("")
			if got != raw {
				t.Errorf("round-trip mismatch:\n  in:  %q\n  out: %q", raw, got)
			}
		})
	}
}

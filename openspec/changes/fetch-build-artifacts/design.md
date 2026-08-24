## Context

Jenkins core exposes archived artifact metadata on a build's Remote Access API and serves each file beneath the build's `artifact/` path. The existing client already resolves numeric and permalink build references, applies authentication, follows redirects, and streams console logs through an `io.Writer`; however, its JSON helper buffers responses and the CLI has no safe filesystem download path.

Artifact content may be large or stored by an external Artifact Manager that redirects requests. Artifact relative paths are server-controlled input and therefore cannot be joined to a local directory without validation.

## Goals / Non-Goals

**Goals:**

- List archived artifact metadata for any supported build reference.
- Download one artifact to an explicit file or all artifacts beneath an explicit directory.
- Stream content with bounded memory and leave no completed-looking partial file after failure.
- Preserve Jenkins artifact relative paths for bulk downloads without permitting traversal or symlink escape.
- Reuse existing authentication, redirects, timeout, error translation, and structured output conventions.

**Non-Goals:**

- Discovering or interpreting report-plugin APIs.
- Fetching Pipeline stashes, workspaces, console logs, or files not archived by Jenkins.
- Depending on Jenkins's `*zip*` directory-browser syntax or creating a local aggregate archive.
- Concurrent downloads, resume/range support, checksum verification, or progress bars in the first version.

## Decisions

### Use explicit list, single-file, and bulk command forms

The CLI will expose:

- `jk build artifacts <build-url>` for structured metadata.
- `jk build artifact <build-url> <relative-path> --destination <file>` for one file.
- `jk build artifacts fetch <build-url> --directory <dir>` for all files.

Explicit destination flags prevent surprising writes and avoid conflicting with the root `-o/--output` format flag. A singular command makes single-file intent clear, while bulk fetch remains grouped under the plural artifact collection.

### List through the build Remote Access API

The client will request `api/json?tree=number,url,artifacts[fileName,relativePath]`. Including the resolved build identity allows permalink input to produce concrete `buildUrl` and `buildNumber` output. Artifact order will follow Jenkins's response.

### Download individual content paths rather than Jenkins ZIP bundles

Each file will be fetched from `artifact/<path-segment-encoded-relative-path>`. Bulk fetch first lists metadata and then downloads each entry sequentially. This uses Jenkins's core artifact model, works with custom Artifact Managers and redirects, supports per-file error reporting, and avoids relying on the special `*zip*` UI-serving syntax.

URL construction will encode each relative-path segment while retaining `/` separators. It will not concatenate a pre-escaped path into a URL string.

### Stream into a same-directory temporary file

Downloads will copy the response body directly to a temporary file in the destination file's parent directory. After close succeeds, the CLI will atomically rename it to the final path. Any HTTP, copy, close, or context error removes the temporary file. Existing final paths are rejected unless `--force` is supplied; overwrite replacement is performed only after the complete temporary file exists.

The download method will bypass the bounded JSON helper. Debug transport must not buffer or dump artifact response bodies; it may log request and response metadata only.

### Treat artifact paths as untrusted input

Bulk fetch will reject empty paths, absolute paths, `.` or `..` components, NUL bytes, and paths whose normalized destination escapes the requested root. Existing parent components must be real directories and must not be symbolic links. The final destination must not be a directory or symlink. These checks are performed before creating each temporary file.

Single-file fetch requires the requested relative path to exactly match an entry returned by the artifact list before downloading. This both validates the path and avoids turning the artifact endpoint into an arbitrary relative URL fetcher.

### Stop bulk fetch on the first failure

Bulk download is sequential and fail-fast. Files already completed remain in place, the failing file leaves no final or temporary output, and the error identifies its artifact path. This is simpler and more diagnosable than rollback of already downloaded files, which would risk deleting pre-existing files when `--force` is used.

## Risks / Trade-offs

- [Artifacts disappear between listing and download due to retention] → Surface the failing path and Jenkins HTTP error; retain previously completed files.
- [Bulk fetch is slower than server-side ZIP] → Prefer stable per-file semantics and bounded memory; concurrency can be added later without changing commands.
- [Filesystem checks cannot make hostile concurrent local mutation impossible] → Validate immediately before creation, use exclusive temporary files, and document that the destination must be trusted local storage.
- [External Artifact Managers redirect to another host] → Reuse Go redirect handling while ensuring credentials are not forwarded to unrelated hosts by the standard client policy.
- [Force replacement differs across operating systems] → Implement and test supported-platform replacement semantics without exposing partial destination files.

# Spikes

This directory holds the conformance corpora and discovery notes that
informed the v0.1 / v0.2 design.

## What's here

- `urls.txt` — 12 Jenkins URL shapes that `internal/jenkinsurl.Parse`
  must handle correctly. Mirrors the table-driven cases in
  `internal/jenkinsurl/parse_test.go`. Tracked as task 1.4 of the
  `init-jk-jenkins-cli` change.

## What's _not_ here (and where to look instead)

Tasks 1.1–1.3 of the `init-jk-jenkins-cli` change called for separate
`wfapi-stages/`, `wfapi-input/`, and `wfapi-logs/` spike folders with
raw captures. In practice the spike learnings flowed directly into:

- `openspec/changes/init-jk-jenkins-cli/design.md` — `wfapi`-based
  shape decisions for stages / per-stage logs / pending-input
  enrichment.
- `openspec/changes/add-input-parameter-submission/design.md` — input
  submit wire format, `proceedText` requirement, and the wfapi
  `proceedUrl` discovery that fixed the v0.2 "Rejected by &lt;user&gt;" bug.
- `internal/schema/mapper_build.go` (`rawWfapiInputParam`) —
  reference implementation of the actual wfapi input-action shape.
- `test/e2e/jenkins/pipelines/*.Jenkinsfile` — live pipelines that
  exercise the discovered shapes (notably `deploy-input.Jenkinsfile`
  for parameterized input + `wfapi/inputSubmit` URL).

If you need raw response samples, run the e2e harness
(`docker compose up -d` from `test/e2e/jenkins/`) and capture from
`http://localhost:18080/job/&lt;pipeline&gt;/&lt;build&gt;/wfapi/...`.

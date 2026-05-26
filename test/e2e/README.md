# jk end-to-end test harness

This directory hosts a self-contained Jenkins instance plus a Go test
suite that drives the compiled `jk` binary against it. It exists so we
can validate jk's behavior against a real Jenkins LTS server without
relying on a developer's personal instance.

## Quick start

```bash
make e2e-up         # build the Jenkins image, start the container, wait healthy
make test-e2e       # go test -tags=e2e ./test/e2e/...
make e2e-down       # stop and remove (deletes the jenkins_home volume)
```

The first `e2e-up` takes 1–3 minutes because the Jenkins image is built
locally with all required plugins. Subsequent runs reuse the image.

## What the harness creates

Once Jenkins is healthy at `http://localhost:18080`, the following exist:

- An admin user `admin` with a fixed API token `jk-e2e-fixed-token`
  (overridable via `test/e2e/.env`).
- Three seeded pipelines:
  - `hello` — single-stage, prints a greeting.
  - `params` — declares string + boolean parameters.
  - `team/parallel` — folder + multi-stage with a parallel block.
- One completed build per pipeline (created by the Go harness's
  `setupHarness`, not by JCasC).

## File layout

```
test/e2e/
  README.md                  this file
  docker-compose.yml         single-service compose; maps Jenkins to :18080
  .env                       committed harness-only credentials
  jenkins/
    Dockerfile               FROM jenkins/jenkins:lts-jdk21 + plugins
    plugins.txt              minimum plugin set
    jcasc/jenkins.yaml       Configuration-as-Code: admin user, token, jobs
    pipelines/               seed Jenkinsfiles (baked into image)
  setup_test.go              builds jk, seeds credentials, warms build history
  auth_e2e_test.go           jk auth add/list/remove
  pipeline_e2e_test.go       jk pipeline info/params/list
  build_e2e_test.go          jk build status/stages/logs
```

## Environment overrides

`go test -tags=e2e ./test/e2e/...` reads these env vars; the defaults
match `test/e2e/.env`:

| Var              | Default                 | Used for                  |
|------------------|-------------------------|---------------------------|
| `JK_E2E_URL`     | `http://localhost:18080`| Jenkins base URL          |
| `JK_E2E_USER`    | `admin`                 | API user                  |
| `JK_E2E_SECRET`  | `admin-password`        | Basic-auth secret (password or API token) |

Set them when running against a Jenkins started outside of compose
(e.g. against an existing dev instance).

## Security note

The committed `.env` contains a fixed admin password, and `nginx/server.crt` + `nginx/server.key` are a self-signed TLS pair for `localhost`. Both are safe only because the Jenkins server is local, throwaway, and not reachable from outside the developer's machine. **Never** reuse them in any real Jenkins instance. The harness uses the password (not an API token) as the Basic-auth secret because the apitoken-property plugin has no public API for injecting a known-plaintext token.

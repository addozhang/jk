## 1. Auth WhoAmI

- [x] 1.1 Add Jenkins whoAmI client call and experimental output mapping
- [x] 1.2 Add CLI URL/context-path handling and identity-specific error translation
- [x] 1.3 Test authenticated, anonymous, missing-endpoint, and context-path behavior

## 2. Build Rebuild

- [x] 2.1 Add rebuild orchestration using recorded build parameters and existing trigger output
- [x] 2.2 Validate recorded parameter names against current pipeline definitions before triggering
- [x] 2.3 Reject redacted and unsupported parameter values without issuing a trigger
- [x] 2.4 Test parameterized, unparameterized, drift, redacted, permalink, and context-path behavior

## 3. Documentation And Verification

- [x] 3.1 Document both commands in README and the public schema
- [x] 3.2 Run formatting, unit tests, vet, lint, and OpenSpec validation
- [x] 3.3 Prepare the v0.7.0 release notes and record that schemaVersion remains 1

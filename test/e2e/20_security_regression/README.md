<!-- Copyright (c) 2026 JAAB Tech SAS, Uruguay All Rights Reserved -->
<!-- See https://jaab.tech -->

# Security Regression E2E Test

## Objective

Fast, standalone coverage of `fluxrig-explained/docs/17-deep-review.md`'s
"Mixer, store and enrollment" findings, against real running binaries. Run it
alone to check a change here without waiting on the full regression sweep:

```
bash test/e2e/20_security_regression/run.sh
```

It also runs as part of `make regression` (auto-discovered like every suite
under `test/e2e/`), so it stays part of the permanent gate too.

## Verifications

- The Mixer refuses unauthenticated and wrongly-authenticated requests to a
  real route (`GET /racks`) with 401, while `/health` stays open with no
  token, using a real `api.auth_token` (unlike the rest of this suite, which
  runs with `FLUXRIG_API_AUTH_DISABLED_DANGEROUSLY=true` for convenience).
- The correct bearer token is accepted.
- A Rack enrolls over NATS via Zero-Config, using `rack.bootstrap_secret`,
  entirely independent of the HTTP API's own auth (the Rack never calls the
  HTTP API to enroll).
- The enrolled Rack's secret never appears in an authenticated `GET /racks`
  response body.
- A second Rack claiming the same name, presenting only the same public
  bootstrap secret every Zero-Config Rack knows, is rejected: the first
  Rack's real secret is a freshly generated one, not the bootstrap value, so
  knowing the bootstrap secret alone must never be enough to hijack an
  enrolled name.

## Extending

As PR2-PR6 of the review's remediation land, add their fast-checkable
behavior here rather than only in the full regression suite.

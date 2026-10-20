# Contributing to fluxrig

Thank you for your interest in contributing to **fluxrig**!

We welcome contributions of all types: bug fixes, features, documentation, and research.

## How to Contribute

To ensure a smooth transition and maintain our high standards, please follow our full contributing guide:

**[Full Contributing Guide](https://fluxrig.org/docs/development/contributing)**

## Every pull request needs an issue

This is a rule, not a suggestion:

1. **Open an issue first.** Describe the problem or the change. One issue per matter.
2. **Agree on the approach on the issue.** For a large change, wait for a maintainer to agree before you write code. This saves you a rewrite.
3. **Open the pull request and reference the issue.** Put `Closes #<issue>` in the pull request body, or `Refs #<issue>` when the pull request is one part of a larger issue.

A pull request with no linked issue is closed and redirected to an issue. The issue keeps the reason for the change in one place, and it lets the maintainers review the idea before the code.

## Branching & History Policy

fluxrig follows a strict **PR-First** workflow to maintain history integrity:

1.  **Target the `dev` Branch**: Community pull requests target `dev`. Public `main` is rebuilt from the internal release tag at each release, so a change merged into `main` is replaced by the next release snapshot. Maintainers port each accepted change from `dev` into the release line.
2.  **History Hardening**: On **2026-04-29**, the project underwent a significant history synchronization event to ensure a unified, complete lineage.
    - If your fork was created before this date, you MUST **re-clone** or **reset** your local `main` and `dev` branches to the current upstream state.
3.  **DCO Sign-off**: Every commit must be signed off (`git commit -s`) to certify compliance with the Developer Certificate of Origin.

### Quick Summary

1.  **Open an issue** and agree on the change.
2.  **Fork** the repository and create your feature branch from `dev`.
3.  Follow our **[Coding Standards](https://fluxrig.org/docs/development/standards)** (Go, Slog, OTel).
4.  Ensure all changes include **Unit Tests**.
5.  Submissions must include a **DCO Sign-off** (`git commit -s`).
6.  Open the pull request against `dev` and put `Closes #<issue>` in its body.
7.  All dependencies must use **Permissive Licenses** (Apache, MIT, BSD).

### Development Environment

Documentation for setting up your local environment can be found here:
**[Local Development Setup](https://fluxrig.org/docs/development/environment)**

## Scope of this repository

This repository holds the open-source engine and the gears the project publishes. The commercial gears (`sim_source`, `sim_responder`) live in the **[enterprise repository](https://github.com/jaab-tech/fluxrig-ent)** under the Business Source License. A pull request here must not depend on them. If your change needs one of them, say so on the issue first.

## Code of Conduct

By participating in this project, you agree to abide by our **[Code of Conduct](CODE_OF_CONDUCT.md)**.

## Questions?

If you have questions or need help, please open a discussion on **[GitHub Discussions](https://github.com/jaab-tech/fluxrig/discussions)**.

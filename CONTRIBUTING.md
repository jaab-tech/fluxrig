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

1.  **Target the `dev` Branch**: Community pull requests target `dev`. A maintainer reviews your pull request and squash-merges it on `dev`. The next release then carries your commit, under your name, into the history of `main`. Public `main` is the release line: only a release changes it, so do not target it.
2.  **History Hardening**: On **2026-04-29**, the project underwent a significant history synchronization event to ensure a unified, complete lineage.
    - If your fork was created before this date, you MUST **re-clone** or **reset** your local `main` and `dev` branches to the current upstream state.
3.  **DCO Sign-off**: Every commit must be signed off (`git commit -s`) to certify compliance with the Developer Certificate of Origin.
4.  **Do not edit `CHANGELOG.md`**: The maintainers write the entry when they merge your change, and they credit you in it. See [The changelog](#the-changelog).
5.  **No changes under `.github/` or `.claude/`**: The maintainers keep the workflows and the repository configuration. A check fails a community pull request that changes them. If a workflow needs a change, describe it in an issue.

### The changelog

Leave `CHANGELOG.md` out of your pull request. The maintainers write the entry when they merge your change, and they credit you in it by name.

The reason is practical. Every change adds a line to the same section, `[Unreleased]`. Two pull requests that both edit it conflict with each other, and the conflict has nothing to do with the code. One author for the entries also keeps their wording and their order the same.

If your pull request already changes `CHANGELOG.md`, remove that change before a maintainer merges it.

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

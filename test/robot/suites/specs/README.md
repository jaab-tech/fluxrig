# Spec suite

What a spec has to declare, what the store does with it, and what the Mixer and
the CLI render from it.

## Why this suite exists

A spec is a deployed artefact, and the rules that make one referable were
unenforced for most of the project's life. The schema asked for a version,
nothing read it, and a spec with no version at all loaded clean. The
content-addressed store read neither the declared name nor the declared version,
so the reference spec — which says `2.2.0` — was filed as `v0.1.0`, the same file
imported twice became two versions of itself with identical hashes, and the
immutability check that already existed could never fire.

None of that produced an error. It produced plausible, wrong answers, which is
what this suite is for.

## The three files

| Suite | What it holds to account |
|:---|:---|
| `spec_contract.robot` | A spec declares who it is and which version it is; the store files it under what it declared; one name and version can never mean two documents. |
| `spec_doc.robot` | The reference the CLI renders: both formats, both scopes, what the public variant withholds, and whether a reader who clicks arrives anywhere. |
| `spec_api.robot` | The Mixer serving stored specs and their reference — the spec an operator wants to read is the one that is running, and that is the one they cannot open from their own disk. |
| `spec_listing.robot` | What the store can be asked about what it holds, from both surfaces: that a listing says which kind of thing it is listing, that it carries when a version was filed and which one `latest` reaches, and that a history is ordered by version rather than by arrival. |

`spec_contract` and `spec_doc` drive the CLI only and need no processes.
`spec_api` starts a Mixer on port 8095 with a spec already in its store, because
the store is opened at boot. `spec_listing` starts its own on 8096 with four
versions of one spec and a scenario: two suites sharing a port means the second
one questions the first one's store and is answered, which is a failure that
reads like a bug in the code.

## Running it

```bash
make test-robot-specs
```

`run.sh` refuses to run against stale binaries, so use the make target rather
than calling it directly: a suite that exercises a binary built before your edit
reports a real failure as a pass.

## What is deliberately not here

The store's own concurrency and the scenario-side integration are covered by
`test/e2e/12_specs`. This suite is about the contract a spec states and the
documents derived from it.

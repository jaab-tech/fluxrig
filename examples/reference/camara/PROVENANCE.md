# Vendored CAMARA API definitions

These files are copied verbatim from the CAMARA project and are **not edited
here**. They are vendored so the tutorial builds against a fixed contract and so
its tests do not reach the network to run.

| File | Source | Tag | API version | Upstream blob SHA |
|:---|:---|:---|:---|:---|
| `device-roaming-status.yaml` | [camaraproject/DeviceStatus](https://github.com/camaraproject/DeviceStatus) `code/API_definitions/device-roaming-status.yaml` | `r2.2` | 1.0.0 | `7a0153d2ed1d8972397698fcd298c9c060987458` |

CAMARA publishes under Apache 2.0, the same licence this repository uses.

## Verifying a copy

The SHA above is the git blob hash of the upstream file, so a copy can be checked
without trusting this directory:

```
git hash-object examples/reference/camara/device-roaming-status.yaml
gh api /repos/camaraproject/DeviceStatus/contents/code/API_definitions/device-roaming-status.yaml?ref=r2.2 --jq .sha
```

## Where this API lives now

CAMARA has split `DeviceStatus` into one repository per API, and roaming moved to
[camaraproject/DeviceRoamingStatus](https://github.com/camaraproject/DeviceRoamingStatus).
That is the repository to watch: `DeviceStatus` last saw a release in March 2025
and a commit in December 2025, while the dedicated one released `r2.1` in August
2026.

The pin stays where it is anyway, and the reason is worth recording. The vendored
file is `1.0.0`, a stable version; the newer repository's latest release carries
`1.2.0-rc.3`, a release candidate. A worked example is better built on a stable
contract than on one still moving, and the part this example depends on has not
changed between them: the operation is still `POST /retrieve`, `roaming` is
still required, and `countryName` is still an array whose emptiness means the
code maps to no country.

Two differences to carry into a re-pin. The candidate adds `maxItems: 5`, which
only confirms the cap on the multi-country case. It also makes
`lastStatusTime` required alongside `roaming`, which this example never reads
but which a consumer treating the response as validated would notice.

Re-pin when `DeviceRoamingStatus` publishes a stable release.

## Updating

Pin a **release tag**, never the default branch: CAMARA develops in the open and
the default branch carries `version: wip`. Bump the tag deliberately, re-run the
tutorial's suite, and record what changed in the contract, because a released
CAMARA API can still change shape between major tags.

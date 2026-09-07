#!/bin/bash
# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0
#
# Fail if a file that reaches the public repository names internal-only
# infrastructure.
#
# This repository is mirrored wholesale: the public tree is rebuilt from the
# release tag with `git checkout <tag> -- .`, which restores every tracked path
# regardless of .gitignore. Only a short exclusion list is stripped. So a file
# added here for the convenience of whoever is working in the repo is published
# on the next release, and the internal repository names, the factory/showroom
# split and the release path are not public.
#
# Verified against the v0.9.0 public tree: none of these terms appears in it.
# This guard exists so that stays true by construction rather than by memory.
set -uo pipefail

# Paths stripped by the release mirror; internal content is expected there.
EXCLUDED_FROM_MIRROR=(".claude" ".github/dependabot.yml")

FORBIDDEN=(
    "fluxrig-internal"
    "fluxrig-ops"
    "fluxrig-docs"
    "main-internal"
    "publish_workspace"
    "git/infra"
)

# A home directory in a mirrored file names the person who wrote it and only
# works on their machine. One was load-bearing in a test: the suite replaced a
# literal /Users/<name>/... string, so the path had to stay wrong to stay
# working. Placeholders are fine; a real account is not.
FORBIDDEN_PATTERNS=(
    "/home/[a-z][a-z0-9_-]*/"
    "/Users/[a-z][a-z0-9_-]*/"
)
# Placeholders that are obviously not somebody's account.
PATTERN_ALLOW="/Users/you/|/home/you/|/home/user/|/Users/user/|/home/runner/"

fail=0
while IFS= read -r file; do
    for skip in "${EXCLUDED_FROM_MIRROR[@]}"; do
        [[ "$file" == "$skip"* ]] && continue 2
    done
    # This file lists the forbidden terms, so it necessarily contains them.
    [[ "$file" == "scripts/check_no_internal_leaks.sh" ]] && continue
    for pattern in "${FORBIDDEN_PATTERNS[@]}"; do
        # Every match that is not one of the accepted placeholders is a real
        # account name, and one is enough.
        hit=$(grep -oE -- "$pattern" "$file" 2>/dev/null | grep -vE "$PATTERN_ALLOW" | head -1)
        if [ -n "$hit" ]; then
            echo "❌ $file carries a home directory ($hit), which names a person and only works on their machine."
            fail=1
        fi
    done
    for term in "${FORBIDDEN[@]}"; do
        if grep -qF -- "$term" "$file" 2>/dev/null; then
            echo "❌ $file names '$term', which is internal-only and would be published."
            fail=1
        fi
    done
# Every tracked text file, not just markdown and YAML: the leak that prompted the
# home-directory check was in a .py, and a Go comment naming the private remote
# would have published just as loudly.
done < <(git ls-files 2>/dev/null | grep -viE '\.(png|jpg|jpeg|gif|svg|ico|pdf|zip|gz|wasm|woff2?|ttf)$')

if [ $fail -ne 0 ]; then
    echo ""
    echo "Internal-only guidance belongs under .claude/, which the release mirror strips."
    exit 1
fi
echo "✅ no internal-only references in mirrored files"

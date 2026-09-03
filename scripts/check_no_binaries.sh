#!/usr/bin/env bash
#
# Refuse to keep compiled executables under version control.
#
# The Makefile builds every binary into bin/, which .gitignore covers. A stray
# `go build -o iso8583-tool ./cmd/iso8583-tool` writes to the repository root
# instead, where no ignore rule catches it, and the artifact rides along into
# the public mirror. Extensions do not help: a Go binary on macOS or Linux has
# none, so this reads the magic number instead.
set -euo pipefail

fail=0

while IFS= read -r path; do
    [ -f "$path" ] || continue

    magic=$(od -An -tx1 -N4 "$path" 2>/dev/null | tr -d ' \n')
    case "$magic" in
        # ELF (Linux), Mach-O 32/64 in either byte order, universal binary
        7f454c46|cffaedfe|cefaedfe|feedfacf|feedface|cafebabe)
            echo "  $path" >&2
            fail=1
            ;;
        # PE/COFF (Windows) — 'MZ' plus whatever follows
        4d5a*)
            echo "  $path" >&2
            fail=1
            ;;
    esac
done < <(git ls-files)

if [ "$fail" -ne 0 ]; then
    cat >&2 <<'MSG'

Compiled executables are tracked in git (listed above).

Binaries belong in bin/, which .gitignore covers. Build with `make build`
rather than `go build -o <name>`, which drops the artifact in the repository
root. To untrack one: git rm --cached <path>
MSG
    exit 1
fi

echo "No compiled executables are tracked."

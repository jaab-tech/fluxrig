# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

"""Where the suites find the binaries they exercise.

The binaries live in <repository>/bin by default. Setting FLUXRIG_BIN_DIR points
the suites at another directory that holds the same file names: fluxrig,
fluxrig-mixer and iso8583-tool. A suite kept in another repository uses it to
run against a build of the binaries that this repository does not produce.
"""

import os


def bin_dir(root):
    """Return the directory that holds the binaries for the repository at root."""
    return os.environ.get("FLUXRIG_BIN_DIR") or os.path.join(root, "bin")

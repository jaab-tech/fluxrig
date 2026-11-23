#!/bin/bash
# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

# Usage: require_free_ports.sh PORT...
#
# Fails when something listens on one of the ports, and names it. The tests
# never kill what they did not start: a port says nothing about who owns the
# process on it, and the host can be shared.
#
# The check is a bind, released at once. It sees the listeners of other users,
# which lsof does not show without privileges, and unlike a connection it does
# not touch them, and cannot block on one that does not accept.

WAIT="${FLUXRIG_PORT_WAIT:-5}" # seconds for a port that is being released

busy() {
    ! python3 -I - "$1" <<'PY'
import errno, socket, sys

port = int(sys.argv[1])
for family, addr in ((socket.AF_INET, "0.0.0.0"), (socket.AF_INET, "127.0.0.1"), (socket.AF_INET6, "::")):
    try:
        with socket.socket(family, socket.SOCK_STREAM) as s:
            # A connection in TIME_WAIT is not a listener.
            s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            s.bind((addr, port))
    except OSError as e:
        if e.errno == errno.EADDRINUSE:
            sys.exit(1)
PY
}

rc=0
for port in "$@"; do
    deadline=$((SECONDS + WAIT))
    while busy "$port"; do
        if (( SECONDS >= deadline )); then
            echo "port $port is in use by a process this run did not start; it is left alone:" >&2
            owner=$(lsof -nP -iTCP:"$port" -sTCP:LISTEN 2>/dev/null | tail -n +2)
            echo "  ${owner:-owner not visible to this user (another user, or a container)}" >&2
            rc=1
            break
        fi
        sleep 0.2
    done
done
exit $rc

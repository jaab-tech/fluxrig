# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

"""A terminal and an upstream that speak only length-prefixed frames, for the
connection events suite. Neither one sends anything: the point of the suite is
that a frame arrives first."""

import socket
import struct


class ConnEventsLibrary:
    ROBOT_LIBRARY_SCOPE = "SUITE"

    def __init__(self):
        self._socks = {}
        self._listener = None

    def connect_terminal(self, port, alias="terminal"):
        """Connect to a port and keep the connection under alias."""
        self._socks[alias] = socket.create_connection(("127.0.0.1", int(port)), timeout=5)

    def listen_as_upstream(self, port):
        """Listen on a port, as the peer of a client gear."""
        self._listener = socket.socket()
        self._listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        self._listener.bind(("127.0.0.1", int(port)))
        self._listener.listen(5)

    def accept_upstream(self, timeout=30, alias="upstream"):
        """Wait for the client gear to dial, and keep the connection under alias."""
        self._listener.settimeout(float(timeout))
        conn, _ = self._listener.accept()
        self._socks[alias] = conn

    def close_connection(self, alias="terminal"):
        """Close a connection."""
        self._socks.pop(alias).close()

    def close_all_connections(self):
        """Close every connection and the listener."""
        for conn in self._socks.values():
            conn.close()
        self._socks.clear()
        if self._listener:
            self._listener.close()
            self._listener = None

    def _read_frame(self, conn):
        head = self._read_exact(conn, 2)
        length = struct.unpack(">H", head)[0]
        return self._read_exact(conn, length).decode("ascii", "replace")

    @staticmethod
    def _read_exact(conn, count):
        data = b""
        while len(data) < count:
            chunk = conn.recv(count - len(data))
            if not chunk:
                raise AssertionError(f"the connection closed after {len(data)} of {count} bytes")
            data += chunk
        return data

    @staticmethod
    def _logon_fields(frame):
        """Read DE 7, DE 11 and DE 70 of a logon. The bitmaps are ASCII hex, and
        these three fields have a fixed length."""
        widths = {7: 10, 11: 6, 70: 3}
        pos = 4
        bitmap = int(frame[pos : pos + 16], 16)
        pos += 16
        present = [i + 1 for i in range(64) if bitmap >> (63 - i) & 1]
        if 1 in present:
            second = int(frame[pos : pos + 16], 16)
            pos += 16
            present = present[1:] + [65 + i for i in range(64) if second >> (63 - i) & 1]
        fields = {}
        for field in present:
            if field not in widths:
                raise AssertionError(f"unexpected DE {field} in {frame!r}")
            fields[field] = frame[pos : pos + widths[field]]
            pos += widths[field]
        return fields

    def logon_should_arrive(self, timeout, alias="terminal"):
        """Fail unless a sign-on 0800 with its date and time arrives on the
        connection within timeout seconds. Return its STAN."""
        conn = self._socks[alias]
        conn.settimeout(float(timeout))
        try:
            frame = self._read_frame(conn)
        except socket.timeout as exc:
            raise AssertionError(f"no frame on {alias} in {timeout}s") from exc
        if not frame.startswith("0800"):
            raise AssertionError(f"expected a 0800 on {alias}, got {frame!r}")
        fields = self._logon_fields(frame)
        if fields.get(70) != "001":
            raise AssertionError(f"expected a sign-on in DE 70, got {frame!r}")
        stamp = fields.get(7, "")
        if len(stamp) != 10 or not stamp.isdigit():
            raise AssertionError(f"expected MMDDhhmmss in DE 7, got {frame!r}")
        return int(fields[11])

    def no_frame_should_arrive(self, timeout, alias="terminal"):
        """Fail if a frame arrives on the connection within timeout seconds."""
        conn = self._socks[alias]
        conn.settimeout(float(timeout))
        try:
            frame = self._read_frame(conn)
        except socket.timeout:
            return
        raise AssertionError(f"expected no frame on {alias}, got {frame!r}")

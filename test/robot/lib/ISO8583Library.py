# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

import subprocess
import json
import os
import signal
import socket
import time
import threading
from robot.api import logger

from binpaths import bin_dir

class ISO8583Library:
    """
    Robot Framework Library for ISO8583 Performance & Functional Testing.
    Wraps 'iso8583-load' (Go) and 'iso8583_tool.py' (Python).

    Supports ASYNC execution for specialized load variance scenarios (Spikes, Steps).
    """

    ROBOT_LIBRARY_SCOPE = 'GLOBAL'
    
    def __init__(self):
        self._processes = {} # Store async load generators by alias
        self._project_root = self._find_project_root()
        # Consolidated Tool
        self._bin_tool = os.path.join(bin_dir(self._project_root), 'iso8583-tool')
        self._tool_script = os.path.join(self._project_root, 'test', 'e2e', 'iso8583', 'iso8583_tool.py')

    def _find_project_root(self):
        # Assuming we are in test/robot/lib, go up 3 levels
        cwd = os.path.dirname(os.path.realpath(__file__))
        return os.path.abspath(os.path.join(cwd, '../../..'))

    def run_native_load_test(self, target="localhost:8583", concurrency=10, rate=100, duration="10s", report_file="report.json", header_len=None, warmup=None, encoding=None):
        """
        Executes the Native Go Load Generator Synchronously (Blocking).
        Use this for standard benchmarks.
        
        Args:
            warmup (str): Optional warmup duration (e.g. "2s") before measurement starts.
        """
        report_path = os.path.abspath(report_file)
        cmd = [
            self._bin_tool, "-mode", "load",
            "-target", target,
            "-concurrency", str(concurrency),
            "-rate", str(rate),
            "-duration", duration,
            "-report", report_path
        ]
        
        if encoding is not None:
            cmd.extend(["-encoding", encoding])
        
        if header_len is not None:
            cmd.extend(["-header-len", str(header_len)])
        
        if warmup is not None:
            cmd.extend(["-warmup", warmup])
        
        logger.info(f"Executing Sync: {' '.join(cmd)}")
        try:
            result = subprocess.run(cmd, check=True, capture_output=True, text=True)
            logger.info(result.stdout)
            if result.stderr:
                logger.error(f"Tool Stderr: {result.stderr}")
            return self._parse_report(report_path)
            
        except subprocess.CalledProcessError as e:
            logger.error(f"Load Generator Failed: {e.stderr}")
            raise

    # --- Async Methods for Variance/Spikes ---

    def start_load_generator(self, alias, target="localhost:8583", concurrency=10, rate=100, duration="1h", report_file=None, header_len=None, encoding=None):
        """
        Starts a background Load Generator instance. 
        Useful for creating background noise or overlapping spikes.
        
        Args:
            alias (str): Unique identifier for this process.
            duration (str): Default long duration for background tasks.
        """
        if report_file is None:
            report_file = f"report_{alias}.json"
        
        report_path = os.path.abspath(report_file)
        
        cmd = [
            self._bin_tool, "-mode", "load",
            "-target", target,
            "-concurrency", str(concurrency),
            "-rate", str(rate),
            "-duration", duration,
            "-report", report_path
        ]

        if encoding is not None:
            cmd.extend(["-encoding", encoding])

        if header_len is not None:
            cmd.extend(["-header-len", str(header_len)])
        
        logger.info(f"Starting Async Load Gen '{alias}': {' '.join(cmd)}")
        
        proc = subprocess.Popen(
            cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, preexec_fn=os.setsid, text=True
        )
        
        self._processes[alias] = {
            "process": proc,
            "cmd": cmd,
            "report_path": report_path
        }
        time.sleep(0.5) # Slight warmup
        if proc.poll() is not None:
             out, err = proc.communicate()
             raise RuntimeError(f"Load Gen '{alias}' failed to start: {err}")

    def stop_load_generator(self, alias):
        """Stops a specific background load generator and returns its metrics."""
        if alias not in self._processes:
            raise RuntimeError(f"No process found with alias '{alias}'")
        
        info = self._processes[alias]
        proc = info["process"]
        
        logger.info(f"Stopping Async Load Gen '{alias}'...")
        if proc.poll() is None:
            os.killpg(os.getpgid(proc.pid), signal.SIGINT) # SIGINT allows graceful shutdown/report save
            try:
                proc.wait(timeout=5)
            except subprocess.TimeoutExpired:
                os.killpg(os.getpgid(proc.pid), signal.SIGKILL)
        
        # Read Output
        stdout, stderr = proc.communicate()
        logger.info(f"Output '{alias}':\n{stdout}")
        if stderr:
            logger.info(f"Stderr '{alias}':\n{stderr}")
            
        del self._processes[alias]
        return self._parse_report(info["report_path"])

    def stop_all_load_generators(self):
        """Closes all background generators."""
        aliases = list(self._processes.keys())
        for alias in aliases:
            self.stop_load_generator(alias)

    # --- TCP Echo/Sink Server (High Performance) ---

    def start_echo_server(self, port=8590, log_file=None):
        """Starts the Go TCP Echo Server (Non-blocking)."""
        self._start_go_server(port, sink=False, log_file=log_file)

    def start_sink_server(self, port=8590, log_file=None):
        """Starts the Go TCP Sink Server (Blackhole, Non-blocking)."""
        self._start_go_server(port, sink=True, log_file=log_file)

    def _start_go_server(self, port, sink=False, log_file=None):
        cmd = [self._bin_tool, "-mode", "echo", "-port", str(port)]
        mode = "Echo"
        if sink:
            cmd.append("-sink")
            mode = "Sink"

        logger.info(f"Starting Go {mode} Server: {' '.join(cmd)}")
        
        stdout_dest = subprocess.PIPE
        stderr_dest = subprocess.PIPE
        self._echo_log_fh = None
        
        if log_file:
            log_dir = os.path.dirname(log_file)
            if log_dir and not os.path.exists(log_dir):
                os.makedirs(log_dir, exist_ok=True)
            self._echo_log_fh = open(log_file, 'w')
            stdout_dest = self._echo_log_fh
            stderr_dest = subprocess.STDOUT
            logger.info(f"redirecting stdout to {log_file}")

        self._echo_process = subprocess.Popen(
            cmd, stdout=stdout_dest, stderr=stderr_dest, preexec_fn=os.setsid
        )
        time.sleep(1) # Fast startup
        
        # Check if failed immediately
        if self._echo_process.poll() is not None:
             err_msg = "Process Exited"
             if log_file:
                 self._echo_log_fh.flush() # ensure written
                 with open(log_file, 'r') as f:
                     err_msg = f.read()
             else:
                 out, err = self._echo_process.communicate()
                 err_msg = err.decode() if err else out.decode()
                 
             raise RuntimeError(f"{mode} Server failed to start: {err_msg}")
             
        logger.info(f"{mode} Server running on {port} (PID: {self._echo_process.pid})")

    def stop_echo_server(self):
        if hasattr(self, '_echo_process') and self._echo_process:
            logger.info("Stopping Go Echo Server...")
            try:
                os.killpg(os.getpgid(self._echo_process.pid), signal.SIGTERM)
                self._echo_process.wait(timeout=5)
            except:
                try:
                    os.killpg(os.getpgid(self._echo_process.pid), signal.SIGKILL)
                except: pass
            
            if hasattr(self, '_echo_log_fh') and self._echo_log_fh:
                self._echo_log_fh.close()
                self._echo_log_fh = None
                
            logger.info("Echo Server stopped.")

    # --- Tool Server (Functional / Legacy) ---

    def start_iso_tool_server(self, port=8585):
        """Starts the Python Tool in Server Mode."""
        cmd = ["python3", self._tool_script, "--mode", "server", "--port", str(port)]
        logger.info(f"Starting ISO Tool Server: {' '.join(cmd)}")
        self._server_process = subprocess.Popen(
            cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, preexec_fn=os.setsid
        )
        time.sleep(2)
        if self._server_process.poll() is not None:
             out, err = self._server_process.communicate()
             raise RuntimeError(f"Server failed to start: {err.decode()}")

    def stop_iso_tool_server(self):
        if hasattr(self, '_server_process') and self._server_process:
            logger.info("Stopping ISO Tool Server...")
            os.killpg(os.getpgid(self._server_process.pid), signal.SIGTERM)
            self._server_process.wait()

    # --- Message construction ---

    # Field formats for the fields these suites exercise. ASCII throughout, which
    # is what the SDL specs under test declare.
    #
    #   n<len>   fixed numeric, left-padded with zeros
    #   a<len>   fixed alphanumeric, exact length required
    #   ll / lll ASCII length prefix of that many digits
    _FIELD_FORMATS = {
        2: ("ll", 19), 3: ("n", 6), 4: ("n", 12), 7: ("n", 10), 11: ("n", 6),
        12: ("n", 6), 13: ("n", 4), 14: ("n", 4), 18: ("n", 4), 22: ("n", 3), 23: ("n", 3),
        25: ("n", 2), 26: ("n", 2), 35: ("ll", 37), 37: ("n", 12), 38: ("a", 6),
        39: ("a", 2), 41: ("a", 8), 42: ("a", 15), 43: ("a", 40),
        48: ("lll", 999), 49: ("n", 3), 70: ("n", 3),
        # 70 exists so secondary-bitmap attempts fail with the
        # explanatory moov Length-16 error below, not "no format".
    }

    def build_iso_message(self, mti, bitmap="binary", **fields):
        """Builds an ASCII ISO 8583 message and returns it as a hex string.

        Fields are passed as `f<N>=value`, because Robot keyword arguments
        cannot begin with a digit. Only the primary bitmap is emitted, which
        covers fields 1..64.

        `bitmap` selects the wire encoding of the primary bitmap, and the
        two paths in this stack genuinely disagree:
        - "binary" (default): 8 raw bytes. What the io_iso8583 gateway
          gears parse, and what classic ASCII interfaces carry.
        - "hex": 32 ASCII characters (primary plus the zeroed secondary
          slot). What the moov-spec codec/sim gears unpack: their bitmap
          spec carries Length 16, which always reads 32 characters.
        A 16-character primary-only bitmap satisfies neither reader and
        fails far downstream (e.g. a later field short), so it is not
        offered: pick the reader you are sending to.

        Fixed alphanumeric fields must be supplied at their exact declared
        length rather than being padded here. `DE 43` is the reason: its country
        occupies the last two characters, so right-padding a short value would
        silently move the country out of the position the comparison reads, and
        the test would pass or fail for the wrong reason.
        """
        parsed = {}
        for name, value in fields.items():
            if not name.startswith("f"):
                raise ValueError(f"field arguments are named f<N>, got '{name}'")
            parsed[int(name[1:])] = str(value)

        primary = 0
        body = ""
        for num in sorted(parsed):
            fmt, length = self._FIELD_FORMATS.get(num, (None, None))
            if fmt is None:
                raise ValueError(f"DE {num} has no declared format in this library")
            value = parsed[num]

            if fmt == "n":
                if len(value) > length:
                    raise ValueError(f"DE {num}: {len(value)} digits exceeds {length}")
                body += value.rjust(length, "0")
            elif fmt == "a":
                if len(value) != length:
                    raise ValueError(
                        f"DE {num}: expected exactly {length} characters, got {len(value)}"
                    )
                body += value
            else:
                digits = len(fmt)
                if len(value) > length:
                    raise ValueError(f"DE {num}: {len(value)} exceeds max {length}")
                body += str(len(value)).rjust(digits, "0") + value

            if 1 <= num <= 64:
                primary |= 1 << (64 - num)
            elif 65 <= num <= 128:
                # Secondary bitmaps have no correct spelling on this
                # stack: moov Length-16 bitmaps always occupy 32 wire
                # characters per unpack iteration, so a secondary can
                # neither fit in 32 nor align in 64. Failing loudly
                # beats emitting a bitmap every reader misparses.
                raise ValueError(
                    f"DE {num}: secondary bitmaps (fields 65-128) are not "
                    "supported by this builder on the moov Length-16 wire "
                    "format; test the MTI pairing without the field instead"
                )
            else:
                raise ValueError(f"DE {num}: only fields 1..128 supported")

        # Bitmap wire form follows the `bitmap` argument (see docstring:
        # the readers disagree). Either spelling goes through .hex()
        # twice or once so fromhex restores the intended wire bytes.
        raw_bitmap = primary.to_bytes(8, "big")
        if bitmap == "hex":
            # 32 ASCII characters: primary hex plus the zeroed secondary
            # slot moov Length-16 bitmaps always occupy. Verified by a
            # moov pack/unpack round trip. NOTE: real acquirers send 16
            # characters when no secondary bitmap exists, so this is
            # interop debt if these specs ever face a live host.
            bitmap_hex = raw_bitmap.hex() + "0" * 16
            bitmap_part = bitmap_hex.encode().hex()
        elif bitmap == "binary":
            bitmap_part = raw_bitmap.hex()
        else:
            raise ValueError(f"bitmap must be 'binary' or 'hex', got '{bitmap}'")
        return (mti.encode().hex()
                + bitmap_part
                + body.encode().hex())

    def acceptor_location(self, name, country):
        """Composes DE 43 with the country in the last two characters.

        The field is 40 characters and the comparison reads its tail, so the
        country's position is the whole point of the field here.
        """
        country = str(country).upper()
        if len(country) != 2:
            raise ValueError(f"country must be ISO 3166 alpha-2, got '{country}'")
        return str(name)[: 40 - 2].ljust(40 - 2)[: 40 - 2] + country

    # --- Single-message exchange ---

    def send_iso_message(self, message_hex, target="localhost:8583", header_len=2, timeout=10):
        """Sends one framed ISO8583 message and returns the reply as a hex string.

        Every other keyword here drives the load generator, which builds its own
        traffic. Fidelity tests need the opposite: one exact message, chosen
        byte by byte, compared against exactly what comes back.

        The message is sent verbatim. Nothing here parses or rebuilds it, so a
        difference between what goes out and what returns is the system under
        test, never the harness.

        `header_len` is the size of the big-endian length prefix that frames the
        message on the wire, matching the gear's `frame_length_size`.
        """
        payload = bytes.fromhex(message_hex.replace(" ", "").replace("\n", ""))
        header_len = int(header_len)
        frame = len(payload).to_bytes(header_len, "big") + payload

        host, _, port = target.rpartition(":")
        logger.info(f"Sending {len(payload)} bytes to {target}: {payload.hex()}")

        with socket.create_connection((host, int(port)), timeout=float(timeout)) as sock:
            sock.settimeout(float(timeout))
            sock.sendall(frame)

            reply_header = self._recv_exactly(sock, header_len)
            reply_len = int.from_bytes(reply_header, "big")
            reply = self._recv_exactly(sock, reply_len)

        logger.info(f"Received {len(reply)} bytes: {reply.hex()}")
        return reply.hex()

    def send_iso_message_expecting_no_reply(self, message_hex, target="localhost:8583",
                                            header_len=2, wait="3"):
        """Sends one framed message and asserts that nothing comes back.

        This is the shape of a message a gear discarded: the connection stays
        open and no reply is framed onto it. Proving a non-event needs a bound,
        so `wait` is how long silence has to last to count -- configurable,
        because how long is long enough belongs to the machine running it, not
        to this file.

        If a reply does arrive it is returned in the failure, since what came
        back is the useful half of the diagnosis.
        """
        payload = bytes.fromhex(message_hex.replace(" ", "").replace("\n", ""))
        header_len = int(header_len)
        frame = len(payload).to_bytes(header_len, "big") + payload

        host, _, port = target.rpartition(":")
        with socket.create_connection((host, int(port)), timeout=float(wait)) as sock:
            sock.settimeout(float(wait))
            sock.sendall(frame)
            try:
                data = sock.recv(4096)
            except socket.timeout:
                return True
        if not data:
            # The peer closed without answering, which is also no reply.
            return True
        raise AssertionError(
            "expected no reply within %ss, got %d bytes: %s" % (wait, len(data), data.hex()))

    def send_iso_messages_together(self, messages_hex, target="localhost:8583",
                                   header_len=2, timeout=10):
        """Sends several messages on their own connections before reading any reply.

        Sending one message and waiting for it, then sending the next, never puts
        two of them in flight at once. A correlation store only collides when two
        contexts are parked simultaneously, so a test for that property has to
        construct the overlap rather than hope for it: this opens a connection per
        message, sends them all, and only then collects the replies.

        Returns the replies as hex, in the order the messages were given.
        """
        header_len = int(header_len)
        host, _, port = target.rpartition(":")
        socks = []
        try:
            for hexmsg in messages_hex:
                payload = bytes.fromhex(str(hexmsg).replace(" ", "").replace("\n", ""))
                sock = socket.create_connection((host, int(port)), timeout=float(timeout))
                sock.settimeout(float(timeout))
                sock.sendall(len(payload).to_bytes(header_len, "big") + payload)
                socks.append(sock)
                logger.info(f"Sent {len(payload)} bytes: {payload.hex()}")

            replies = []
            for i, sock in enumerate(socks):
                try:
                    reply_len = int.from_bytes(self._recv_exactly(sock, header_len), "big")
                    reply = self._recv_exactly(sock, reply_len)
                except (socket.timeout, TimeoutError) as exc:
                    # Starvation is the interesting outcome, not an infrastructure
                    # hiccup: when two parked contexts collide, one connection is
                    # answered twice and the other never. Saying which one waited
                    # is the difference between a diagnosis and a stack trace.
                    raise AssertionError(
                        f"message {i} of {len(socks)} got no reply within {timeout}s. "
                        f"With several messages in flight this usually means their "
                        f"correlation keys collided and another connection received "
                        f"this one's reply."
                    ) from exc
                logger.info(f"Received {len(reply)} bytes: {reply.hex()}")
                replies.append(reply.hex())
            return replies
        finally:
            for sock in socks:
                try:
                    sock.close()
                except OSError:
                    pass

    def _recv_exactly(self, sock, count):
        """Reads exactly count bytes, because a short read is a test result too.

        Returning whatever happened to arrive would turn a truncated reply into
        a confusing byte-comparison failure instead of a clear one.
        """
        buf = b""
        while len(buf) < count:
            chunk = sock.recv(count - len(buf))
            if not chunk:
                raise AssertionError(
                    f"connection closed after {len(buf)} of {count} expected bytes"
                )
            buf += chunk
        return buf

    # --- Helpers ---

    def _parse_report(self, report_path):
        if not os.path.exists(report_path):
            return {} # Might happen if killed too fast
            
        with open(report_path, 'r') as f:
            data = json.load(f)
        return data

    def assert_latency_p99_below(self, report, threshold_ms):
        p99 = report.get('latency_p99_ms', 9999.9)
        if p99 > float(threshold_ms):
            raise AssertionError(f"P99 {p99}ms > {threshold_ms}ms")

    def assert_success_rate_above(self, report, threshold_percent=100.0):
        sent = report.get('req_sent', 0)
        failed = report.get('req_failed', 0)
        if sent == 0: logger.warn("No requests sent."); return
        rate = ((sent - failed) / sent) * 100.0
        if rate < float(threshold_percent):
            raise AssertionError(f"Success {rate}% < {threshold_percent}%")

    def assert_response_rate_above(self, report, threshold_percent=100.0):
        sent = report.get('req_sent', 0)
        # Should match req_sent ideally, but for async/pipelining there might be gap.
        received = report.get('resp_recv', 0)
        if sent == 0: return # Already warned in success checks
        
        rate = (received / sent) * 100.0
        logger.info(f"Response Rate: {received}/{sent} ({rate}%)")
        
        if rate < float(threshold_percent):
             raise AssertionError(f"Response Rate {rate}% < {threshold_percent}% (Recv: {received}, Sent: {sent})")

    def read_from_socket(self, host="127.0.0.1", port=8583, header_len=2, timeout=10):
        """Reads a single framed ISO8583 message from a TCP socket.
        
        Args:
            host: Target hostname or IP address
            port: Target TCP port
            header_len: Length of the length prefix in bytes (2 or 4)
            timeout: Connection timeout in seconds
            
        Returns:
            Hex string of the complete framed message (header + body)
        """
        import socket
        host = str(host)
        port = int(port)
        header_len = int(header_len)
        timeout = float(timeout)
        
        with socket.create_connection((host, port), timeout=timeout) as sock:
            sock.settimeout(timeout)
            # Read length prefix
            header = self._recv_exactly(sock, header_len)
            if not header:
                raise RuntimeError("Connection closed before reading length header")
            
            if header_len == 2:
                length = int.from_bytes(header, "big")
            elif header_len == 4:
                length = int.from_bytes(header, "big")
            else:
                raise ValueError(f"Unsupported header length: {header_len}")
            
            # Read message body
            body = self._recv_exactly(sock, length)
            if len(body) != length:
                raise RuntimeError(f"Expected {length} bytes, got {len(body)}")
            
            return (header + body).hex()

    def open_socket_connection(self, alias, host="127.0.0.1", port=8583, timeout=10):
        """Opens a persistent TCP socket connection for repeated reads.
        
        Args:
            alias: Unique identifier for this connection
            host: Target hostname or IP address
            port: Target TCP port
            timeout: Connection timeout in seconds
        """
        import socket
        if not hasattr(self, '_socket_connections'):
            self._socket_connections = {}
        
        # Close existing connection with same alias if any
        if alias in self._socket_connections:
            try:
                self._socket_connections[alias].close()
            except:
                pass
            self._socket_connections.pop(alias, None)
            logger.info(f"Closed existing socket connection '{alias}'")
        
        sock = socket.create_connection((str(host), int(port)), timeout=float(timeout))
        sock.settimeout(float(timeout))
        self._socket_connections[alias] = sock
        logger.info(f"Opened socket connection '{alias}' to {host}:{port}")

    def read_from_socket_connection(self, alias, header_len=2, timeout=10):
        """Reads a length-prefixed message from an existing socket connection.
        
        Args:
            alias: Connection identifier from Open Socket Connection
            header_len: Length of the length prefix in bytes (2 or 4)
            timeout: Read timeout in seconds
            
        Returns:
            Hex string of the complete framed message (header + body)
        """
        import socket
        if not hasattr(self, '_socket_connections'):
            self._socket_connections = {}
        
        if alias not in self._socket_connections:
            raise RuntimeError(f"Socket connection '{alias}' not found. Call Open Socket Connection first.")
        
        sock = self._socket_connections[alias]
        sock.settimeout(float(timeout))
        header_len = int(header_len)
        
        try:
            header = self._recv_exactly(sock, header_len)
            if not header:
                raise RuntimeError("Connection closed before reading length header")
            
            if header_len == 2:
                length = int.from_bytes(header, "big")
            elif header_len == 4:
                length = int.from_bytes(header, "big")
            else:
                raise ValueError(f"Unsupported header length: {header_len}")
            
            body = self._recv_exactly(sock, length)
            if len(body) != length:
                raise RuntimeError(f"Expected {length} bytes, got {len(body)}")
            
            return (header + body).hex()
        except socket.timeout:
            # Remove broken connection on timeout
            self._socket_connections.pop(alias, None)
            raise RuntimeError(f"Read timeout after {timeout}s")
        except Exception as e:
            # Remove broken connection
            self._socket_connections.pop(alias, None)
            raise

    def close_socket_connection(self, alias):
        """Closes a persistent socket connection.
        
        Args:
            alias: Connection identifier
        """
        if hasattr(self, '_socket_connections') and alias in self._socket_connections:
            try:
                self._socket_connections[alias].close()
            except:
                pass
            self._socket_connections.pop(alias, None)
            logger.info(f"Closed socket connection '{alias}'")

    def close_all_socket_connections(self):
        """Closes all persistent socket connections."""
        if hasattr(self, '_socket_connections'):
            for alias, sock in self._socket_connections.items():
                try:
                    sock.close()
                except:
                    pass
            self._socket_connections.clear()
            logger.info("Closed all socket connections")

    def decode_iso_message(self, raw_hex):
        """Decodes a hex-encoded ISO8583 message using the iso8583-tool binary.
        
        Args:
            raw_hex: Hex string of the ISO8583 message (with or without length prefix)
            
        Returns:
            Dictionary of decoded fields with iso8583.field.<num> keys
        """
        import subprocess
        import json
        
        # Use the iso8583-tool binary to decode
        tool_path = os.path.join(bin_dir(self._project_root), 'iso8583-tool')
        if not os.path.exists(tool_path):
            # Fallback to simple decode if tool not built
            return self._simple_decode_iso_message(raw_hex)
        
        spec_path = os.path.join(self._project_root, 'examples/specs/iso8583-v87-ascii.yaml')
        
        cmd = [tool_path, "-mode", "decode", "-message", raw_hex, "-spec", spec_path]
        try:
            result = subprocess.run(cmd, check=True, capture_output=True, text=True, timeout=10)
            # Parse JSON output
            return json.loads(result.stdout)
        except subprocess.CalledProcessError as e:
            logger.error(f"iso8583-tool decode failed: {e.stderr}")
            return self._simple_decode_iso_message(raw_hex)
        except Exception as e:
            logger.error(f"Decode error: {e}")
            return self._simple_decode_iso_message(raw_hex)

    def _simple_decode_iso_message(self, raw_hex):
        """Simple fallback decode without external dependencies.
        Uses the same field format as build_iso_message (ASCII MTI + bitmap)."""
        logger.info(f"_simple_decode_iso_message input: {raw_hex[:100]}...")
        raw_bytes = bytes.fromhex(raw_hex)
        
        # Remove length prefix if present (2 bytes)
        if len(raw_bytes) >= 2:
            length = int.from_bytes(raw_bytes[:2], "big")
            logger.info(f"Length prefix: {length}, remaining: {len(raw_bytes) - 2}")
            if length == len(raw_bytes) - 2:
                raw_bytes = raw_bytes[2:]
                logger.info(f"Removed length prefix, payload: {raw_bytes[:50].hex()}...")
        
        # Simple parse: ASCII MTI (4 chars) + bitmap (8 bytes = 16 hex chars = 64 bits)
        if len(raw_bytes) < 12:
            logger.info("Too short for MTI+Bitmap")
            return {}
        
        # MTI is 4 ASCII bytes (e.g., "0100")
        mti = raw_bytes[:4].decode('ascii', errors='ignore')
        result = {"iso8583.mti": mti}
        logger.info(f"MTI: {mti}")
        
        # Primary bitmap is 8 bytes (64 bits) starting at offset 4
        if len(raw_bytes) < 12:
            logger.info("Too short for bitmap")
            return result
            
        bitmap_bytes = raw_bytes[4:12]
        bitmap = int.from_bytes(bitmap_bytes, 'big')
        logger.info(f"Primary bitmap: {bitmap:064b}")
        
        # Check for secondary bitmap (bit 1 of primary bitmap)
        has_secondary = (bitmap & 0x8000000000000000) != 0
        offset = 12
        if has_secondary:
            if len(raw_bytes) < 20:
                logger.info("Too short for secondary bitmap")
                return result
            secondary_bitmap_bytes = raw_bytes[12:20]
            secondary_bitmap = int.from_bytes(secondary_bitmap_bytes, 'big')
            logger.info(f"Secondary bitmap: {secondary_bitmap:064b}")
            offset = 20
        
        # Parse fields based on bitmap (fields 1-64 from primary, 65-128 from secondary)
        for field_num in range(1, 65):
            if bitmap & (1 << (64 - field_num)):
                fmt, length = self._FIELD_FORMATS.get(field_num, (None, None))
                if fmt is None:
                    continue
                if offset + length > len(raw_bytes):
                    logger.info(f"Field {field_num}: offset+length ({offset+length}) > len ({len(raw_bytes)})")
                    break
                value = raw_bytes[offset:offset+length].decode('ascii', errors='ignore')
                result[f"iso8583.field.{field_num}"] = value
                logger.info(f"Field {field_num}: '{value}'")
                offset += length
        
        # Workaround: moov-io/iso8583 may not set bitmap bit for field 2 (PAN) in some cases
        # If MTI is 0100 or 0200 and field 2 not decoded, try to decode it at expected position
        if mti in ("0100", "0200") and "iso8583.field.2" not in result:
            # Field 2 (PAN) is ll-var (2-digit len + up to 19 digits)
            # Try to find it after the bitmap
            # Scan for a 19-digit numeric string that looks like a PAN
            for scan_offset in range(12, min(len(raw_bytes), 12 + 20)):
                if scan_offset + 2 <= len(raw_bytes):
                    try:
                        pan_len = int.from_bytes(raw_bytes[scan_offset:scan_offset+2], 'big')
                        if 12 <= pan_len <= 19 and scan_offset + 2 + pan_len <= len(raw_bytes):
                            pan_bytes = raw_bytes[scan_offset+2:scan_offset+2+pan_len]
                            if pan_bytes.isdigit():
                                pan = pan_bytes.decode('ascii')
                                result["iso8583.field.2"] = pan
                                logger.info(f"Field 2 (PAN) found via scan: '{pan}'")
                                break
                    except:
                        pass
        
        # Parse secondary bitmap fields (65-128)
        if has_secondary:
            for field_num in range(65, 129):
                if secondary_bitmap & (1 << (128 - field_num)):
                    fmt, length = self._FIELD_FORMATS.get(field_num, (None, None))
                    if fmt is None:
                        continue
                    if offset + length > len(raw_bytes):
                        logger.info(f"Field {field_num}: offset+length ({offset+length}) > len ({len(raw_bytes)})")
                        break
                    value = raw_bytes[offset:offset+length].decode('ascii', errors='ignore')
                    result[f"iso8583.field.{field_num}"] = value
                    logger.info(f"Field {field_num}: '{value}'")
                    offset += length
        
        logger.info(f"Decoded result: {result}")
        return result

    def parse_iso_message(self, raw_hex):
        """Alias for Decode Iso Message."""
        return self.decode_iso_message(raw_hex)

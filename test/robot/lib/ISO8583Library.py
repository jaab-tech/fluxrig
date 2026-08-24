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
        self._bin_tool = os.path.join(self._project_root, 'bin', 'iso8583-tool')
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

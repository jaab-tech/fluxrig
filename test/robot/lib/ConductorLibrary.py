# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

"""Robot Framework library for Conductor payment-switch validation.

Drives the two new simulator modes of `iso8583-tool`:
  - `scheme` : an upstream card-scheme host (replies 0210 with a DE39, or sinks)
  - `auth`   : one or many POS terminals (0200 auth, verifies STAN echo + DE39),
               writing a JSON report the suite asserts on.

Kept separate from ISO8583Library, which wraps the throughput `load` mode.
"""

import json
import os
import signal
import subprocess
import time

from robot.api import logger
from robot.api.deco import keyword, library


@library(scope="GLOBAL")
class ConductorLibrary:
    def __init__(self):
        self._procs = {}  # alias -> {process, report_path}
        root = os.path.abspath(
            os.path.join(os.path.dirname(os.path.realpath(__file__)), "../../..")
        )
        self._tool = os.path.join(root, "bin", "iso8583-tool")

    # --- Scheme hosts -----------------------------------------------------

    @keyword
    def start_scheme_host(self, alias, port, spec, de39=None, sink=False, log_file=None):
        """Start an upstream scheme host in the background.

        Replies 0210 with `de39`, or (with sink=True) accepts and never answers
        to drive the switch's timeout path.
        """
        cmd = [self._tool, "-mode", "scheme", "-scheme-port", str(port), "-scheme-spec", spec]
        if sink:
            cmd.append("-scheme-sink")
        elif de39 is not None:
            cmd.extend(["-scheme-de39", str(de39)])
        self._spawn(alias, cmd, log_file)

    # --- Auth terminals ---------------------------------------------------

    def _auth_cmd(self, target, spec, report_file, opts):
        cmd = [
            self._tool, "-mode", "auth",
            "-auth-target", target,
            "-auth-spec", spec,
            "-auth-report", os.path.abspath(report_file),
        ]
        flag_map = {
            "pan": "-auth-pan", "stan": "-auth-stan", "mix": "-auth-mix",
            "expect_mti": "-auth-expect-mti", "expect_de39": "-auth-expect-de39",
            "accept_de39": "-auth-accept-de39", "conns": "-auth-conns",
            "count": "-auth-count", "rate": "-auth-rate",
            "stan_base": "-auth-stan-base", "timeout": "-auth-timeout",
        }
        for key, flag in flag_map.items():
            if key in opts and opts[key] is not None:
                cmd.extend([flag, str(opts[key])])
        # Boolean switch: present when truthy, absent otherwise.
        if opts.get("reconnect"):
            cmd.append("-auth-reconnect")
        return cmd

    @keyword
    def run_auth_terminal(self, target, spec, report_file, timeout=180, **opts):
        """Run a terminal (or terminal fleet) to completion; return its report.

        The returned dict is the parsed JSON report plus an `rc` (exit code)
        key. Does NOT raise on a non-zero exit, so a suite can assert on the
        structured result (e.g. a chaos run where some declines are expected).
        """
        cmd = self._auth_cmd(target, spec, report_file, opts)
        logger.info(f"auth (blocking): {' '.join(cmd)}")
        proc = subprocess.run(cmd, capture_output=True, text=True, timeout=int(timeout))
        if proc.stdout:
            logger.info(proc.stdout.strip())
        if proc.stderr:
            logger.info(f"stderr:\n{proc.stderr.strip()}")
        rep = self.read_auth_report(report_file)
        rep["rc"] = proc.returncode
        return rep

    @keyword
    def start_auth_terminal(self, alias, target, spec, report_file, **opts):
        """Start a terminal fleet in the background (for chaos-during-load)."""
        cmd = self._auth_cmd(target, spec, report_file, opts)
        self._spawn(alias, cmd, log_file=None, report_path=os.path.abspath(report_file))

    @keyword
    def wait_auth_terminal(self, alias, timeout=180):
        """Join a background terminal fleet and return its report (with `rc`)."""
        info = self._procs.get(alias)
        if not info:
            raise RuntimeError(f"No process with alias '{alias}'")
        proc = info["process"]
        try:
            proc.wait(timeout=int(timeout))
        except subprocess.TimeoutExpired:
            logger.warn(f"terminal '{alias}' did not finish in {timeout}s; killing")
            self._kill(proc)
        out, err = proc.communicate()
        if out:
            logger.info(out)
        if err:
            logger.info(f"stderr:\n{err}")
        rep = self.read_auth_report(info["report_path"])
        rep["rc"] = proc.returncode
        del self._procs[alias]
        return rep

    # --- Assertions -------------------------------------------------------

    @keyword
    def assert_no_cross_wiring(self, report):
        """Hard invariant: not a single reply came back with the wrong STAN."""
        cw = int(report.get("cross_wired", -1))
        if cw != 0:
            raise AssertionError(f"cross_wired={cw} (expected 0): a reply was routed to the wrong terminal")

    @keyword
    def assert_all_ok(self, report):
        """Every txn got a well-formed, correctly-routed, expected reply."""
        self.assert_no_cross_wiring(report)
        ok, total, failed = int(report.get("ok", 0)), int(report.get("total", -1)), int(report.get("failed", -1))
        if ok != total or failed != 0:
            raise AssertionError(f"ok={ok} total={total} failed={failed} (want ok==total, failed==0)")

    @keyword
    def assert_de39_seen(self, report, de39):
        """Assert at least one reply carried this DE39 (proves a path executed)."""
        n = int(report.get("by_de39", {}).get(str(de39), 0))
        if n <= 0:
            raise AssertionError(f"expected at least one DE39={de39}, saw none. by_de39={report.get('by_de39')}")

    @keyword
    def assert_declines_present(self, report, approve_de39="00"):
        """Assert at least one non-approve reply came back (chaos took effect)."""
        by = report.get("by_de39", {})
        declines = sum(v for k, v in by.items() if k != str(approve_de39))
        if declines <= 0:
            raise AssertionError(f"expected declines during chaos, saw none. by_de39={by}")
        logger.info(f"declines during chaos: {declines} ({by})")
        return declines

    @keyword
    def assert_survived_reconnects(self, report, min_ok_ratio=0.8):
        """Terminal-side chaos invariant: the switch never mis-delivered under an
        ingress blip. No cross-wiring, every reply that arrived was well-formed
        (failed==0), the terminal actually reconnected (reconnects>0), and the
        large majority of transactions still completed once it was back."""
        self.assert_no_cross_wiring(report)
        failed = int(report.get("failed", -1))
        reconnects = int(report.get("reconnects", 0))
        ok = int(report.get("ok", 0))
        total = int(report.get("total", 1)) or 1
        if failed != 0:
            raise AssertionError(f"failed={failed} (a reply arrived mis-formed/mis-routed under terminal chaos)")
        if reconnects <= 0:
            raise AssertionError(f"reconnects={reconnects}: the terminal never lost its link, so the blip was not exercised")
        ratio = ok / total
        if ratio < float(min_ok_ratio):
            raise AssertionError(f"only {ok}/{total} ({ratio:.0%}) completed; expected >= {float(min_ok_ratio):.0%}. report={report}")
        logger.info(f"survived {reconnects} reconnect(s): {ok}/{total} ok, {report.get('dropped')} dropped, 0 cross-wired")

    @keyword
    def read_auth_report(self, report_file):
        path = os.path.abspath(report_file)
        if not os.path.exists(path):
            return {}
        with open(path) as f:
            return json.load(f)

    # --- Process management ----------------------------------------------

    def _spawn(self, alias, cmd, log_file=None, report_path=None):
        if alias in self._procs:
            self.stop_conductor_process(alias)
        logger.info(f"start '{alias}': {' '.join(cmd)}")
        stdout = subprocess.PIPE
        stderr = subprocess.PIPE
        fh = None
        if log_file:
            os.makedirs(os.path.dirname(log_file), exist_ok=True)
            fh = open(log_file, "w")
            stdout, stderr = fh, subprocess.STDOUT
        proc = subprocess.Popen(cmd, stdout=stdout, stderr=stderr, preexec_fn=os.setsid, text=True)
        self._procs[alias] = {"process": proc, "report_path": report_path, "fh": fh}
        time.sleep(0.4)
        if proc.poll() is not None and not report_path:
            # A short-lived server that died immediately is a setup error.
            out = ""
            if not fh:
                _, out = proc.communicate()
            raise RuntimeError(f"process '{alias}' exited immediately: {out}")

    @keyword
    def stop_conductor_process(self, alias):
        info = self._procs.pop(alias, None)
        if not info:
            return
        self._kill(info["process"])
        if info.get("fh"):
            info["fh"].close()

    @keyword
    def stop_all_conductor_processes(self):
        for alias in list(self._procs.keys()):
            self.stop_conductor_process(alias)

    def _kill(self, proc):
        if proc.poll() is None:
            try:
                os.killpg(os.getpgid(proc.pid), signal.SIGTERM)
                proc.wait(timeout=5)
            except Exception:
                try:
                    os.killpg(os.getpgid(proc.pid), signal.SIGKILL)
                except Exception:
                    pass

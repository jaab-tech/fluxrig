# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0
"""Keywords for the spec suite: the CLI, the store, and the reference it renders.

The suite drives the shipped binary rather than the Go packages, because what
this proves is that the pieces agree at their edges -- the loader's rules, what
the store files a document under, and what the Mixer serves for it.
"""

import os
import re
import subprocess

from robot.api.deco import keyword, library


@library(scope="GLOBAL")
class SpecLibrary:
    ROBOT_LIBRARY_VERSION = "1.0"

    def __init__(self):
        self._root = self._find_root()

    def _find_root(self):
        here = os.path.abspath(__file__)
        for _ in range(8):
            here = os.path.dirname(here)
            if os.path.isdir(os.path.join(here, "cmd")) and os.path.isdir(os.path.join(here, "bin")):
                return here
        raise RuntimeError("cannot locate the repository root from %s" % __file__)

    @property
    def _cli(self):
        return os.path.join(self._root, "bin", "fluxrig")

    @keyword
    def repository_path(self, *parts):
        """A path inside the checkout, so a suite can name the shipped reference spec."""
        return os.path.join(self._root, *parts)

    @keyword
    def run_fluxrig(self, *args, expect_failure=False):
        """Runs the CLI and returns {rc, stdout, stderr, output}.

        The exit code is read from the process, never through a pipe: a pipeline
        reports the status of its last stage, which is how a failing command
        reads as a pass.
        """
        proc = subprocess.run(
            [self._cli, *[str(a) for a in args]],
            capture_output=True, text=True, timeout=120, check=False,
        )
        result = {
            "rc": proc.returncode,
            "stdout": proc.stdout,
            "stderr": proc.stderr,
            "output": proc.stdout + proc.stderr,
        }
        if expect_failure and proc.returncode == 0:
            raise AssertionError(
                "expected the CLI to refuse `%s`, and it succeeded:\n%s" % (" ".join(args), result["output"]))
        if not expect_failure and proc.returncode != 0:
            raise AssertionError(
                "`%s` failed with rc=%d:\n%s" % (" ".join(args), proc.returncode, result["output"]))
        return result

    @keyword
    def write_spec(self, path, body):
        """Writes a spec fixture, creating its directory."""
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "w", encoding="utf-8") as fh:
            fh.write(body)
        return path

    # ── The rendered reference ────────────────────────────────────────────────

    @keyword
    def read_file_text(self, path):
        with open(path, encoding="utf-8") as fh:
            return fh.read()

    @keyword
    def every_internal_link_resolves(self, html):
        """Fails naming the anchors that lead nowhere.

        A reference is navigated, not read front to back, and a link into a
        section that does not exist is a dead end the reader finds instead of
        the answer.
        """
        ids = set(re.findall(r'id="([^"]+)"', html))
        broken = sorted({h for h in re.findall(r'href="#([^"]+)"', html) if h not in ids})
        if broken:
            raise AssertionError("%d anchors lead nowhere: %s" % (len(broken), ", ".join(broken[:12])))
        return len(ids)

    @keyword
    def page_fetches_nothing_external(self, html):
        """Fails naming any resource the page would go to the network for.

        A protocol reference gets mailed and opened offline. A page that pulls a
        stylesheet renders as unstyled text on the machine that matters.
        """
        remote = re.findall(r'(?:src|href)="(https?://[^"]+)"', html)
        # A citation the reader may follow is a link, not a fetch: it loads only
        # if someone clicks it.
        fetched = [u for u in remote if not re.search(r'>[^<]*</a>', html[html.find(u):html.find(u) + 400])]
        if fetched:
            raise AssertionError("the page fetches %d external resources: %s" % (len(fetched), fetched[:5]))
        return len(remote)

    @keyword
    def count_data_elements(self, html):
        """How many data elements the page documents."""
        return len(set(re.findall(r'id="de-(\d+)"', html)))

    @keyword
    def extract_first_group(self, text, pattern):
        m = re.search(pattern, text)
        if not m:
            raise AssertionError("pattern %r does not appear in the text" % pattern)
        return m.group(1)

    # ── The Mixer serving what the store holds ────────────────────────────────

    @keyword
    def http_get(self, url, expect_status=200):
        """GETs a URL and returns {status, body, content_type}.

        Status is asserted here rather than in the suite so a failure names the
        URL and shows the body, which is where the server says what it objected
        to.
        """
        import requests

        resp = requests.get(url, timeout=30)
        result = {
            "status": resp.status_code,
            "body": resp.text,
            "content_type": resp.headers.get("Content-Type", ""),
        }
        if int(expect_status) != resp.status_code:
            raise AssertionError(
                "GET %s answered %d, expected %s:\n%s"
                % (url, resp.status_code, expect_status, resp.text[:600]))
        return result

    @keyword
    def parse_json(self, text):
        """Decodes a JSON body, so a suite asserts on values rather than on the
        substrings that happen to appear in the rendering of them."""
        import json

        return json.loads(text)

    @keyword
    def versions_in_order(self, items):
        """The tags of a listing, in the order it returned them."""
        return [i["tag"] for i in items]

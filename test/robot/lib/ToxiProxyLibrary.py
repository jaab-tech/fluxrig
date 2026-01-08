# Copyright 2025 JAAB Tech SAS, Uruguay
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

from toxiproxy import Toxiproxy
from robot.api import logger
from robot.api.deco import keyword, library

@library
class ToxiProxyLibrary:
    """
    Toxiproxy Helper for Network Chaos.
    """

    ROBOT_LISTENER_API_VERSION = 3

    def __init__(self):
        self.server = None
        self.proxies = {}

    @keyword
    def connect_to_toxiproxy(self, host: str = "localhost:8474"):
        """Connects to the Toxiproxy server API."""
        self.server = Toxiproxy(host)
        logger.info(f"Connected to Toxiproxy at {host}")

    @keyword
    def create_proxy(self, name: str, upstream: str, listen: str):
        """Creates a new proxy mapping."""
        if name in self.proxies:
            self.delete_proxy(name)
        
        proxy = self.server.create(name=name, upstream=upstream, listen=listen, enabled=True)
        self.proxies[name] = proxy
        logger.info(f"Created Proxy {name}: {listen} -> {upstream}")

    @keyword
    def add_latency(self, name: str, latency: int, jitter: int = 0):
        """Adds latency toxic to the proxy."""
        proxy = self._get_proxy(name)
        proxy.add_toxic(
            type="latency",
            attributes={"latency": latency, "jitter": jitter}
        )
        logger.info(f"Added Latency to {name}: {latency}ms (jitter={jitter})")

    @keyword
    def cut_link(self, name: str):
        """Disables the proxy (simulates cable cut)."""
        proxy = self._get_proxy(name)
        proxy.disable()
        logger.info(f"Cut Link: {name}")

    @keyword
    def restore_link(self, name: str):
        """Enables the proxy (restores cable)."""
        proxy = self._get_proxy(name)
        proxy.enable()
        logger.info(f"Restored Link: {name}")

    @keyword
    def reset_toxics(self, name: str):
        """Removes all toxics from a proxy."""
        proxy = self._get_proxy(name)
        for toxic in proxy.toxics():
            toxic.destroy()
        logger.info(f"Reset Toxics on {name}")

    @keyword
    def delete_proxy(self, name: str):
        """Deletes a proxy."""
        if name in self.proxies:
            self.proxies[name].destroy()
            del self.proxies[name]
    
    def _get_proxy(self, name):
        if name not in self.proxies:
            raise ValueError(f"Proxy {name} not found")
        return self.proxies[name]

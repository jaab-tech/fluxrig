# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

import asyncio
import cbor2
import nats
from robot.api import logger
from robot.api.deco import keyword, library

@library
class NATSLibrary:
    """
    NATS Helper for FluxRig Robot Tests.
    Allows publishing messages to 'flux-msg' and verifying receipts using CBOR.
    """
    
    ROBOT_LISTENER_API_VERSION = 3

    def __init__(self):
        self.nc = None
        self.subs = []

    @keyword
    def connect_to_nats(self, url: str = "nats://localhost:4222"):
        """Connects to the NATS server."""
        asyncio.run(self._connect(url))

    async def _connect(self, url):
        self.nc = await nats.connect(url)
        logger.info(f"Connected to NATS: {url}")

    @keyword
    def publish_flux_msg(self, subject: str, payload: dict):
        """Publishes a CBOR payload to a subject."""
        asyncio.run(self._publish(subject, payload))

    async def _publish(self, subject, payload):
        if not self.nc:
            raise RuntimeError("Not connected to NATS")
        # Encode as CBOR for FluxRig compatibility
        data = cbor2.dumps(payload)
        await self.nc.publish(subject, data)
        await self.nc.flush()
        logger.info(f"Published to {subject} (CBOR): {payload}")

    @keyword
    def subscribe_and_expect(self, subject: str, expected_key: str, expected_value: str, timeout: str = "5s"):
        """
        Subscribes to a subject and waits for a CBOR message containing expected data.
        """
        return asyncio.run(self._subscribe_expect(subject, expected_key, expected_value, timeout))

    async def _subscribe_expect(self, subject, key, value, timeout_str):
        if not self.nc:
            raise RuntimeError("Not connected to NATS")
        
        # Parse timeout (e.g. "5s" -> 5)
        timeout = int(timeout_str.replace("s", ""))
        
        future = asyncio.Future()

        async def cb(msg):
            try:
                # Decode CBOR from FluxMsg
                data = cbor2.loads(msg.data)
                # Check for nested keys if necessary, or just top-level
                val = data.get(key)
                if str(val) == str(value):
                    if not future.done():
                        future.set_result(data)
            except Exception as e:
                # Log and ignore non-CBOR or malformed messages
                pass

        sub = await self.nc.subscribe(subject, cb=cb)
        
        try:
            result = await asyncio.wait_for(future, timeout)
            logger.info(f"Received expected message on {subject}: {result}")
            await sub.unsubscribe()
            return result
        except asyncio.TimeoutError:
            await sub.unsubscribe()
            raise AssertionError(f"Timeout waiting for {key}={value} on {subject}")

    @keyword
    def close_nats(self):
        """Closes the NATS connection."""
        if self.nc:
            asyncio.run(self.nc.close())
            self.nc = None

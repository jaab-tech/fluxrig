# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0
#
# One image for every node of the HA testbed: the NATS servers, the Mixers
# and the Racks. It runs the real binaries, so it measures the product, not
# the mechanism. nats-server is built from the version go.mod pins, the one
# the engine is tested against.
#
# ubuntu:24.04, not bookworm-slim: the binaries link glibc 2.38. iproute2 and
# iptables are for the faults (netem shaping, cutting a node off).
FROM ubuntu:24.04
RUN apt-get update \
 && apt-get install -y --no-install-recommends iproute2 iptables ca-certificates \
 && rm -rf /var/lib/apt/lists/*
COPY .build/fluxrig /usr/local/bin/fluxrig
COPY .build/fluxrig-mixer /usr/local/bin/fluxrig-mixer
COPY .build/nats-server /usr/local/bin/nats-server
COPY .build/s3server /usr/local/bin/s3server
COPY .build/otlpreceiver /usr/local/bin/otlpreceiver
ENTRYPOINT ["/usr/local/bin/fluxrig-mixer"]

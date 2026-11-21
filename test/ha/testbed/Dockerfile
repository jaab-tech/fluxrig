# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0
#
# One HA testbed node: the failover tool in server mode (the nats-server
# version pinned in go.mod), plus the tools the chaos script drives inside
# the container: tc for netem, iptables for partitions.
FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends iproute2 iptables \
 && rm -rf /var/lib/apt/lists/*
COPY .build/ha-node /usr/local/bin/ha-node
ENTRYPOINT ["/usr/local/bin/ha-node", "server"]

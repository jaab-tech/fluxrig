# Copyright 2025 JAAB Tech SAS, Uruguay
meta:
  name: "iso8583_client_mode_e2e"
  version: "1.0.0"

racks:
  - name: "iso-node-01"
    labels:
      tier: "edge"

gears:
  # Ingress Gear (Server Mode)
  # Receives traffic from Test Script
  - name: "iso-server"
    type: "io_iso8583"
    deploy: "iso-node-01"
    config:
      mode: "server"
      bind: ":8583"
      encoding: "{{ENCODING}}"
      header_endian: "{{ENDIAN}}"
      header_length_bytes: 2
      variant: "{{VARIANT}}"

  # Egress Gear (Client Mode)
  # Connects to Mock Server
  - name: "iso-client"
    type: "io_iso8583"
    deploy: "iso-node-01"
    config:
      mode: "client"
      connect: "127.0.0.1:10000"
      encoding: "{{ENCODING}}"
      header_endian: "{{ENDIAN}}"
      header_length_bytes: 2
      variant: "{{VARIANT}}"
      reconnect_wait: "1s"
      # Visa defaults (only used if variant is visa)
      visa_src_id: "123456"
      visa_dst_id: "654321"

wires:
  # Forward traffic from Server (Ingress) to Client (Egress)
  - from: "iso-server.out"
    to: "iso-client.in"

  # Forward response from Client (Ingress from Mock) to Server (Egress to Script)
  - from: "iso-client.out"
    to: "iso-server.in"

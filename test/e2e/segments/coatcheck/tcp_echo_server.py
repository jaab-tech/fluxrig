#!/usr/bin/env python3

# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0
"""
Simple TCP Echo Server for Coat Check E2E Tests.
Handles raw socket connections and echoes back received data.
Used for "Custom Protocol" line-based testing.
"""

import socket
import sys

def main():
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 9000
    
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        s.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
        s.bind(('0.0.0.0', port))
        s.listen(5) # Backlog
        print(f"TCP Echo Server (Threaded) listening on port {port}")
        sys.stdout.flush()
        
        import threading

        def handle_client(conn, addr):
            print(f"Connected by {addr}", file=sys.stderr)
            with conn:
                while True:
                    try:
                        data = conn.recv(4096)
                        if not data:
                            break
                        print(f"[{addr}] Received {len(data)} bytes: {data!r}", file=sys.stderr)
                        
                        # Delay Logic for TC54
                        if b'delay:' in data:
                            try:
                                parts = data.split(b':')
                                seconds = int(parts[1].strip())
                                import time
                                print(f"[{addr}] Sleeping for {seconds}s...", file=sys.stderr)
                                time.sleep(seconds)
                            except Exception as e:
                                print(f"[{addr}] Delay parse error: {e}", file=sys.stderr)

                        conn.sendall(data)
                        print(f"[{addr}] Echoed {len(data)} bytes", file=sys.stderr)
                    except Exception as e:
                        print(f"[{addr}] Error: {e}", file=sys.stderr)
                        break
            print(f"Disconnected {addr}", file=sys.stderr)

        while True:
            try:
                conn, addr = s.accept()
                t = threading.Thread(target=handle_client, args=(conn, addr))
                t.daemon = True
                t.start()
            except KeyboardInterrupt:
                break
            except Exception as e:
                print(f"Accept Error: {e}", file=sys.stderr)

if __name__ == "__main__":
    main()
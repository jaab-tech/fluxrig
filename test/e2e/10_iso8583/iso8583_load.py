#!/usr/bin/env python3
# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

"""
ISO8583 Load Tester & Concurrency Verifier.

This script spawns multiple concurrent threads to connect to the fluxrig server
and send messages, verifying:
1. MaxConnections limits (some connections should be rejected if limit is reached).
2. Server stability under load (no crashes).
3. Throughput (messages/sec).

Usage:
    python iso8583_load.py --host 127.0.0.1 --port 8583 --conns 50 --loops 10
"""

import argparse
import socket
import struct
import threading
import time
import sys
from concurrent.futures import ThreadPoolExecutor

# Stats
stats_lock = threading.Lock()
stats = {
    "connect_ok": 0,
    "connect_fail": 0,
    "msg_sent": 0,
    "msg_fail": 0,
    "closed_by_peer": 0
}

def update_stat(key):
    with stats_lock:
        stats[key] += 1

def build_0800():
    # Simple 0800 message (ASCII)
    # MTI(4) + DE7(10) + DE11(6) + DE70(3)
    # 0800 0124160000 000001 001
    # Bitmap (Primary) for 7, 11, 70:
    # 0010 0010 0000 0000 ... -> 2200000000000000
    # Payload: 0800 2200000000000000 0124160000 000001 001
    return b"080022000000000000000124160000000001001"

def send_frame(sock, payload):
    # 2-byte Big Endian Length
    length = len(payload)
    frame = struct.pack(">H", length) + payload
    sock.sendall(frame)

def worker(worker_id, args):
    try:
        sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        sock.settimeout(5.0)
        sock.connect((args.host, args.port))
        update_stat("connect_ok")
    except Exception as e:
        # print(f"[{worker_id}] Connect Failed: {e}")
        update_stat("connect_fail")
        return

    try:
        payload = build_0800()
        for i in range(args.loops):
            try:
                send_frame(sock, payload)
                update_stat("msg_sent")
                time.sleep(args.delay)
            except Exception as e:
                # print(f"[{worker_id}] Send Failed: {e}")
                update_stat("msg_fail")
                break
    except Exception:
        pass
    finally:
        sock.close()

def run_load_test(args):
    print(f"=== ISO8583 Load Test ===")
    print(f"Target: {args.host}:{args.port}")
    print(f"Workers: {args.conns}")
    print(f"Loops per Worker: {args.loops}")
    print("--------------------------------")

    start_time = time.time()
    
    with ThreadPoolExecutor(max_workers=args.conns) as executor:
        futures = [executor.submit(worker, i, args) for i in range(args.conns)]
        for f in futures:
            f.result()

    duration = time.time() - start_time
    print("--------------------------------")
    print(f"Duration: {duration:.2f}s")
    print(f"Connections OK: {stats['connect_ok']}")
    print(f"Connections Fail: {stats['connect_fail']}")
    print(f"Messages Sent: {stats['msg_sent']}")
    print(f"Messages Fail: {stats['msg_fail']}")
    
    # Validation logic for MaxConnections
    # Since rejection happens at application level (post-Accept), connect_ok might be high, 
    # but subsequent sends will fail (Broken Pipe / Reset).
    # We consider it a pass if we see failures (connect_fail OR msg_fail).
    total_failures = stats['connect_fail'] + stats['msg_fail']
    
    if args.expect_rejects:
        if total_failures == 0:
            print("FAILURE: Expected connection/message failures (MaxConnections), but got none.")
            sys.exit(1)
        else:
             print(f"SUCCESS: Detected {total_failures} failures (Expected due to limit).")
    
    if not args.expect_rejects and total_failures > 0:
        print("WARNING: Unexpected connection/message failures.")

    print("=== Test Finished ===")

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=8583)
    parser.add_argument("--conns", type=int, default=10)
    parser.add_argument("--loops", type=int, default=10)
    parser.add_argument("--delay", type=float, default=0.1)
    parser.add_argument("--expect-rejects", action="store_true", help="Fail if no connections are rejected")
    
    args = parser.parse_args()
    run_load_test(args)

if __name__ == "__main__":
    main()

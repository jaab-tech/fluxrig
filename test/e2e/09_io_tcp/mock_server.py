# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

import socket
import sys
import time

HOST = '127.0.0.1'
PORT = 9002

def run_server():
    print(f"[MockServer] Listening on {HOST}:{PORT}...")
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        s.bind((HOST, PORT))
        s.listen(1)
        s.settimeout(10.0) # Prevent hang if Rack never connects
        
        # Accept connection (Blocking with timeout)
        try:
            conn, addr = s.accept()
        except socket.timeout:
            print("[MockServer] FAIL: Timeout waiting for Rack connection.")
            sys.exit(1)
            
        with conn:
            print(f"[MockServer] Connected by {addr}")
            
            # 1. Send Traffic (Generate input for Rack)
            msg = b"PING\n"
            print(f"[MockServer] Sending: {msg}")
            conn.sendall(msg)
            
            # 2. Wait for Echo (Validate Output from Rack)
            conn.settimeout(5.0) # 5s timeout for echo
            try:
                data = conn.recv(1024)
                print(f"[MockServer] Received: {data}")
                
                if b"PING" in data:
                    print("[MockServer] SUCCESS: Echo Verified.")
                    sys.exit(0)
                else:
                    print(f"[MockServer] FAIL: Expected PING, got {data}")
                    sys.exit(1)
            except socket.timeout:
                print("[MockServer] FAIL: Timeout waiting for Echo.")
                sys.exit(1)

if __name__ == "__main__":
    run_server()

#!/usr/bin/env python3
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

"""
ISO8583 Traffic Generator & Verifier (Dual Mode).

This script acts as BOTH the Traffic Source (Injector) and the Traffic Sink (Mock Server).
It injects a message into FluxRig (Client Side) and verifies it arrives at the Mock Server (Server Side).

Flow:
  [Injector] -> (TCP) -> [FluxRig Ingress] -> [FluxRig Egress] -> (TCP) -> [Mock Server]

Usage:
    python iso8583_tool.py --e2e --host 127.0.0.1 --port 8583 --mock-port 10000
    python iso8583_tool.py --mode server --port 8585 (Standalone Mock Server)
"""

import argparse
import socket
import struct
import sys
import time
import binascii
import threading
import queue
import iso8583
from iso8583.specs import default_ascii, default
import copy

financial_bcd = copy.deepcopy(default)
financial_bcd["t"] = {
    "data_enc": "b",
    "len_enc": "ascii",
    "len_type": 0,
    "max_len": 2,
    "desc": "Message Type",
}

SPECS = {
    "ascii": default_ascii,
    "bcd": financial_bcd
}

# EBCDIC Helpers
EBCDIC_MAP = {
    '0': 0xF0, '1': 0xF1, '2': 0xF2, '3': 0xF3, '4': 0xF4,
    '5': 0xF5, '6': 0xF6, '7': 0xF7, '8': 0xF8, '9': 0xF9,
}

def to_ebcdic(s):
    return bytes([EBCDIC_MAP.get(c, 0x00) for c in s])

def ascii_to_ebcdic_numeric(data):
    """Simple ASCII to EBCDIC mapping for hex digits 0-F."""
    res = bytearray()
    for b in data:
        if 0x30 <= b <= 0x39: # '0'-'9'
            res.append(b + 0xC0)
        elif 0x41 <= b <= 0x46: # 'A'-'F'
            res.append(b + 0x80)
        elif 0x61 <= b <= 0x66: # 'a'-'f'
            res.append(b + 0x20)
        else:
            res.append(b)
    return bytes(res)

def build_visa_header(payload_len, src_id="445566", dst_id="112233"):
    """Build a 22-byte Visa V.I.P Header."""
    h = bytearray(22)
    h[0] = 0x16  # Length 22
    h[1] = 0x01  # Standard
    h[2] = 0x02  # Visa Standard Text
    
    # Total Length (Header + Payload)
    total_len = 22 + payload_len
    h[3] = (total_len >> 8) & 0xFF
    h[4] = total_len & 0xFF
    
    # Dest ID (BCD 3 bytes)
    h[5:8] = bytes.fromhex(dst_id)
    # Src ID (BCD 3 bytes)
    h[8:11] = bytes.fromhex(src_id)
    
    return bytes(h)

def encode_with_length_prefix(message_bytes, header_bytes=2, endian='big'):
    """Add length prefix to ISO8583 message."""
    length = len(message_bytes)
    fmt = ">" if endian == 'big' else "<"
    if header_bytes == 2:
        header = struct.pack(f"{fmt}H", length)
    else:
        header = struct.pack(f"{fmt}I", length)
    return header + message_bytes

def decode_with_length_prefix(data, header_bytes=2, endian='big'):
    """Extract ISO8583 message from length-prefixed frame."""
    fmt = ">" if endian == 'big' else "<"
    if header_bytes == 2:
        length = struct.unpack(f"{fmt}H", data[:2])[0]
        return data[2:2+length]
    else:
        length = struct.unpack(f"{fmt}I", data[:4])[0]
        return data[4:4+length]

# -----------------------------------------------------------------------------
# Mock Server Logic (Runs in Background Thread)
# -----------------------------------------------------------------------------
class MockServer(threading.Thread):
    def __init__(self, port, expected_queue, variant="none", standalone=False):
        super().__init__()
        self.port = port
        self.expected_queue = expected_queue # Queue of (payload_bytes, description)
        self.variant = variant
        self.standalone = standalone
        self.server_socket = None
        self.running = True
        self.error = None
        
    def run(self):
        print(f"[MOCK] Listening on port {self.port}...")
        self.server_socket = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        self.server_socket.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        try:
            self.server_socket.bind(('0.0.0.0', self.port))
            self.server_socket.listen(1)
            self.server_socket.settimeout(2.0) # Check self.running periodically
            
            while self.running:
                try:
                    conn, addr = self.server_socket.accept()
                except socket.timeout:
                    continue
                    
                print(f"[MOCK] Connection from {addr}")
                t = threading.Thread(target=self.handle_connection, args=(conn,))
                t.daemon = True
                t.start()
                
        except Exception as e:
            if self.running:
                print(f"[MOCK] Critical Error: {e}")
                self.error = e
        finally:
            if self.server_socket:
                self.server_socket.close()

    def handle_connection(self, conn):
        try:
            conn.settimeout(5.0)
            while self.running:
                # 1. Read Length (Default to BIG endian for network headers)
                len_bytes = conn.recv(2)
                if not len_bytes:
                    break
                    
                length = struct.unpack(">H", len_bytes)[0]
                payload = conn.recv(length)
                
                if not payload:
                    break

                if self.standalone:
                    # Echo Mode
                    print(f"[MOCK] Received {length} bytes. Echoing...")
                    # Basic Echo: Just send it back
                    frame = encode_with_length_prefix(payload)
                    conn.sendall(frame)
                    continue

                try:
                     # Get expected message from Queue
                     expected_payload, desc = self.expected_queue.get(timeout=1.0)
                except queue.Empty:
                     print(f"[MOCK] Received unexpected message (len={len(payload)}) - No expectation pending")
                     continue

                print(f"[MOCK] Verifying {desc}...")
                
                # Verification Logic
                match = False
                if self.variant == "visa":
                    match = (payload == expected_payload)
                else:
                    match = (payload == expected_payload)
                
                if match:
                    print(f"[MOCK] ✅ VALIDATED: {desc}")
                    self.expected_queue.task_done()
                else:
                    print(f"[MOCK] ❌ MISMATCH: {desc}")
                    print(f"   Expected: {expected_payload.hex().upper()}")
                    print(f"   Received: {payload.hex().upper()}")
                    # Fail hard? Or just log? For now, log.
                    self.error = AssertionError(f"Mismatch in {desc}")
                    self.expected_queue.task_done()
                    
        except Exception as e:
            print(f"[MOCK] Connection Error: {e}")
        finally:
            conn.close()

    def stop(self):
        self.running = False


# -----------------------------------------------------------------------------
# Builders & Senders
# -----------------------------------------------------------------------------
def build_0800_network_management():
    # ...same as before...
    return {
        "t": "0800", "7": "0124160000", "11": "000001", "70": "001"
    }

def build_0200_authorization(stan):
    return {
        "t": "0200", "2": "4111111111111111", "3": "000000", "4": "000000010000",
        "7": "0124160000", "11": stan, "12": "160000", "13": "0124", "14": "2512",
        "22": "051", "23": "001", "25": "00", "26": "12",
        "35": "4111111111111111D2512101123400001", "37": "123456789012",
        "41": "TERM0001", "42": "MERCHANT0000001", "43": "TEST MERCHANT".ljust(40),
        "49": "840"
    }

def prepare_message_bytes(decoded_msg, spec, variant=None):
    """Encodes message to bytes WITHOUT length prefix (Verification Baseline)."""
    raw_bytes, _ = iso8583.encode(decoded_msg, spec)
    if variant == "visa":
        header = build_visa_header(len(raw_bytes))
        ebcdic_bytes = ascii_to_ebcdic_numeric(raw_bytes)
        return header + ebcdic_bytes
    return raw_bytes

def send_prepared_message(sock, payload, header_bytes=2, endian='big'):
    frame = encode_with_length_prefix(payload, header_bytes, endian)
    sock.sendall(frame)
    return frame

# -----------------------------------------------------------------------------
# Main E2E Logic
# -----------------------------------------------------------------------------
def run_e2e(args):
    print(f"=== ISO8583 E2E Verifier ===")
    print(f"Injector: {args.host}:{args.port}")
    print(f"Mock Srv: Port {args.mock_port}")
    
    # 1. Start Mock Server
    expectation_queue = queue.Queue()
    mock_server = MockServer(args.mock_port, expectation_queue, args.variant)
    mock_server.start()
    time.sleep(1) # Allow bind
    
    spec = SPECS[args.encoding]
    exit_code = 0
    
    try:
        # 2. Connect Injector
        sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        sock.settimeout(10)
        sock.connect((args.host, args.port))
        print(f"[INJECTOR] Connected to Gateway {args.host}:{args.port}")

        # 3. Test Loop
        
        # --- Msg 1: 0800 ---
        print("\n--- Transaction 1: 0800 Sign-On ---")
        msg_0800 = build_0800_network_management()
        # What arrives at Mock?
        # If Variant=Visa, Injector sends EBCDIC+Header, FluxRig should pass it through (Transparent Proxy logic for now)
        # Verify: The bytes we send are the bytes we expect to receive (assuming Transparent Proxy)
        payload_0800 = prepare_message_bytes(msg_0800, spec, args.variant)
        
        # Queue Expectation -> Send Check
        expectation_queue.put((payload_0800, "0800 Sign-On"))
        
        # Send
        send_prepared_message(sock, payload_0800, args.header_bytes, args.endian)
        print("[INJECTOR] Sent 0800")
        
        # Wait for Queue to drain (Validation to complete)
        # We need to ensure we don't block forever if Mock fails
        try:
             # Wait up to 5s for the Mock to process
             # Since queue.join() blocks until task_done(), this works.
             # But it doesn't support timeout natively in Python < 3.
             # We can busy wait on empty()
             t_start = time.time()
             while not expectation_queue.empty():
                 if time.time() - t_start > 5:
                     print("[ERROR] Timeout waiting for verification")
                     exit_code = 1
                     break
                 if mock_server.error:
                     print(f"[ERROR] Mock Server reported error: {mock_server.error}")
                     exit_code = 1
                     break
                 time.sleep(0.1)
        except Exception as e:
             print(f"[ERROR] Wait failed: {e}")
             exit_code = 1
             
        if exit_code != 0: raise Exception("Verification Failed")

        # --- Msg 2..N: 0200 ---
        for i in range(args.count):
            if exit_code != 0: break
            print(f"\n--- Transaction {i+2}: 0200 Auth ---")
            stan = f"{i+1:06d}"
            msg_0200 = build_0200_authorization(stan)
            payload_0200 = prepare_message_bytes(msg_0200, spec, args.variant)
            
            expectation_queue.put((payload_0200, f"0200 Auth STAN={stan}"))
            send_prepared_message(sock, payload_0200, args.header_bytes, args.endian)
            print(f"[INJECTOR] Sent 0200 (STAN {stan})")
            
            # Wait
            t_start = time.time()
            while not expectation_queue.empty():
                 if time.time() - t_start > 5:
                     print("[ERROR] Timeout waiting for verification")
                     exit_code = 1
                     break
                 if mock_server.error:
                     exit_code = 1
                     break
                 time.sleep(0.1)
            time.sleep(args.delay)

    except Exception as e:
        print(f"[ERROR] Test Aborted: {e}")
        exit_code = 1
    finally:
        sock.close()
        mock_server.stop()
        # Connect to mock to unblock accept() if needed? 
        # (Our Mock has timeout loop, so it should exit)
        mock_server.join(timeout=2)
        print("=== Test Finished ===")
    
    sys.exit(exit_code)

def run_standalone_server(args):
    print(f"=== ISO8583 Standalone Mock Server ===")
    print(f"Listening on Port {args.port} (Echo Mode)")
    
    mock_server = MockServer(args.port, None, standalone=True)
    mock_server.start()
    
    try:
        while True:
            time.sleep(1)
    except KeyboardInterrupt:
        print("Stopping...")
        mock_server.stop()
        mock_server.join()

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--e2e", action="store_true", help="Run in E2E Verification Mode")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=8583)
    parser.add_argument("--mock-port", type=int, default=10000)
    parser.add_argument("--count", type=int, default=1)
    parser.add_argument("--delay", type=float, default=0.2)
    parser.add_argument("--header-bytes", type=int, default=2, choices=[2, 4])
    parser.add_argument("--endian", default="big", choices=["big", "little"])
    parser.add_argument("--encoding", default="ascii", choices=["ascii", "bcd"])
    parser.add_argument("--variant", choices=["none", "visa"], default="none")
    parser.add_argument("--mode", default="client", help="Legacy mode (client/server)") 
    
    args = parser.parse_args()
    
    if args.e2e:
        run_e2e(args)
    elif args.mode == 'server':
        run_standalone_server(args)
    else:
        print("Use --e2e for E2E test or --mode server for standalone mock.")
        sys.exit(1)

if __name__ == "__main__":
    main()

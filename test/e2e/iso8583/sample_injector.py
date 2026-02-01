#!/usr/bin/env python3
import socket
import argparse
import time
import subprocess
import os
import sys

def get_pcap_payloads(pcap_path):
    """Extracts TCP payloads from a PCAP file using tshark."""
    try:
        cmd = [
            "tshark", "-r", pcap_path,
            "-Y", "tcp.payload",
            "-T", "fields", "-e", "tcp.payload"
        ]
        result = subprocess.run(cmd, capture_output=True, text=True, check=True)
        payloads = []
        for line in result.stdout.splitlines():
            line = line.strip().replace(":", "")
            if line:
                payloads.append(bytes.fromhex(line))
        return payloads
    except subprocess.CalledProcessError as e:
        print(f"Error reading PCAP {pcap_path}: {e}")
        return []
    except FileNotFoundError:
        print("Error: tshark not found in PATH")
        sys.exit(1)

def main():
    parser = argparse.ArgumentParser(description="Inject payloads from PCAP files into a TCP listener.")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=8583)
    parser.add_argument("--pcap", nargs="+", help="Path to PCAP file(s)")
    args = parser.parse_args()

    files = []
    if args.pcap:
        files.extend(args.pcap)

    if not files:
        print("No PCAP files specified. Use --pcap.")
        sys.exit(1)

    for pcap in sorted(files):
        print(f"Reading PCAP: {os.path.basename(pcap)}")
        payloads = get_pcap_payloads(pcap)
        
        if not payloads:
            print("  No payloads found.")
            continue

        try:
            with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
                s.settimeout(10)
                print(f"  Connecting to {args.host}:{args.port}...")
                s.connect((args.host, args.port))
                
                for i, payload in enumerate(payloads):
                    if len(payload) < 2:
                        continue
                    
                    print(f"  Sending payload {i+1} ({len(payload)} bytes)...")
                    s.sendall(payload)
                    # No sleep between messages for max throughput
                
                # Wait briefly after the last message to allow server response before closing
                print("  Batch complete. Waiting for trailing responses...")
                time.sleep(0.5)
                
        except Exception as e:
            print(f"  Connection error: {e}")

if __name__ == "__main__":
    main()

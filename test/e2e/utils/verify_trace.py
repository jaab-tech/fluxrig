# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

import re
import sys
import datetime

def verify_log(log_path):
    print(f"[VerifyTrace] Scanning {log_path}...")
    
    # Regex for UUIDs
    UUID_PATTERN = r'[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}'
    id_re = re.compile(rf'flux_id="?({UUID_PATTERN})"?(?:\s+|$)')
    path_re = re.compile(r'flux_path="?(\[\{.*?\}\])"?')

    # Entity Decoding Logic
    ENTITY_TYPES = {
        0x01: "Cluster", 0x02: "Mixer", 0x03: "FluxMsg", 0x04: "Rack", 
        0x05: "Gear", 0x06: "PortIn", 0x07: "PortOut", 0x08: "Wire"
    }

    def decode_eid_components(val_str):
        # We don't decode UUID components for now as they are opaque v7
        return "ID", 0, 0

    def format_ts_human(ns_str):
        try:
            ts_sec = int(ns_str) / 1e9
            dt = datetime.datetime.fromtimestamp(ts_sec)
            return dt.strftime('%H:%M:%S.%f')[:-3]
        except:
            return ns_str

    # Mapping (ID (hex string) -> Name)
    id_map = {}
    traces = {}
    
    # regex for subject parsing: subject="flux.gear.<gear>.<port>"
    subject_re = re.compile(r'subject="flux\.gear\.([^"]+)\.([^"]+)"')
    # regex for port_id="<uuid>"
    pid_re = re.compile(rf'port_id="?({UUID_PATTERN})"?(?:\s+|$)')
    # regex for flux.name="..."
    fname_re = re.compile(r'flux\.name="([^"]+)"')
    # regex for Bus Receive: Bus Receive | ... port_id="..." gear="..."
    bus_recv_re = re.compile(rf'Bus Receive.*port_id="?({UUID_PATTERN})"?.*gear="([^"]+)"')

    with open(log_path, 'r') as f:
        for line in f:
            # check for flux_id on this line
            m_id = id_re.search(line)
            fid = m_id.group(1) if m_id else None
            
            # --- Name Mapping Logic ---
            # 1. Map Port Names from Subject (Outputs)
            # Log: Bus Emit | ... port_id="..." ... subject="flux.gear.gateway-server.out"
            m_pid = pid_re.search(line)
            m_sub = subject_re.search(line)
            if m_pid and m_sub:
                pid = m_pid.group(1)
                pname = m_sub.group(2) # "out"
                id_map[pid] = pname

            # 2. Map Input Ports from Bus Receive
            # Log: Bus Receive | ... port_id="..." gear="..."
            m_recv = bus_recv_re.search(line)
            if m_recv:
                pid = m_recv.group(1)
                # We assume standard input port is named "in", or we could use "in(<Gear>)"
                # But simple "in" aligns with "out"
                id_map[pid] = "in" 

            # 3. Map Gear Names from Context
            # If line belongs to a gear (flux.name="...") and mentions a gear ID in path, 
            # we can associate them IF the path is short or we use heuristics.
            # Simpler: If this log line HAS a flux_path, and IS a GEAR log (flux.name found),
            # then the LAST hop in the path (or the one matching local clock) is likely the current gear.
            # But wait, flux_path logs are sent *after* processing.
            # Heuristic: The Gear ID in the path usually repeats. If flux.name="gateway-server",
            # any "g:0x..." in the path belonging to this machine/seq is likely it.
            # Let's simple-map: Check all g:IDs in the path. If flux.name is present, map them.
            # (Note: Valid for simple scenarios where logs aren't interleaved heavily or diverse).
            m_fname = fname_re.search(line)
            m_path = path_re.search(line)
            
            if m_fname and m_path:
                gname = m_fname.group(1)
                path_str = m_path.group(1)
                # Find all g:UUIDs
                g_ids = re.findall(rf'g:({UUID_PATTERN})', path_str)
                for gid in g_ids:
                     # Just overwrite (safe because Gear ID <-> Name is 1:1 in a session)
                     id_map[gid] = gname

            # --- End Mapping Logic ---

            if m_path and fid:
                path_str = m_path.group(1)
                if fid not in traces:
                    traces[fid] = []
                if path_str not in traces[fid]:
                    traces[fid].append(path_str)

    if not traces:
         pass

    print(f"[VerifyTrace] Found traces for {len(traces)} FluxIDs.")
    print(f"[VerifyTrace] Discovered {len(id_map)} ID mappings.")
    
    # Hop Regex: {g:<uuid>, p:<uuid>, t:...}
    hop_re = re.compile(rf'\{{g:({UUID_PATTERN})\s+p:({UUID_PATTERN})\s+t:(\d+)\}}')

    for fid, paths in traces.items():
        print(f"- {fid}")
        
        for p_str in paths:
            hops = hop_re.findall(p_str)
            if not hops:
                print(f"   - (Empty or Malformed Path)")
                continue
                
            for (g_hex, p_hex, t_val) in hops:
                # Decode Gear
                if g_hex in id_map:
                    g_str = f"{id_map[g_hex]}" # Use Name!
                else:
                    g_type, g_mid, g_seq = decode_eid_components(g_hex)
                    g_str = f"{g_type}(M:{g_mid},S:{g_seq})"
                
                # Decode Port
                if p_hex in id_map:
                    p_str = f"{id_map[p_hex]}" # Use Name!
                else:
                    p_type, p_mid, p_seq = decode_eid_components(p_hex)
                    p_str = f"{p_type}(M:{p_mid},S:{p_seq})" if p_type != "Unk" else p_hex
                
                # Timestamp
                t_readable = format_ts_human(t_val)
                # Assuming trace header TS is also UTC (safe bet for machine logs)
                
                print(f"   - {g_str:<25} | {p_str:<20} | {t_readable} UTC")
            print("") # newline between paths

    # Validation Logic (Exit Code)
    if not traces:
        # Check if we at least found IDs?
        # The original script failed if no paths found.
        # But maybe we only see paths on some nodes.
        # Strict mode: Fail if no paths found.
        print("[VerifyTrace] FAIL: No 'flux_path' traces found.")
        sys.exit(1)
    
    print("[VerifyTrace] SUCCESS: FluxMsg IDs and Paths validated.")

if __name__ == "__main__":
    if len(sys.argv) < 2:
        print("Usage: verify_trace.py <log_file>")
        sys.exit(1)
    verify_log(sys.argv[1])

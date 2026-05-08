# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

import sys
import os
import re
import json
import subprocess
from datetime import datetime, timedelta, timezone

def parse_physical_line(line):
    line = line.strip()
    if not line:
        return None

    # Try Pipe Format (Mixer/Rack v0.5.0)
    # Format: TS | LEVEL | TYPE | NAME | SOURCE | MSG | [ATTRS]
    if " | " in line:
        parts = [p.strip() for p in line.split('|')]
        
        # Find Source Column (contains .go:)
        src_idx = -1
        for i, p in enumerate(parts):
            if ".go:" in p:
                src_idx = i
                break
        
        if src_idx != -1 and len(parts) > src_idx + 1:
            return {
                "ts_str": parts[0],
                "level": parts[1],
                "msg": parts[src_idx + 1],
                "raw": line
            }

    # Try Key-Value Format (Legacy slog)
    m = re.search(r'time=([^\s]+)\s+level=([^\s]+)\s+msg=(.*)', line)
    if m:
        ts_str = m.group(1).strip('"')
        level = m.group(2).strip('"')
        rest = m.group(3)
        
        msg_val = ""
        if rest.startswith('"'):
            end_quote = rest.find('"', 1)
            if end_quote != -1:
                msg_val = rest[1:end_quote]
            else:
                msg_val = rest
        else:
            msg_val = rest.split(' ', 1)[0]
            
        return {
            "ts_str": ts_str,
            "level": level,
            "msg": msg_val,
            "raw": line
        }

    return None


def normalize_msg(msg):
    # Remove variable parts if necessary, but exact match is better first
    return msg.strip()

def normalize_level(level):
    """Normalize log levels to canonical names for comparison.
    
    OTel uses severity names like DEBUG-4, DEBUG-3, etc.
    Our physical logs use TRACE, DEBUG, INFO, WARN, ERROR.
    """
    level = level.upper().strip()
    
    # Map OTel DEBUG-X levels to our canonical names
    if level.startswith("DEBUG-"):
        # DEBUG-4 = TRACE (lowest debug level)
        # DEBUG-3, DEBUG-2, DEBUG-1 = DEBUG
        suffix = level.split("-")[1] if "-" in level else "0"
        if suffix == "4":
            return "TRACE"
        return "DEBUG"
    
    # Map WARN to WARNING and vice versa
    if level == "WARN":
        return "WARN"
    if level == "WARNING":
        return "WARN"
    
    return level

def get_parquet_logs(work_dir):
    # Dump parquet to JSON using duckdb
    cmd = [
        "duckdb",
        "-c",
        f"SET TimeZone='UTC'; COPY (SELECT strftime(timestamp, '%Y-%m-%dT%H:%M:%S.%g') as ts_str, severity as level, body as msg, attributes FROM (SELECT * FROM read_parquet('{work_dir}/mixer/data/telemetry/logs/**/*.parquet') ORDER BY timestamp ASC)) TO 'parquet_dump.json' (FORMAT JSON, ARRAY true)"
    ]
    subprocess.run(cmd, check=True)
    
    with open('parquet_dump.json', 'r') as f:
        data = json.load(f)
    return data

def main():
    if len(sys.argv) < 2:
        print("Usage: python3 compare_logs.py <work_dir>")
        sys.exit(1)
        
    work_dir = sys.argv[1]
    rack_log_path = os.path.join(work_dir, "rack/logs/rack.log")
    mixer_log_path = os.path.join(work_dir, "mixer/logs/mixer.log")
    
    physical_logs = []
    
    for log_path in [rack_log_path, mixer_log_path]:
        if not os.path.exists(log_path):
            print(f"Log not found: {log_path}")
            continue
            
        print(f"Scanning Physical Log: {log_path}")
        with open(log_path, 'r') as f:
            for line in f:
                parsed = parse_physical_line(line)
                if parsed:
                    physical_logs.append(parsed)

    # Sort merged logs by timestamp if needed, but matching loop handles order somewhat
    # Matching logic is effectively looking for existence in Parquet set
    
    print(f"Scanning Parquet (via DuckDB)...")
    try:
        parquet_logs = get_parquet_logs(work_dir)
    except Exception as e:
        print(f"Failed to read parquet: {e}")
        sys.exit(1)

    print(f"\n--- Log Files Summary ---")
    print(f"{'File':<40} | {'Lines':<8} | {'Size (KB)':<10}")
    print("-" * 65)
    
    total_physical_lines = 0
    # Include mixer_telemetry.log if it exists
    mixer_tel_log = os.path.join(work_dir, "mixer/logs/mixer_telemetry.log")
    log_candidates = [rack_log_path, mixer_log_path, mixer_tel_log]
    
    for lp in log_candidates:
        if os.path.exists(lp):
            size_kb = os.path.getsize(lp) / 1024
            with open(lp, 'r') as f:
                lines = sum(1 for _ in f)
            print(f"{os.path.basename(lp):<40} | {lines:<8} | {size_kb:<10.2f}")
            total_physical_lines += lines
            
            # Reset pointer for parsing
    print("-" * 65)
    print(f"{'Total Physical':<40} | {total_physical_lines:<8} |")
    print(f"\n--- Parquet Metrics ---")
    print(f"Total Parquet Rows: {len(parquet_logs)}")
    
    # Entity Distribution using DuckDB
    print(f"\n--- Parquet Entity Distribution ---")
    print(f"{'Type':<15} | {'Name':<30} | {'Count':<8}")
    print("-" * 60)
    
    try:
        # We need to query the parquet files again for the new columns
        # Since get_parquet_logs used a fixed schema, we query dynamically here for display
        cmd_dist = [
            "duckdb", "-noheader", "-list", "-c",
            f"SELECT entity_type, entity_name, count(*) FROM read_parquet('{work_dir}/mixer/data/telemetry/logs/**/*.parquet') GROUP BY entity_type, entity_name ORDER BY count(*) DESC"
        ]
        res = subprocess.run(cmd_dist, capture_output=True, text=True)
        if res.returncode == 0:
            for line in res.stdout.strip().split('\n'):
                if not line: continue
                parts = line.split('|')
                if len(parts) >= 3:
                   etype = parts[0].strip() or "UNKNOWN"
                   ename = parts[1].strip()
                   count = parts[2].strip()
                   print(f"{etype:<15} | {ename:<30} | {count:<8}")
        else:
            print(f"Error querying distribution: {res.stderr}")
    except Exception as e:
        print(f"Failed to query distribution: {e}")

    print(f"\n--- Parity Check ---")
    print(f"Physical Logs Scanned (Parsed): {len(physical_logs)}")
    print(f"Parquet Logs Loaded:            {len(parquet_logs)}")
    
    # Matching Logic
    # We iterate physical logs and look for matches in Parquet
    # Since order is generally preserved, we can use a window or simple list removal?
    # List removal is O(N^2), but N ~ 200, so it's fine.
    
    matched_indices = set()
    matches = []
    missing = []
    
    # Optimize: Create a list of parquet items with a "used" flag
    p_items = [{"data": p, "used": False} for p in parquet_logs]
    
    for plog in physical_logs:
        found = False
        p_msg = normalize_msg(plog['msg'])
        p_level = normalize_level(plog['level'])
        
        for i, item in enumerate(p_items):
            if item["used"]:
                continue
            
            # Comparison Criteria
            # 1. Message Body (Exact after normalization)
            q_msg = normalize_msg(item["data"]["msg"])
            
            if p_msg != q_msg:
                continue
                
            # 2. Level Check (Normalized)
            q_level = normalize_level(item["data"]["level"]) if item["data"]["level"] else ""
            if p_level != q_level:
                continue
            
            # 3. Timestamp Check (Tolerance 10s to account for timezone issues)
            try:
                # Parse physical log timestamp
                p_ts_str = plog['ts_str']
                # If physical log has a timezone offset, normalize it to UTC
                p_dt = datetime.fromisoformat(p_ts_str)
                if p_dt.tzinfo is not None:
                    p_dt = p_dt.astimezone(timezone.utc)
                else:
                    # If naive, assume UTC for comparison purposes
                    p_dt = p_dt.replace(tzinfo=timezone.utc)
                
                # Parse parquet log timestamp (DuckDB strftime output is naive UTC)
                q_str = item["data"]["ts_str"]
                q_dt = datetime.fromisoformat(q_str)
                if q_dt.tzinfo is None:
                    q_dt = q_dt.replace(tzinfo=timezone.utc)
                else:
                    q_dt = q_dt.astimezone(timezone.utc)
                    
                delta = abs((p_dt - q_dt).total_seconds())
                
                # FALLBACK: If they are exactly N hours apart (timezone mismatch), consider it a match
                # This handles cases where one side is local and the other is UTC but the wall clock is the same.
                is_wall_match = False
                if delta > 300: # If more than 5 mins apart, check wall clock
                     # Remove TZ and compare naive
                     p_wall = p_dt.replace(tzinfo=None)
                     q_wall = q_dt.replace(tzinfo=None)
                     # Wait! We need to shift p_dt back to local if it was normalized to UTC
                     # Actually, let's just parse the strings and take first 19 chars
                     if p_ts_str[:19] == q_str[:19]:
                         is_wall_match = True

                if delta < 10.0 or is_wall_match:
                    item["used"] = True
                    found = True
                    matches.append((plog, item["data"]))
                    break
            except Exception as e:
                # If timestamp parsing fails, match on message+level only
                print(f"Match Warning: TS Parse Error: {e}")
                item["used"] = True
                found = True
                matches.append((plog, item["data"]))
                break
        
        if not found:
            missing.append(plog)
    
    # Calculate unmatched parquet logs
    unmatched_parquet = sum(1 for item in p_items if not item["used"])
    
    print(f"\n--- Parity Summary ---")
    print(f"Physical Logs:       {len(physical_logs)}")
    print(f"Parquet Logs:        {len(parquet_logs)}")
    print(f"Count Difference:    {len(physical_logs) - len(parquet_logs)} (Physical - Parquet)")
    print(f"")
    print(f"Matched:             {len(matches)}")
    print(f"Physical NOT in Parquet: {len(missing)}")
    print(f"Parquet NOT in Physical: {unmatched_parquet}")
    
    # Consistency check
    if len(missing) != (len(physical_logs) - len(parquet_logs) + unmatched_parquet):
        print(f"\n[WARN] Matching inconsistency detected!")
        print(f"       Expected missing = {len(physical_logs) - len(parquet_logs) + unmatched_parquet}")
        print(f"       Actual missing   = {len(missing)}")
        print(f"       This may indicate timestamp/level mismatch issues in the matching algorithm.")
    
    # Show unmatched parquet logs for debugging
    if unmatched_parquet > 0:
        print(f"\n--- Unmatched Parquet Logs (In Parquet, Not matched to Physical) ---")
        unmatched_items = [item["data"] for item in p_items if not item["used"]]
        for i, item in enumerate(unmatched_items[:10]):
            print(f"[{i+1}] {item['ts_str']} | {item['level']} | {item['msg'][:60]}...")
    
    print(f"\n--- Sample Missing Logs (In Physical, Not in Parquet) ---")
    for i, m in enumerate(missing[:20]):
        print(f"[{i+1}] {m['ts_str']} | {m['level']} | {m['msg']}")
        if i == 19:
            print("... (more)")

    print(f"\n--- Analysis of Top Missing Categories ---")
    # Group by message prefix or type
    counts = {}
    # Analysis of missing categories
    counts = {}
    for m in missing:
        msg = m['msg'].lower()
        matched_key = "Other"
        
        # Categorize Infrastructure Noise (Expected before bus is ready)
        if "dial failed" in msg or "connection refused" in msg or "nats: no servers available" in msg:
            matched_key = "Expected Bootstrap Noise (Dial/Connection)"
        elif "interrupt signal received" in msg or "shutting down" in msg:
            matched_key = "Expected Shutdown Noise"
        
        counts[matched_key] = counts.get(matched_key, 0) + 1
        
    print("\n--- Analysis of Missing Categories (Physical NOT in Parquet) ---")
    for k, v in sorted(counts.items(), key=lambda x: x[1], reverse=True):
        print(f"{k}: {v}")

    # Clean up
    if os.path.exists("parquet_dump.json"):
        os.remove("parquet_dump.json")

if __name__ == "__main__":
    main()

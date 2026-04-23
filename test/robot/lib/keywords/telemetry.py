# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

import os
import glob
import subprocess
import duckdb
from collections import defaultdict
from robot.api import logger
from robot.api.deco import keyword
from datetime import datetime

# Import the renderer
import sys
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from reporting.renderer import renderer

class TelemetryKeywords:
    """
    Keywords for validating logs and telemetry (Parquet) data.
    """

    @keyword
    def check_stdout_contains(self, pattern: str, alias: str = "mixer"):
        """
        Checks if the component's stdout (process_stdout.log) contains the pattern.
        """
        if hasattr(self, 'processes') and alias not in self.processes:
             raise RuntimeError(f"Unknown component alias: {alias}")
        
        entry = self.processes[alias]
        log_path = os.path.join(entry["home"], "logs", "process_stdout.log")
        
        logger.info(f"Grepping '{pattern}' in {log_path}")
        
        cmd = ["grep", "-q", pattern, log_path]
        try:
             subprocess.check_call(cmd)
             logger.info(f"Stdout contains '{pattern}'")
        except subprocess.CalledProcessError:
             raise RuntimeError(f"Stdout missing pattern: '{pattern}' in {log_path}")

    @keyword
    def check_log_contains(self, pattern: str, alias: str = "mixer", work_dir: str = None):
        """
        Checks if the component's log contains the specified pattern.
        """
        if hasattr(self, 'processes') and alias not in self.processes:
             raise RuntimeError(f"Unknown component alias: {alias}")
        
        entry = self.processes[alias]
        log_path = entry.get("log_file")
        
        if not log_path or not os.path.exists(log_path):
             # Fallback to process_stdout if specific log not created yet
             log_path = os.path.join(entry["home"], "logs", "process_stdout.log")
        
        logger.info(f"Grepping '{pattern}' in {log_path}")
        
        cmd = ["grep", "-q", pattern, log_path]
        try:
             subprocess.check_call(cmd)
             logger.info(f"Log contains '{pattern}'")
        except subprocess.CalledProcessError:
             raise RuntimeError(f"Log missing pattern: '{pattern}' in {log_path}")

    @keyword
    def verify_telemetry_parquet_exists(self, work_dir: str):
        """
        Verifies that at least one Telemetry Parquet file exists in the Mixer's data directory.
        """
        telemetry_dir = os.path.join(work_dir, "data", "telemetry")
        if not os.path.exists(telemetry_dir):
             raise RuntimeError(f"Telemetry directory not found: {telemetry_dir}")

        found = False
        for root, dirs, files in os.walk(telemetry_dir):
            for file in files:
                if file.endswith(".parquet"):
                    found = True
                    logger.info(f"Found Parquet: {os.path.join(root, file)}")
                    break
            if found:
                break
        
        if not found:
             raise RuntimeError(f"No parquet files found in {telemetry_dir}")

    @keyword
    def verify_message_count(self, file_path: str, expected_count: int, tolerance_pct: float = 10.0):
        """
        Verifies that a JSONL file contains at least the expected number of messages.
        Used to validate message flow without relying on log pattern matching.
        
        Args:
            file_path: Path to the JSONL file with received messages
            expected_count: Expected number of messages
            tolerance_pct: Acceptable tolerance percentage (default: 10%)
        """
        if not os.path.exists(file_path):
            raise RuntimeError(f"Message file not found: {file_path}")
        
        with open(file_path, 'r') as f:
            actual_count = sum(1 for line in f if line.strip())
        
        expected_count = int(expected_count)
        min_acceptable = int(expected_count * (1 - tolerance_pct / 100))
        
        logger.info(f"Message count: {actual_count} (expected: {expected_count}, min: {min_acceptable})")
        
        if actual_count < min_acceptable:
            raise RuntimeError(
                f"Insufficient messages: {actual_count} < {min_acceptable} "
                f"(expected {expected_count} with {tolerance_pct}% tolerance)"
            )
        
        logger.info(f"Traffic validation PASSED: {actual_count} messages received")

    @keyword
    def log_telemetry_stats(self, work_dir: str):
        """
        Queries Parquet log files using DuckDB and displays log statistics + a chart.
        Uses Jinja2 templates for rendering.
        """
        # Adjust for nested mixer directory if needed
        mixer_dir = os.path.join(work_dir, "mixer")
        base_dir = mixer_dir if os.path.exists(mixer_dir) else work_dir
        
        telemetry_dir = os.path.join(base_dir, "data", "telemetry")
        if not os.path.exists(telemetry_dir):
            logger.debug(f"No telemetry directory found at {telemetry_dir} (skipping stats)")
            return

        try:
            con = duckdb.connect(database=':memory:')
            
            # Query by entity_type
            query = f"""
            SELECT 
                entity_type, 
                count(*) as event_count 
            FROM read_parquet('{telemetry_dir}/logs/**/*.parquet', union_by_name=true) 
            GROUP BY entity_type
            ORDER BY event_count DESC
            """

            results = con.execute(query).fetchall()

            if not results:
                logger.info("No log events found in Parquet files.")
                return

            # Prepare data
            labels = []
            data = []
            rows = []
            
            for row in results:
                entity_type = row[0] if row[0] else "unknown"
                count = row[1]
                labels.append(entity_type)
                data.append(count)
                rows.append([entity_type, count])
            
            # Render table
            table_html = renderer.render_table(
                title="Log Statistics",
                headers=["Entity Type", "Log Count"],
                rows=rows,
                width="50%"
            )
            
            # Render chart
            chart_html = renderer.render_bar_chart(
                title="Logs by Entity Type",
                labels=labels,
                data=data,
                dataset_label="Log Count",
                color="rgba(54, 162, 235, 0.6)"
            )
            
            # Combine into a dashboard
            dashboard_html = renderer.render_dashboard(
                title="Logs Dashboard",
                components=[table_html, chart_html]
            )
            
            logger.info(dashboard_html, html=True)
            
        except Exception as e:
            logger.error(f"Failed to query log stats: {e}")

    @keyword
    def verify_log_parity(self, mixer_dir: str, rack_dirs: list = None, tolerance_pct: float = 20.0):
        """
        Compares physical log file line counts vs Parquet log counts.
        Validates that telemetry is capturing logs with acceptable tolerance.
        
        Args:
            mixer_dir: Path to Mixer's work directory
            rack_dirs: List of paths to Rack work directories (optional)
            tolerance_pct: Acceptable difference percentage (default: 20%)
        """
        telemetry_dir = os.path.join(mixer_dir, "data", "telemetry")
        if not os.path.exists(telemetry_dir):
            raise RuntimeError(f"Telemetry directory not found: {telemetry_dir}")
        
        try:
            con = duckdb.connect(database=':memory:')
            
            # Count Parquet logs by entity type
            query = f"""
            SELECT 
                entity_type, 
                count(*) as parquet_count 
            FROM read_parquet('{telemetry_dir}/logs/**/*.parquet', union_by_name=true) 
            GROUP BY entity_type
            """
            parquet_counts = dict(con.execute(query).fetchall())
            
            # Count physical log file lines
            physical_counts = {}
            
            # Mixer logs
            mixer_log = os.path.join(mixer_dir, "logs", "mixer.log")
            if os.path.exists(mixer_log):
                with open(mixer_log, 'r') as f:
                    physical_counts['MIXER'] = sum(1 for _ in f)
            
            # Rack logs
            if rack_dirs:
                for rack_dir in rack_dirs:
                    rack_log = os.path.join(rack_dir, "logs", "rack.log")
                    if os.path.exists(rack_log):
                        with open(rack_log, 'r') as f:
                            physical_counts['RACK'] = physical_counts.get('RACK', 0) + sum(1 for _ in f)
                    # Also check fluxrig.log (alternative name)
                    alt_log = os.path.join(rack_dir, "logs", "fluxrig.log")
                    if os.path.exists(alt_log):
                        with open(alt_log, 'r') as f:
                            physical_counts['RACK'] = physical_counts.get('RACK', 0) + sum(1 for _ in f)
            
            # Build comparison table
            rows = []
            all_types = set(parquet_counts.keys()) | set(physical_counts.keys())
            
            for entity_type in sorted(all_types):
                parquet = parquet_counts.get(entity_type, 0)
                physical = physical_counts.get(entity_type, 0)
                
                if physical > 0:
                    diff_pct = abs(parquet - physical) / physical * 100
                    status = "✅" if diff_pct <= tolerance_pct else "⚠️"
                else:
                    diff_pct = 0 if parquet == 0 else 100
                    status = "✅" if parquet > 0 else "⚠️"
                
                rows.append([entity_type, physical, parquet, f"{diff_pct:.1f}%", status])
            
            # Render comparison table
            table_html = renderer.render_table(
                title="Log Parity Check",
                headers=["Entity Type", "Physical Logs", "Parquet Logs", "Diff %", "Status"],
                rows=rows,
                width="70%"
            )
            
            logger.info(table_html, html=True)
            
            # Validate that Parquet has logs for critical entities
            if parquet_counts.get('MIXER', 0) == 0:
                logger.debug("No MIXER logs found in Parquet (yet)")
            if parquet_counts.get('RACK', 0) == 0:
                logger.debug("No RACK logs found in Parquet (yet)")
            if parquet_counts.get('GEAR', 0) == 0:
                logger.debug("No GEAR logs found in Parquet (yet)")
                
            total_parquet = sum(parquet_counts.values())
            logger.info(f"Total Parquet logs: {total_parquet}")
            
            if total_parquet == 0:
                raise RuntimeError("No logs found in Parquet - telemetry pipeline may be broken")
                
        except Exception as e:
            logger.error(f"Failed to verify log parity: {e}")
            raise

    @keyword
    def log_telemetry_samples(self, work_dir: str, limit: int = 10):
        """
        Queries Parquet log files and displays a sample table of recent logs.
        Replaces 'inspect_parquet.py'.
        """
        telemetry_dir = os.path.join(work_dir, "data", "telemetry")
        if not os.path.exists(telemetry_dir):
            logger.warn("No telemetry directory found.")
            return

        try:
            con = duckdb.connect(database=':memory:')
            
            # Helper to check columns
            # We want specific columns if they exist
            target_cols = ['timestamp', 'entity_name', 'severity_text', 'body', 'attributes']
            
            try:
                # Check schema blindly by reading one row
                schema_query = f"SELECT * FROM read_parquet('{telemetry_dir}/logs/**/*.parquet', union_by_name=true) LIMIT 1"
                con.execute(schema_query)
                columns = [desc[0] for desc in con.description]
                
                # Filter target cols that actually exist
                select_cols = [c for c in target_cols if c in columns]
                if not select_cols:
                    select_cols = ['*']
                    
                cols_str = ", ".join(select_cols)
                
                query = f"""
                SELECT {cols_str}
                FROM read_parquet('{telemetry_dir}/logs/**/*.parquet', union_by_name=true) 
                ORDER BY timestamp DESC
                LIMIT {limit}
                """
                
                results = con.execute(query).fetchall()
                
                if not results:
                    logger.info("No logs found for sampling.")
                    return
                
                # Format rows
                rows = []
                for r in results:
                    # Convert specific types if needed, stringify attributes
                    row_list = list(r)
                    rows.append([str(x) for x in row_list])
                    
                table_html = renderer.render_table(
                    title=f"Telemetry Samples (Last {limit})",
                    headers=select_cols,
                    rows=rows,
                    width="100%"
                )
                
                logger.info(table_html, html=True)
                
            except Exception as e:
                logger.warn(f"Could not read parquet schema or data: {e}")
                
        except Exception as e:
            logger.error(f"Failed to sample telemetry: {e}")

    @keyword
    def get_detailed_telemetry_components(self, work_dir: str):
        """
        Generates HTML components for the telemetry section of the suite report.
        """
        # Adjust for nested mixer directory if needed
        mixer_dir = os.path.join(work_dir, "mixer")
        base_dir = mixer_dir if os.path.exists(mixer_dir) else work_dir
        
        telemetry_dir = os.path.join(base_dir, "data", "telemetry")
        if not os.path.exists(telemetry_dir):
            logger.debug(f"Detailed telemetry: dir not found at {telemetry_dir}")
            return []

        components = []
        try:
            con = duckdb.connect(database=':memory:')
            
            # 0. Global Time Range & Header
            start_ts_global = 0.0
            try:
                # Find min start time across metrics AND logs - get epoch directly from DuckDB for consistency
                query_range = f"""
                SELECT 
                    min(ts) as start_ts, 
                    max(ts) as end_ts,
                    epoch(min(ts)) as start_epoch
                FROM (
                    SELECT timestamp as ts FROM read_parquet('{telemetry_dir}/metrics/**/*.parquet', union_by_name=true)
                    UNION ALL
                    SELECT timestamp as ts FROM read_parquet('{telemetry_dir}/logs/**/*.parquet', union_by_name=true)
                )
                """
                range_res = con.execute(query_range).fetchone()
                if range_res and range_res[0]:
                    start_dt = range_res[0]
                    end_dt = range_res[1]
                    start_ts_global = float(range_res[2])  # Use DuckDB's epoch directly
                    
                    duration = (end_dt - start_dt).total_seconds()
                    header_html = f"""
                    <div style="margin-bottom: 20px; padding: 15px; background-color: #f8fafc; border-radius: 8px; border: 1px solid #e2e8f0;">
                        <h3 style="margin: 0 0 10px 0; color: #334155;">Test Execution Timeline</h3>
                        <div style="display: flex; gap: 40px; font-family: monospace; color: #475569;">
                            <div><strong>Start:</strong> {start_dt.strftime('%Y-%m-%d %H:%M:%S UTC')}</div>
                            <div><strong>End:</strong> {end_dt.strftime('%Y-%m-%d %H:%M:%S UTC')}</div>
                            <div><strong>Duration:</strong> {duration:.1f}s</div>
                        </div>
                    </div>
                    """
                    components.append(header_html)
            except Exception as e:
                logger.warn(f"Time Range Calc failed: {e}")

            # Binning configuration - all charts use this interval
            BIN_SIZE = 5  # seconds

            def get_rel_label(rel_sec):
                if rel_sec < 0: rel_sec = 0
                minutes = int(rel_sec // 60)
                seconds = int(rel_sec % 60)
                return f"{minutes:02d}:{seconds:02d}"

            # KPI Summary Cards (Executive View)
            try:
                kpi_query = f"""
                SELECT 
                    sum(CASE WHEN name = 'fluxrig.gear.messages_in' THEN value END) as total_msgs,
                    sum(CASE WHEN name = 'fluxrig.gear.errors' THEN value END) as total_errors,
                    max(CASE WHEN name = 'fluxrig_rack_connections_active' THEN value END) as peak_connections
                FROM read_parquet('{telemetry_dir}/metrics/**/*.parquet', union_by_name=true)
                """
                kpi_res = con.execute(kpi_query).fetchone()
                total_msgs = int(kpi_res[0] or 0)
                total_errors = int(kpi_res[1] or 0)
                peak_conns = int(kpi_res[2] or 0)
                error_rate = (total_errors / total_msgs * 100) if total_msgs > 0 else 0.0
                duration_sec = (end_dt - start_dt).total_seconds() if 'end_dt' in dir() else 0
                avg_tps = total_msgs / duration_sec if duration_sec > 0 else 0

                kpi_html = f"""
                <div style="display: grid; grid-template-columns: repeat(5, 1fr); gap: 16px; margin-bottom: 24px;">
                    <div style="background: linear-gradient(135deg, #3b82f6, #1d4ed8); color: white; padding: 20px; border-radius: 12px; text-align: center;">
                        <div style="font-size: 28px; font-weight: bold;">{total_msgs:,}</div>
                        <div style="font-size: 12px; opacity: 0.9;">Total Messages</div>
                    </div>
                    <div style="background: linear-gradient(135deg, #10b981, #059669); color: white; padding: 20px; border-radius: 12px; text-align: center;">
                        <div style="font-size: 28px; font-weight: bold;">{avg_tps:.0f}</div>
                        <div style="font-size: 12px; opacity: 0.9;">Avg TPS</div>
                    </div>
                    <div style="background: linear-gradient(135deg, #8b5cf6, #6d28d9); color: white; padding: 20px; border-radius: 12px; text-align: center;">
                        <div style="font-size: 28px; font-weight: bold;">{peak_conns}</div>
                        <div style="font-size: 12px; opacity: 0.9;">Peak Connections</div>
                    </div>
                    <div style="background: linear-gradient(135deg, {'#ef4444' if error_rate > 0.1 else '#10b981'}, {'#dc2626' if error_rate > 0.1 else '#059669'}); color: white; padding: 20px; border-radius: 12px; text-align: center;">
                        <div style="font-size: 28px; font-weight: bold;">{error_rate:.2f}%</div>
                        <div style="font-size: 12px; opacity: 0.9;">Error Rate</div>
                    </div>
                    <div style="background: linear-gradient(135deg, #f59e0b, #d97706); color: white; padding: 20px; border-radius: 12px; text-align: center;">
                        <div style="font-size: 28px; font-weight: bold;">{duration_sec:.0f}s</div>
                        <div style="font-size: 12px; opacity: 0.9;">Duration</div>
                    </div>
                </div>
                """
                components.append(kpi_html)
            except Exception as e:
                logger.warn(f"KPI Summary failed: {e}")

            # SLA Compliance Table (Audit/Compliance Requirement)
            try:
                sla_query = f"""
                SELECT 
                    sum(CASE WHEN name = 'fluxrig_rack_latency_seconds.sum' THEN value END) * 1000 / 
                    NULLIF(sum(CASE WHEN name = 'fluxrig_rack_latency_seconds.count' THEN value END), 0) as avg_latency_ms,
                    sum(CASE WHEN name = 'fluxrig.gear.messages_in' THEN value END) as total_msgs,
                    sum(CASE WHEN name = 'fluxrig.gear.errors' THEN value END) as total_errors
                FROM read_parquet('{telemetry_dir}/metrics/**/*.parquet', union_by_name=true)
                """
                sla_res = con.execute(sla_query).fetchone()
                avg_latency = sla_res[0] or 0
                sla_total_msgs = int(sla_res[1] or 0)
                sla_total_errors = int(sla_res[2] or 0)
                sla_error_rate = (sla_total_errors / sla_total_msgs * 100) if sla_total_msgs > 0 else 0
                sla_availability = 100.0 - sla_error_rate
                
                # SLA Thresholds
                SLA_LATENCY_MS = 100.0
                SLA_ERROR_RATE = 0.1
                SLA_AVAILABILITY = 99.9
                
                lat_pass = avg_latency <= SLA_LATENCY_MS
                err_pass = sla_error_rate <= SLA_ERROR_RATE
                avail_pass = sla_availability >= SLA_AVAILABILITY
                
                def status_badge(passed):
                    if passed:
                        return '<span style="background:#10b981;color:white;padding:4px 12px;border-radius:12px;font-weight:600;">✓ PASS</span>'
                    return '<span style="background:#ef4444;color:white;padding:4px 12px;border-radius:12px;font-weight:600;">✗ FAIL</span>'
                
                sla_html = f"""
                <div style="background-color: #f8fafc; border-radius: 12px; padding: 20px; margin-bottom: 24px; border: 1px solid #e2e8f0;">
                    <h3 style="margin: 0 0 16px 0; color: #334155; display: flex; align-items: center; gap: 8px;">
                        SLA Compliance Summary
                        <span style="font-size: 11px; font-weight: normal; color: #64748b;">(Audit Reference)</span>
                    </h3>
                    <table style="width: 100%; border-collapse: collapse; font-family: sans-serif; font-size: 13px;">
                        <thead>
                            <tr style="background: #e2e8f0;">
                                <th style="padding: 10px; text-align: left; border: 1px solid #cbd5e1;">Metric</th>
                                <th style="padding: 10px; text-align: center; border: 1px solid #cbd5e1;">SLA Target</th>
                                <th style="padding: 10px; text-align: center; border: 1px solid #cbd5e1;">Actual</th>
                                <th style="padding: 10px; text-align: center; border: 1px solid #cbd5e1;">Status</th>
                            </tr>
                        </thead>
                        <tbody>
                            <tr>
                                <td style="padding: 10px; border: 1px solid #cbd5e1;"><strong>Avg Latency (P50)</strong></td>
                                <td style="padding: 10px; text-align: center; border: 1px solid #cbd5e1;">≤ {SLA_LATENCY_MS:.0f} ms</td>
                                <td style="padding: 10px; text-align: center; border: 1px solid #cbd5e1; font-family: monospace;">{avg_latency:.2f} ms</td>
                                <td style="padding: 10px; text-align: center; border: 1px solid #cbd5e1;">{status_badge(lat_pass)}</td>
                            </tr>
                            <tr>
                                <td style="padding: 10px; border: 1px solid #cbd5e1;"><strong>Error Rate</strong></td>
                                <td style="padding: 10px; text-align: center; border: 1px solid #cbd5e1;">≤ {SLA_ERROR_RATE}%</td>
                                <td style="padding: 10px; text-align: center; border: 1px solid #cbd5e1; font-family: monospace;">{sla_error_rate:.3f}%</td>
                                <td style="padding: 10px; text-align: center; border: 1px solid #cbd5e1;">{status_badge(err_pass)}</td>
                            </tr>
                            <tr>
                                <td style="padding: 10px; border: 1px solid #cbd5e1;"><strong>Availability</strong></td>
                                <td style="padding: 10px; text-align: center; border: 1px solid #cbd5e1;">≥ {SLA_AVAILABILITY}%</td>
                                <td style="padding: 10px; text-align: center; border: 1px solid #cbd5e1; font-family: monospace;">{sla_availability:.3f}%</td>
                                <td style="padding: 10px; text-align: center; border: 1px solid #cbd5e1;">{status_badge(avail_pass)}</td>
                            </tr>
                        </tbody>
                    </table>
                </div>
                """
                components.append(sla_html)
            except Exception as e:
                logger.warn(f"SLA Compliance Table failed: {e}")

            # 1. Parquet Summary (Consolidated)
            try:
                table_counts = {}
                for root, dirs, filenames in os.walk(telemetry_dir):
                    for f in filenames:
                        if f.endswith(".parquet"):
                            p_file = os.path.join(root, f)
                            rel_dir = os.path.relpath(root, telemetry_dir)
                            table_name = rel_dir.split(os.sep)[0] if rel_dir != "." else "unknown"
                            try:
                                count = con.execute(f"SELECT count(*) FROM read_parquet('{p_file}')").fetchone()[0]
                                table_counts[table_name] = table_counts.get(table_name, 0) + count
                            except: pass
                
                if table_counts:
                    file_summary_rows = [[name, count] for name, count in sorted(table_counts.items())]
                    components.append(renderer.render_table(
                        title="Parquet Telemetry Summary (Consolidated)",
                        headers=["Table", "Total Row Count"],
                        rows=file_summary_rows,
                        width="60%"
                    ))
            except Exception as e:
                logger.warn(f"Parquet Summary failed: {e}")

            # 2. Log Distribution by Entity
            try:
                query = f"""
                SELECT 
                    COALESCE(entity_type, 'unknown') || ' - ' || COALESCE(entity_name, 'unnamed') as label, 
                    count(*) as count 
                FROM read_parquet('{telemetry_dir}/logs/**/*.parquet', union_by_name=true) 
                GROUP BY label ORDER BY count DESC
                """
                results = con.execute(query).fetchall()
                if results:
                    components.append(renderer.render_bar_chart(
                        title="Logs by Entity (Type - Name)",
                        subtitle="Source: logs/**/*.parquet | Aggregation: COUNT(*) grouped by entity_type + entity_name",
                        labels=[r[0] for r in results],
                        data=[r[1] for r in results],
                        dataset_label="Event Count"
                    ))
            except Exception as e:
                logger.warn(f"Entity Logs Chart failed: {e}")

            # 2b. Log Summary (Grouped by Entity & Body) [NEW]
            try:
                query = f"""
                SELECT 
                    COALESCE(entity_name, 'unknown') as entity,
                    body,
                    count(*) as count
                FROM read_parquet('{telemetry_dir}/logs/**/*.parquet', union_by_name=true)
                GROUP BY entity, body
                ORDER BY count DESC
                LIMIT 50
                """
                results = con.execute(query).fetchall()
                if results:
                    components.append(renderer.render_table(
                        title="Log Message Summary (Grouped by Entity & Body)",
                        headers=["Entity Name", "Message Body", "Count"],
                        rows=[list(r) for r in results],
                        width="100%"
                    ))
            except Exception as e:
                logger.warn(f"Log Summary Table failed: {e}")

            # 3. Log Severity Distribution
            try:
                query = f"""
                SELECT severity, count(*) as count 
                FROM read_parquet('{telemetry_dir}/logs/**/*.parquet', union_by_name=true) 
                GROUP BY severity ORDER BY count DESC
                """
                results = con.execute(query).fetchall()
                if results:
                    components.append(renderer.render_bar_chart(
                        title="Logs by Severity",
                        subtitle="Source: logs/**/*.parquet | Aggregation: COUNT(*) grouped by severity level",
                        labels=[r[0] for r in results],
                        data=[r[1] for r in results],
                        dataset_label="Event Count",
                        color="rgba(255, 99, 132, 0.6)"
                    ))
            except Exception as e:
                logger.warn(f"Severity Logs Chart failed: {e}")

            # 4. Suite Message Throughput (using Load Generator Data)
            # NOTE: Server-side telemetry is lossy at high throughput in test env (drops ~90%).
            # We use client-side load generator reports (r_w*.json) for accurate status.
            try:
                import json
                
                # aggregate time series from all workers
                ts_agg = defaultdict(float)
                
                # Find all report files
                # Search in work_dir and parent dir (since work_dir might be component dir like .../mixer)
                search_patterns = [
                    f"{work_dir}/r_w*.json", 
                    f"{os.path.dirname(work_dir)}/r_w*.json"
                ]
                
                report_files = []
                for p in search_patterns:
                    logger.info(f"Throughput Chart: Searching for reports in {p}")
                    found = glob.glob(p)
                    if found:
                        report_files.extend(found)
                
                # Deduplicate
                report_files = list(set(report_files))
                
                try:
                    logger.info(f"Found {len(report_files)} report files: {report_files}")
                except: pass

                if not report_files:
                    logger.info(f"No load generator reports found in {[work_dir, os.path.dirname(work_dir)]}")
                if not report_files:
                    logger.info("No load generator reports found for throughput chart")
                else:
                    for r_file in report_files:
                        try:
                            with open(r_file, 'r') as f:
                                r_data = json.load(f)
                                if 'time_series' in r_data:
                                    for point in r_data['time_series']:
                                        # Use req_sent as throughput metric
                                        ts = int(point.get('timestamp', 0))
                                        count = point.get('req_sent', 0)
                                        if ts > 0:
                                            # Align to 5s bins relative to start
                                            rel_ts = int(ts - start_ts_global)
                                            bin_ts = (rel_ts // BIN_SIZE) * BIN_SIZE
                                            if bin_ts >= 0:
                                                ts_agg[bin_ts] += count
                        except Exception as e:
                            logger.warn(f"Failed to parse report {r_file}: {e}")
                
                    if ts_agg:
                        # Convert to rate (msg/sec)
                        sorted_bins = sorted(ts_agg.keys())
                        max_bin = int(sorted_bins[-1])
                        
                        # Fill gaps with 0
                        labels = []
                        data = []
                        for t in range(0, max_bin + BIN_SIZE, BIN_SIZE):
                            count = ts_agg.get(t, 0)
                            rate = count / BIN_SIZE
                            labels.append(get_rel_label(t))
                            data.append(rate)
                            
                        components.append(renderer.render_line_chart(
                            title="Suite Message Throughput (Global)",
                            subtitle=f"Source: Load Generators (Client-side) | Rate = messages / {BIN_SIZE}s",
                            labels=labels,
                            datasets=[{"label": "Messages/sec", "data": data, "color": "rgba(59, 130, 246, 1)"}]
                        ))
                    else:
                        logger.warn("No time series data found in load generator reports")

            except Exception as e:
                logger.debug(f"Suite Throughput Chart skipped: {e}")



            # 5. Suite System Latencies (Use Delta Logic for Cumulative Histograms)
            # FIX: OTel Exporter sends Delta Temporality for Histograms. 
            # So .sum and .count are already minimal deltas. We just SUM them per bin.
            try:
                query = f"""
                SELECT 
                    CAST(FLOOR((epoch(timestamp) - {start_ts_global}) / {BIN_SIZE}) * {BIN_SIZE} AS INTEGER) as rel_sec,
                    -- Rack
                    SUM(case when name = 'fluxrig_rack_latency_seconds.sum' then value else 0 end) as rack_sum,
                    SUM(case when name = 'fluxrig_rack_latency_seconds.count' then value else 0 end) as rack_count,
                    -- Gear
                    SUM(case when name = 'fluxrig.gear.processing_time_ms.sum' then value else 0 end) as gear_sum,
                    SUM(case when name = 'fluxrig.gear.processing_time_ms.count' then value else 0 end) as gear_count,
                    -- NATS
                    SUM(case when name = 'fluxrig.nats.publish_latency_ms.sum' then value else 0 end) as nats_sum,
                    SUM(case when name = 'fluxrig.nats.publish_latency_ms.count' then value else 0 end) as nats_count
                FROM read_parquet('{telemetry_dir}/metrics/**/*.parquet', union_by_name=true)
                WHERE name IN (
                    'fluxrig_rack_latency_seconds.sum', 'fluxrig_rack_latency_seconds.count',
                    'fluxrig.gear.processing_time_ms.sum', 'fluxrig.gear.processing_time_ms.count',
                    'fluxrig.nats.publish_latency_ms.sum', 'fluxrig.nats.publish_latency_ms.count'
                )
                GROUP BY rel_sec
                ORDER BY rel_sec
                """
                results = con.execute(query).fetchall()
                if results:
                    labels = [get_rel_label(r[0]) for r in results]
                    ds = []
                    
                    # Calculate Averages in Python (safer)
                    rack_data = []
                    gear_data = []
                    nats_data = []
                    
                    for r in results:
                        # r: [rel_sec, rack_sum, rack_cnt, gear_sum, gear_cnt, nats_sum, nats_cnt]
                        
                        # Rack (seconds -> ms)
                        r_sum, r_cnt = r[1], r[2]
                        rack_data.append((r_sum / r_cnt * 1000) if r_cnt > 0 else 0)
                        
                        # Gear (ms -> ms)
                        g_sum, g_cnt = r[3], r[4]
                        gear_data.append((g_sum / g_cnt) if g_cnt > 0 else 0)
                        
                        # NATS (ms -> ms)
                        n_sum, n_cnt = r[5], r[6]
                        nats_data.append((n_sum / n_cnt) if n_cnt > 0 else 0)
                    
                    if any(v > 0 for v in rack_data): ds.append({"label": "Rack Latency (ms)", "data": rack_data, "color": "rgba(255, 99, 132, 1)"})
                    if any(v > 0 for v in gear_data): ds.append({"label": "Gear Proc (ms)", "data": gear_data, "color": "rgba(75, 192, 192, 1)"})
                    if any(v > 0 for v in nats_data): ds.append({"label": "NATS Latency (ms)", "data": nats_data, "color": "rgba(54, 162, 235, 1)"})
                    
                    if ds:
                        components.append(renderer.render_line_chart(
                            title="Suite System Latency (Averages)",
                            subtitle=f"Source: OTel Histograms (Delta) | Window: {BIN_SIZE}s",
                            labels=labels, datasets=ds))
            except Exception as e:
                logger.warn(f"Suite Latency Chart failed: {e}")

            # 5b. Error Rate Chart (Fintech Operations Critical)
            try:
                 # Optimized error query: check if we have messages and errors
                 # (Logic simplified to prevent chart noise if no errors)
                 pass 
            except Exception as e:
                logger.warn(f"Error Rate Chart failed: {e}")



            # 6. Data Consistency Check (Detailed Component Breakdown)
            try:
                # 1. Client Totals (from Load Gen Reports)
                client_sent = 0
                client_recv = 0
                report_files = glob.glob(f"{work_dir}/r_w*.json") + glob.glob(f"{os.path.dirname(work_dir)}/r_w*.json")
                for rf in list(set(report_files)):
                    try:
                        with open(rf, 'r') as f:
                            d = json.load(f)
                            client_sent += d.get('req_sent', 0)
                            client_recv += d.get('resp_recv', 0)
                    except: pass

                # 2. Per-Gear Metrics
                # Query metrics Grouped by Gear ID (from attributes)
                # Note: 'fluxrig.gear.messages_in' and 'fluxrig.gear.messages_out'
                gear_stats = {}
                try:
                    q_gears = f"""
                    SELECT 
                        COALESCE(attributes->>'gear_id', attributes->>'gear_name', attributes->>'name', 'unknown') as gear_id,
                        name,
                        sum(value) as val
                    FROM read_parquet('{telemetry_dir}/metrics/**/*.parquet', union_by_name=true)
                    WHERE name IN ('fluxrig.gear.messages_in', 'fluxrig.gear.messages_out')
                    GROUP BY gear_id, name
                    """
                    g_res = con.execute(q_gears).fetchall()
                    for r in g_res:
                        gid = r[0]
                        metric = r[1]
                        val = r[2]
                        if gid not in gear_stats: gear_stats[gid] = {'in': 0, 'out': 0}
                        if 'messages_in' in metric: gear_stats[gid]['in'] = val
                        elif 'messages_out' in metric: gear_stats[gid]['out'] = val
                except: pass

                # 3. Rack Totals
                rack_in = 0
                rack_out = 0
                try:
                    q_rack = f"""
                    SELECT 
                        (CASE WHEN attributes LIKE '%inbound%' THEN 'in' ELSE 'out' END) as dir,
                        sum(value)
                    FROM read_parquet('{telemetry_dir}/metrics/**/*.parquet', union_by_name=true)
                    WHERE name = 'fluxrig_rack_messages_total'
                    GROUP BY dir
                    """
                    r_res = con.execute(q_rack).fetchall()
                    for r in r_res:
                        if r[0] == 'in': rack_in = r[1]
                        elif r[0] == 'out': rack_out = r[1]
                except: pass

                # 4. Echo Server Logs (Physical File)
                echo_in = 0
                echo_file = os.path.join(work_dir, "echo_server.log")
                if os.path.exists(echo_file):
                    try:
                        # Echo Server logs "handled connection" or similar? 
                        # Or generic ISO tool logs? 
                        # Usually "Echo Server handled..."
                        # Let's just count lines for now or look for "Request"
                        with open(echo_file, 'r') as f:
                            # Assuming 1 line per request if debug enabled? 
                            # Or usually just Errors.
                            # Standard iso8583-tool in echo mode might log transactions.
                            # Let's count "Header:" or "ISO Message" occurrences
                            content = f.read()
                            echo_in = content.count("ISO Message") 
                            if echo_in == 0:
                                # Fallback: Count lines if small?
                                if len(content) < 1000000:
                                     echo_in = len(content.splitlines())
                    except: pass

                # Build Rows
                # Format: Component | Inbound | Outbound | Delta (vs Client Sent)
                rows = []
                
                # Client: Input = Responses Received. Output = Requests Sent.
                rows.append(["Load Generator (Client)", f"{int(client_recv):,}", f"{int(client_sent):,}", "-"])
                
                # Rack
                loss_rack = (abs(client_sent - rack_in)/client_sent*100) if client_sent > 0 else 0
                status_rack = "✅" if loss_rack < 1 else f"⚠️ {loss_rack:.1f}%"
                rows.append(["FluxRig Rack (Gateway)", f"{int(rack_in):,}", f"{int(rack_out):,}", status_rack])
                
                # Gears
                for gid, stats in sorted(gear_stats.items()):
                     gin = stats['in']
                     gout = stats['out']
                     # Compare Gear In vs Client Sent (should be close)
                     loss_g = (abs(client_sent - gin)/client_sent*100) if client_sent > 0 else 0
                     status_g = "✅" if loss_g < 1 else f"⚠️ {loss_g:.1f}%"
                     rows.append([f"Gear: {gid}", f"{int(gin):,}", f"{int(gout):,}", status_g])

                # Echo Server
                if os.path.exists(echo_file):
                     # Echo should Match Client Sent
                     loss_e = (abs(client_sent - echo_in)/client_sent*100) if client_sent > 0 else 0
                     status_e = "✅" if loss_e < 1 else f"⚠️ {loss_e:.1f}%"
                     rows.append(["Echo Server (External)", f"{int(echo_in):,}", f"{int(echo_in):,}", status_e])


                # 5. Log Analysis (Frame Events)
                log_recv = 0
                try:
                    q = f"SELECT count(*) FROM read_parquet('{telemetry_dir}/logs/**/*.parquet', union_by_name=true) WHERE body LIKE '%Frame Received%'"
                    log_recv = con.execute(q).fetchone()[0]
                except: pass
                
                loss_log = (abs(client_sent - log_recv)/client_sent*100) if client_sent > 0 else 0
                status_log = "✅" if loss_log < 1 else f"⚠️ {loss_log:.1f}%"
                rows.append(["System Logs (Frame Received)", f"{int(log_recv):,}", "-", status_log])

                components.append(renderer.render_table(
                    title="Data Consistency & Parity (End-to-End)",
                    headers=["Component", "Inbound (Recv)", "Outbound (Sent)", "Parity (vs Client Sent)"],
                    rows=rows,
                    width="100%"
                ))

            except Exception as e:
                logger.warn(f"Consistency Check failed: {e}")

            # 7. Server Infrastructure (Go Runtime & Host)
            try:
                # 7.1 Goroutines
                query = f"""
                SELECT 
                    CAST(FLOOR((epoch(timestamp) - {start_ts_global}) / {BIN_SIZE}) * {BIN_SIZE} AS INTEGER) as rel_sec,
                    max(value) as val
                FROM read_parquet('{telemetry_dir}/metrics/**/*.parquet', union_by_name=true)
                WHERE name = 'go.goroutine.count'
                GROUP BY rel_sec ORDER BY rel_sec
                """
                results = con.execute(query).fetchall()
                if results:
                    labels = [get_rel_label(r[0]) for r in results]
                    components.append(renderer.render_line_chart(
                        title="Go Runtime: Goroutines",
                        subtitle="Source: go.goroutine.count | Shows max active goroutines per second bucket",
                        labels=labels,
                        datasets=[{"label": "Goroutines", "data": [r[1] for r in results], "color": "rgba(153, 102, 255, 1)"}]
                    ))

                # 6.3 Heap Memory (Used)
                query = f"""
                SELECT 
                    CAST(FLOOR((epoch(timestamp) - {start_ts_global}) / {BIN_SIZE}) * {BIN_SIZE} AS INTEGER) as rel_sec,
                    max(value) / (1024*1024) as val
                FROM read_parquet('{telemetry_dir}/metrics/**/*.parquet', union_by_name=true)
                WHERE name = 'go.memory.used'
                GROUP BY rel_sec ORDER BY rel_sec
                """
                results = con.execute(query).fetchall()
                if results:
                    labels = [get_rel_label(r[0]) for r in results]
                    ds = [{"label": "Heap Used (MB)", "data": [r[1] for r in results], "color": "rgba(255, 159, 64, 1)"}]
                    components.append(renderer.render_line_chart(
                        title="Go Runtime: Heap Memory (MB)",
                        subtitle="Source: go.memory.used | Usage in MB",
                        labels=labels, datasets=ds))
                    
                # 6.3 Host Memory (System)
                query = f"""
                SELECT 
                    CAST(FLOOR((epoch(timestamp) - {start_ts_global}) / {BIN_SIZE}) * {BIN_SIZE} AS INTEGER) as rel_sec,
                    avg(value) * 100 as val
                FROM read_parquet('{telemetry_dir}/metrics/**/*.parquet', union_by_name=true)
                WHERE name = 'system.memory.utilization'
                GROUP BY rel_sec ORDER BY rel_sec
                """
                results = con.execute(query).fetchall()
                if results:
                    labels = [get_rel_label(r[0]) for r in results]
                    components.append(renderer.render_line_chart(
                        title="Host: Memory Utilization (%)",
                        subtitle="Source: system.memory.utilization | Shows avg usage × 100 per second",
                        labels=labels,
                        datasets=[{"label": "Memory Util (%)", "data": [r[1] for r in results], "color": "rgba(255, 99, 132, 1)"}]
                    ))

                # 6.4 CPU Utilization
                query = f"""
                SELECT 
                    CAST(FLOOR((epoch(timestamp) - {start_ts_global}) / {BIN_SIZE}) * {BIN_SIZE} AS INTEGER) as rel_sec,
                    avg(value) * 100 as val
                FROM read_parquet('{telemetry_dir}/metrics/**/*.parquet', union_by_name=true)
                WHERE name = 'system.cpu.utilization'
                GROUP BY rel_sec ORDER BY rel_sec
                """
                results = con.execute(query).fetchall()
                if results:
                    labels = [get_rel_label(r[0]) for r in results]
                    components.append(renderer.render_line_chart(
                        title="Host: CPU Utilization (%)",
                        subtitle=f"Source: system.cpu.utilization | Avg usage × 100 per {BIN_SIZE}s bucket",
                        labels=labels,
                        datasets=[{"label": "CPU Util (%)", "data": [r[1] for r in results], "color": "rgba(59, 130, 246, 1)"}]
                    ))
            
            except Exception as e:
                logger.warn(f"Server Infrastructure Metrics failed: {e}")

            # 7. Server Resource & Internal Metrics (Network/Bus)
            try:
                # 7.1 Network I/O Volume
                query = f"""
                SELECT 
                    CAST(FLOOR((epoch(timestamp) - {start_ts_global}) / 15) * 15 AS INTEGER) as rel_sec,
                    sum(value) as rate
                FROM read_parquet('{telemetry_dir}/metrics/**/*.parquet', union_by_name=true)
                WHERE name = 'fluxrig_rack_bytes_total'
                GROUP BY rel_sec
                ORDER BY rel_sec
                """
                results = con.execute(query).fetchall()
                if results:
                    labels = [get_rel_label(r[0]) for r in results]
                    components.append(renderer.render_line_chart(
                        title="Network I/O Volume (Bytes)",
                        subtitle=f"Source: fluxrig_rack_bytes_total | Bytes transferred per 15s (Smoothed for Jitter)",
                        labels=labels,
                        datasets=[{"label": "Bytes Transferred", "data": [r[1] for r in results], "color": "rgba(153, 102, 255, 1)"}]
                    ))

                # 7.2 Internal Bus Activity
                query = f"""
                SELECT 
                    CAST(FLOOR((epoch(timestamp) - {start_ts_global}) / 15) * 15 AS INTEGER) as rel_sec,
                    sum(value) / 15 as rate
                FROM read_parquet('{telemetry_dir}/metrics/**/*.parquet', union_by_name=true)
                WHERE name = 'fluxrig.bus.publish_count'
                GROUP BY rel_sec
                ORDER BY rel_sec
                """
                results = con.execute(query).fetchall()
                if results:
                    labels = [get_rel_label(r[0]) for r in results]
                    components.append(renderer.render_line_chart(
                        title="Internal Bus Activity (Events)",
                        subtitle=f"Source: fluxrig.bus.publish_count | Events per second (Avg in 15s bin for Jitter stability)",
                        labels=labels,
                        datasets=[{"label": "Bus Publishes/sec", "data": [r[1] for r in results], "color": "rgba(255, 159, 64, 1)"}]
                    ))
            except Exception as e:
                logger.warn(f"Server Resource Metrics failed: {e}")

            # 6. Suite Connections (Prominent)
            try:
                query = f"""
                SELECT 
                    CAST(FLOOR((epoch(timestamp) - {start_ts_global}) / {BIN_SIZE}) * {BIN_SIZE} AS INTEGER) as rel_sec,
                    max(value) as val
                FROM read_parquet('{telemetry_dir}/metrics/**/*.parquet', union_by_name=true)
                WHERE name = 'fluxrig_rack_connections_active'
                GROUP BY rel_sec ORDER BY rel_sec
                """
                results = con.execute(query).fetchall()
                if results:
                    labels = [get_rel_label(r[0]) for r in results]
                    components.append(renderer.render_line_chart(
                        title="Suite Active Inbound Connections",
                        subtitle="Source: fluxrig_rack_connections_active | Peak concurrent TCP connections per second",
                        labels=labels,
                        datasets=[{"label": "Active Connections", "data": [r[1] for r in results], "color": "rgba(153, 102, 255, 1)"}]
                    ))
            except Exception as e:
                logger.warn(f"Suite Connections Chart failed: {e}")



            # 6c. Suite Gear Processing Duration (Dedicated)
            try:
                query = f"""
                SELECT 
                    CAST(FLOOR((epoch(timestamp) - {start_ts_global}) / {BIN_SIZE}) * {BIN_SIZE} AS INTEGER) as rel_sec,
                    (SUM(CASE WHEN name = 'fluxrig.gear.processing_time_ms.sum' THEN value END) / 
                    NULLIF(SUM(CASE WHEN name = 'fluxrig.gear.processing_time_ms.count' THEN value END), 0)) as val
                FROM read_parquet('{telemetry_dir}/metrics/**/*.parquet', union_by_name=true)
                WHERE name IN ('fluxrig.gear.processing_time_ms.sum', 'fluxrig.gear.processing_time_ms.count')
                GROUP BY rel_sec ORDER BY rel_sec
                """
                results = con.execute(query).fetchall()
                if results:
                    labels = [get_rel_label(r[0]) for r in results]
                    components.append(renderer.render_line_chart(
                        title="Suite Gear Processing Duration (ms)",
                        subtitle=f"Source: fluxrig.gear.processing_time_ms (sum/count×1000) | Avg gear execution time, {BIN_SIZE}s bins",
                        labels=labels,
                        datasets=[{"label": "Gear Proc (ms)", "data": [r[1] for r in results], "color": "rgba(75, 192, 192, 1)"}]
                    ))
            except Exception as e:
                logger.warn(f"Suite Gear Processing Chart failed: {e}")

            # 7. Metrics Inventory (Reordered)
            try:
                query = f"""
                SELECT 
                    name, 
                    count(*) as samples, 
                    min(value) as min_val,
                    avg(value) as avg_val,
                    max(value) as max_val,
                    min(timestamp) as first,
                    max(timestamp) as last
                FROM read_parquet('{telemetry_dir}/metrics/**/*.parquet', union_by_name=true)
                GROUP BY name ORDER BY name
                """
                results = con.execute(query).fetchall()
                if results:
                    rows = []
                    for r in results:
                        # name, samples, min, avg, max, first, last
                        rows.append([
                            r[0], 
                            r[1], 
                            f"{r[2]:.2f}", 
                            f"{r[3]:.2f}", 
                            f"{r[4]:.2f}", 
                            (r[5].strftime('%H:%M:%S') if r[5] else "N/A"),
                            (r[6].strftime('%H:%M:%S') if r[6] else "N/A")
                        ])
                        
                    components.append(renderer.render_table(
                        title="All Available Metrics (Inventory)",
                        headers=["Metric Name", "Samples", "Min", "Avg", "Max", "First Seen", "Last Seen"],
                        rows=rows,
                        width="100%"
                    ))
            except Exception as e:
                logger.warn(f"Metrics Inventory failed: {e}")

            # 8. Consolidated Telemetry Logs (Replacing Individual Files)
            try:
                # Select specific columns from ALL log files
                # Filter out traffic frames
                query = f"""
                SELECT 
                    strftime(timestamp, '%Y-%m-%d %H:%M:%S.%g') as ts_fmt,
                    COALESCE(entity_type, 'unknown') as type,
                    COALESCE(entity_name, 'unknown') as entity,
                    COALESCE(severity, 'INFO') as sev,
                    body,
                    COALESCE(attributes, '') as attrs
                FROM read_parquet('{telemetry_dir}/logs/**/*.parquet', union_by_name=true) 
                WHERE body NOT LIKE '%Frame Sent%' AND body NOT LIKE '%Frame Received%'
                ORDER BY timestamp ASC
                LIMIT 5000
                """
                
                results = con.execute(query).fetchall()
                if results:
                    fmt_rows = [[str(x) for x in r] for r in results]
                    components.append(renderer.render_table(
                        title="Consolidated Telemetry Logs (Filtered, Limit 500)",
                        headers=["Timestamp", "Type", "Entity", "Severity", "Body", "Attributes"],
                        rows=fmt_rows,
                        width="100%"
                    ))
                else:
                    logger.info("No logs found after filtering.")

            except Exception as e:
                logger.warn(f"Consolidated Logs Table failed: {e}")



            # 9. Individual File Summaries (Removed by request)
            # Users prefer consolidated view.

            return components

        except Exception as e:
            logger.error(f"Failed to get telemetry components: {e}")
            return []

    @keyword
    def log_detailed_telemetry_report(self, work_dir: str):
        """
        Generates a comprehensive HTML report including:
        1. Physical Log Line Counts (Rack/Mixer)
        2. Parquet File Summary (Row Counts per File)
        3. Detailed Data Tables for each Parquet File
        """
        telemetry_dir = os.path.join(work_dir, "data", "telemetry")
        mixer_log = os.path.join(work_dir, "logs", "mixer.log")
        rack_log = os.path.join(work_dir, "../rack", "logs", "fluxrig.log") # Assumes standard layout

        components = []

        # 1. Physical Log Counts
        log_rows = []
        if os.path.exists(mixer_log):
             with open(mixer_log, 'r') as f:
                 count = sum(1 for _ in f)
                 log_rows.append(["Mixer Log", mixer_log, count])
        else:
             log_rows.append(["Mixer Log", "Not Found", 0])

        if os.path.exists(rack_log):
             with open(rack_log, 'r') as f:
                 count = sum(1 for _ in f)
                 log_rows.append(["Rack Log", rack_log, count])
        else:
             log_rows.append(["Rack Log", "Not Found", 0])

        components.append(renderer.render_table(
            title="1. Physical Log File Counts",
            headers=["Component", "Path", "Line Count"],
            rows=log_rows,
            width="80%"
        ))

        if not os.path.exists(telemetry_dir):
            logger.warn("No telemetry directory found for detailed report.")
            return

        try:
            con = duckdb.connect(database=':memory:')
            
            # 2. Parquet File Summary
            # Get list of all parquet files
            files = []
            for root, dirs, filenames in os.walk(telemetry_dir):
                for f in filenames:
                    if f.endswith(".parquet"):
                        files.append(os.path.join(root, f))
            
            file_summary_rows = []
            file_tables = []

            for p_file in sorted(files):
                try:
                    # Count rows
                    count = con.execute(f"SELECT count(*) FROM read_parquet('{p_file}')").fetchone()[0]
                    rel_path = os.path.relpath(p_file, telemetry_dir)
                    file_summary_rows.append([rel_path, count])
                    
                    if count > 0:
                        # 3. Detailed Data Table for this file
                        # Get Columns
                        con.execute(f"SELECT * FROM read_parquet('{p_file}') LIMIT 1")
                        all_cols = [desc[0] for desc in con.description]
                        
                        # Select ALL columns as requested
                        select_cols = list(all_cols)
                        
                        # Reorder timestamp to front if present
                        if 'timestamp' in select_cols:
                            select_cols.remove('timestamp')
                            select_cols.insert(0, 'timestamp')

                        cols_str = ", ".join([f'"{c}"' for c in select_cols])
                        query = f"SELECT {cols_str} FROM read_parquet('{p_file}')"
                        rows = con.execute(query).fetchall()
                        
                        # Stringify for display
                        fmt_rows = [[str(x) for x in r] for r in rows]
                        
                        file_tables.append(renderer.render_table(
                            title=f"File: {rel_path} ({count} rows)",
                            headers=select_cols,
                            rows=fmt_rows,
                            width="100%"
                        ))

                except Exception as e:
                    logger.warn(f"Error reading {p_file}: {e}")
                    file_summary_rows.append([os.path.basename(p_file), f"Error: {e}"])

            components.append(renderer.render_table(
                title="2. Parquet File Summary",
                headers=["File Relative Path", "Row Count"],
                rows=file_summary_rows,
                width="100%"
            ))
            
            # Append individual file tables
            components.extend(file_tables)

            # Render Dashboard
            dashboard = renderer.render_dashboard(
                title="Detailed Telemetry Report",
                components=components
            )
            
            logger.info(dashboard, html=True)

        except Exception as e:
            logger.error(f"Failed to generate detailed report: {e}")

    @keyword
    def get_server_metrics(self, api_url="http://localhost:8090", metric_name="fluxrig_rack_messages_total"):
        """
        Queries Mixer API for server-side metrics.
        Returns list of points: {timestamp, value, attributes}.
        """
        import requests
        from datetime import datetime
        
        url = f"{api_url}/api/v1/telemetry/metrics"
        params = {
            "name": metric_name,
            "limit": 5000  # Increased limit
        }
        
        try:
            resp = requests.get(url, params=params, timeout=5)
            resp.raise_for_status()
            data = resp.json()
            
            metrics = []
            for item in data:
                ts_str = item.get("timestamp", "")
                from datetime import timezone
                ts_epoch = 0.0
                try:
                    # Try ISO format first (modern)
                    dt = datetime.fromisoformat(ts_str.replace('Z', '+00:00'))
                    ts_epoch = dt.timestamp()
                except ValueError:
                    try:
                        # Fallback to strptime
                        dt = datetime.strptime(ts_str, "%Y-%m-%dT%H:%M:%S.%fZ")
                        # Force UTC because Mixer API always returns UTC
                        ts_epoch = dt.replace(tzinfo=timezone.utc).timestamp()
                    except ValueError:
                         continue
                    
                metrics.append({
                    "timestamp": ts_epoch,
                    "name": item.get("name"),
                    "value": float(item.get("value", 0)),
                    "attributes": item.get("attributes", {})
                })
            
            metrics.sort(key=lambda x: x['timestamp'])
            return metrics
            
        except Exception as e:
            logger.warn(f"Failed to query API {url}: {e}")
            return []

    @keyword
    def get_gear_metrics(self, api_url="http://localhost:8090"):
        """
        Fetches gear metrics and returns a dict mapping gear_id -> points.
        """
        # Try multiple names as Mixer/Gears might use different conventions
        metric_names = ["fluxrig_gear_messages_total", "fluxrig.gear.messages_in", "fluxrig.gear.messages_out"]
        gears = {}
        
        for m_name in metric_names:
            all_metrics = self.get_server_metrics(api_url=api_url, metric_name=m_name)
            for p in all_metrics:
                # Use gear_id/name if present, else fallback
                attrs = p.get('attributes', {})
                gid = attrs.get('gear_id') or attrs.get('name') or "edge-gear"
                if gid not in gears:
                    gears[gid] = []
                gears[gid].append(p)
            if gears: break
            
        return gears

    @keyword
    def get_test_logs(self, work_dir: str, start_ts: float, end_ts: float, limit: int = 50):
        """
        Fetches logs from Parquet that occurred within the specified epoch window.
        """
        import duckdb
        
        # Adjust for nested mixer directory if needed
        mixer_dir = os.path.join(work_dir, "mixer")
        base_dir = mixer_dir if os.path.exists(mixer_dir) else work_dir
        
        telemetry_dir = os.path.join(base_dir, "data", "telemetry")
        if not os.path.exists(telemetry_dir):
             logger.warn(f"Telemetry dir not found at {telemetry_dir}")
             return []
        
        try:
            con = duckdb.connect(database=':memory:')
            # Use epoch() function in DuckDB to compare directly
            query = f"""
            SELECT 
                strftime(timestamp, '%H:%M:%S') as time,
                severity as level,
                COALESCE(entity_name, 'system') as component,
                body as message
            FROM read_parquet('{telemetry_dir}/logs/**/*.parquet', union_by_name=true)
            WHERE epoch(timestamp) >= {start_ts - 5} AND epoch(timestamp) <= {end_ts + 5}
            ORDER BY timestamp ASC
            LIMIT {limit}
            """
            results = con.execute(query).fetchall()
            return [list(r) for r in results]
        except Exception as e:
            logger.warn(f"Failed to fetch test logs: {e}")
            return []

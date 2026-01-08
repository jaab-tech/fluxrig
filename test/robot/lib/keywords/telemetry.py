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

import os
import subprocess
import duckdb
from robot.api import logger
from robot.api.deco import keyword

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
        telemetry_dir = os.path.join(work_dir, "data", "telemetry")
        if not os.path.exists(telemetry_dir):
            logger.warn("No telemetry directory found.")
            return

        try:
            con = duckdb.connect(database=':memory:')
            
            # Query by entity_type
            query = f"""
            SELECT 
                entity_type, 
                count(*) as event_count 
            FROM read_parquet('{telemetry_dir}/**/*.parquet') 
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
            FROM read_parquet('{telemetry_dir}/**/*.parquet') 
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
                logger.warn("No MIXER logs found in Parquet")
            if parquet_counts.get('RACK', 0) == 0:
                logger.warn("No RACK logs found in Parquet")
            if parquet_counts.get('GEAR', 0) == 0:
                logger.warn("No GEAR logs found in Parquet")
                
            total_parquet = sum(parquet_counts.values())
            logger.info(f"Total Parquet logs: {total_parquet}")
            
            if total_parquet == 0:
                raise RuntimeError("No logs found in Parquet - telemetry pipeline may be broken")
                
        except Exception as e:
            logger.error(f"Failed to verify log parity: {e}")
            raise

# Copyright 2025 JAAB Tech SAS, Uruguay
import os
import sys
import json
import re
from datetime import datetime
from robot.api.deco import library, keyword
from robot.api import logger
from robot.libraries.BuiltIn import BuiltIn

import warnings
try:
    from urllib3.exceptions import NotOpenSSLWarning
    warnings.filterwarnings("ignore", category=NotOpenSSLWarning)
except ImportError:
    pass

# Ensure local lib/keywords can be imported
current_dir = os.path.dirname(os.path.abspath(__file__))
if current_dir not in sys.path:
    sys.path.append(current_dir)

from keywords.process import ProcessKeywords
from keywords.api import ApiKeywords
from keywords.telemetry import TelemetryKeywords
from reporting.renderer import ReportRenderer

@library
class FluxRigLibrary(ProcessKeywords, ApiKeywords, TelemetryKeywords):
    """
    FluxRig Process Management Library.
    Composition of Process, API, and Telemetry keywords.
    Also includes Reporting/Verification utilities directly.
    """
    
    ROBOT_LISTENER_API_VERSION = 3
    ROBOT_LIBRARY_SCOPE = 'TEST SUITE'

    def __init__(self):
        self.processes = {}
        self.root_dir = self._find_root()
        self._renderer = ReportRenderer()
        self.suite_results = [] # Collects {name, data, server_chart, telemetry_components}
        self.suite_logs = []
        
    def _find_root(self):
        current = os.path.dirname(os.path.abspath(__file__))
        return os.path.abspath(os.path.join(current, "../../../"))

    # --- Verification Keywords ---
    
    @keyword
    def check_log_for_errors(self, log_path, ignore_patterns=None):
        """
        Scans a log file for logical errors (ERROR, FATAL, PANIC) and Warnings.
        Fails if found.
        """
        if not os.path.exists(log_path):
            logger.warn(f"Log file not found: {log_path}")
            return

        with open(log_path, 'r', encoding='utf-8', errors='replace') as f:
            lines = f.readlines()
            
        params = ignore_patterns if ignore_patterns else []
        ignored = [re.compile(p) for p in params]
        
        errors = []
        warnings = []
        
        for line in lines:
            line_lower = line.lower()
            
            is_err = "level=error" in line_lower or "level=fatal" in line_lower or "panic:" in line_lower
            is_warn = "level=warn" in line_lower
            
            if not (is_err or is_warn):
                continue

            skip = False
            for p in ignored:
                if p.search(line):
                    skip = True
                    break
            if skip:
                continue
                
            if is_err:
                errors.append(line.strip())
            elif is_warn:
                warnings.append(line.strip())
        
        if warnings:
            logger.info("Found Warnings:\n" + "\n".join(warnings))
            
        if errors:
            msg = f"Found {len(errors)} ERRORs in {log_path}:\n" + "\n".join(errors[:10])
            if len(errors) > 10: msg += "\n...and more"
            raise AssertionError(msg)

    @keyword
    def record_multi_worker_result(self, worker_reports: list, name="Staged Load", description=None):
        """
        Aggregates multiple worker JSON reports into a stacked bar chart.
        
        Args:
            worker_reports: List of dicts with keys: path, label, color
            name: Chart title prefix
            description: Optional HTML description
        """
        from collections import defaultdict
        
        # Merge all time-series onto a unified timeline
        all_data = []
        global_start = float('inf')
        global_end = float('-inf')
        
        for wr in worker_reports:
            path = wr['path']
            if not os.path.exists(path):
                logger.warn(f"Worker report not found: {path}")
                continue
            with open(path, 'r') as f:
                data = json.load(f)
            ts = data.get('time_series', [])
            if not ts:
                continue
            ts.sort(key=lambda x: x['timestamp'])
            if len(ts) > 5:
                ts = ts[:-1]  # Filter trailing point
            for p in ts:
                global_start = min(global_start, p['timestamp'])
                global_end = max(global_end, p['timestamp'])
            all_data.append({'label': wr['label'], 'color': wr['color'], 'series': ts})
        
        if not all_data or global_start == float('inf'):
            logger.warn("No valid worker data for aggregation")
            return
        
        # Create unified time buckets (1 second resolution)
        duration = int(global_end - global_start) + 1
        labels = [f"{i // 60:02d}:{i % 60:02d}" for i in range(duration)]
        
        # Build datasets with values aligned to buckets
        datasets = []
        for wd in all_data:
            bucket_values = [0] * duration
            for pt in wd['series']:
                idx = int(pt['timestamp'] - global_start)
                if 0 <= idx < duration:
                    bucket_values[idx] = pt.get('req_sent', 0)
            datasets.append({
                'label': wd['label'],
                'data': bucket_values,
                'color': wd['color']
            })
        
        # Render stacked chart
        chart = self._renderer.render_stacked_bar_chart(
            title=f"{name}: Client Throughput (All Workers)",
            subtitle="Source: Load generator JSON reports | Shows requests/sec from each worker, stacked by time bucket",
            labels=labels,
            datasets=datasets
        )
        
        if description:
            self.suite_results.append(description)
        self.suite_results.append(chart)

    @keyword
    def record_performance_result(self, json_path, name="Test", description=None, server_api_url="http://localhost:8090", work_dir=None):
        """
        Processes a performance JSON and stores it for the suite summary.
        """
        if not os.path.exists(json_path):
            logger.warn(f"JSON Report not found: {json_path}")
            return

        with open(json_path, 'r') as f:
            data = json.load(f)
            
        time_series = data.get('time_series', [])
        if not time_series:
            logger.warn(f"No time series data in {name}")
            return
            
        time_series.sort(key=lambda x: x['timestamp'])
        if len(time_series) > 5:
            time_series = time_series[:-1] # Filter last trailing point
        
        start_ts = time_series[0]['timestamp']
        end_ts = time_series[-1]['timestamp']
        # Use relative time labels (MM:SS from start)
        def fmt_rel(ts):
            rel = int(ts - start_ts)
            if rel < 0: rel = 0
            return f"{rel // 60:02d}:{rel % 60:02d}"
        timestamps = [fmt_rel(x['timestamp']) for x in time_series]
        
        # 1. Client Charts
        chart_tps = self._renderer.render_bar_chart(
            title=f"{name}: Client Throughput",
            subtitle="Source: iso8583-tool JSON report | Client-side requests sent per second",
            labels=timestamps,
            data=[x['req_sent'] for x in time_series],
            dataset_label="Req/sec",
            color="rgba(75, 192, 192, 0.6)"
        )
        
        latency_p50 = [x.get('latency_p50_ms', 0) for x in time_series]
        latency_p99 = [x.get('latency_p99_ms', 0) for x in time_series]
        latency_max = [x.get('latency_max_ms', 0) for x in time_series]
        
        chart_lat = self._renderer.render_line_chart(
             title=f"{name}: Client Latency (ms)",
             subtitle="Source: iso8583-tool JSON report | Client-measured round-trip time percentiles (P50, P99, Max)",
             labels=timestamps,
             datasets=[
                 {"label": "P50", "data": latency_p50, "color": "rgba(75, 192, 192, 1)"},
                 {"label": "P99", "data": latency_p99, "color": "rgba(255, 99, 132, 1)"},
                 {"label": "Max", "data": latency_max, "color": "rgba(255, 206, 86, 1)"}
             ]
        )
        
        # 2. Server/Gear Charts
        server_chart_html = ""
        server_lat_chart = ""
        test_logs_html = ""
        rack_lat_pts = []
        gear_proc_pts = []
        
        if server_api_url:
            try:
                # Try multiple metric names as fallbacks
                metric_names = ["fluxrig_rack_messages_total", "fluxrig.nats.messages_published", "fluxrig.gear.messages_in"]
                rack_raw = []
                for m_name in metric_names:
                    rack_raw = self.get_server_metrics(api_url=server_api_url, metric_name=m_name)
                    if rack_raw: break
                
                gear_raw_map = self.get_gear_metrics(api_url=server_api_url)
                
                # Filter by test window (+/- 60s for safety)
                window_start = start_ts - 60
                window_end = end_ts + 60
                
                rack_data = [p for p in rack_raw if window_start <= p['timestamp'] <= window_end]
                gear_map = {gid: [p for p in pts if window_start <= p['timestamp'] <= window_end] 
                            for gid, pts in gear_raw_map.items()}
                
                if not rack_data and rack_raw:
                    # Clock skew fallback: take points based on counts if window fails
                    rack_data = rack_raw[-10:]
                    logger.warn(f"No direct window match for rack metrics for {name}, using latest samples.")
                
                def get_rates(points):
                    if not points: return [], []
                    b = {}
                    for p in points:
                        ts = int(p['timestamp'])
                        val = float(p['value'])
                        # With Delta temporality, we just sum up deltas for the same bucket if they happen
                        b[ts] = b.get(ts, 0) + val
                    sts = sorted(b.keys())
                    rates, labs = [], []
                    for i in range(1, len(sts)):
                        dt = sts[i] - sts[i-1]
                        if dt > 0:
                            # dv is already a delta
                            dv = b[sts[i]]
                            rates.append(dv / dt)
                            labs.append(datetime.fromtimestamp(sts[i]).strftime('%H:%M:%S'))
                    return labs, rates

                rack_labels, rack_rates = get_rates(rack_data)
                datasets = []
                if rack_rates:
                    # Try to separate inbound/outbound if attributes allow
                    inbound_pts = [p for p in rack_data if p['attributes'].get('direction') == 'inbound']
                    outbound_pts = [p for p in rack_data if p['attributes'].get('direction') == 'outbound']
                    
                    if inbound_pts:
                         labs, rates = get_rates(inbound_pts)
                         datasets.append({"label": "Rack (Inbound)", "data": rates, "color": "rgba(153, 102, 255, 1)"})
                    if outbound_pts:
                         labs, rates = get_rates(outbound_pts)
                         datasets.append({"label": "Rack (Outbound)", "data": rates, "color": "rgba(255, 159, 64, 1)"})
                    
                    if not inbound_pts and not outbound_pts:
                         # Fallback to total
                         datasets.append({"label": "Rack (Total)", "data": rack_rates, "color": "rgba(153, 102, 255, 1)"})
                
                colors = ["rgba(54, 162, 235, 1)", "rgba(75, 192, 192, 1)", "rgba(255, 206, 86, 1)"]
                last_g_labs = []
                for i, (gid, gpoints) in enumerate(gear_map.items()):
                    glabs, grates = get_rates(gpoints)
                    if grates:
                        datasets.append({"label": f"Gear: {gid}", "data": grates, "color": colors[i % len(colors)]})
                        last_g_labs = glabs

                # 2.2 Server Latencies
                rack_lat_sum = self.get_server_metrics(api_url=server_api_url, metric_name="fluxrig_rack_latency_seconds.sum")
                rack_lat_count = self.get_server_metrics(api_url=server_api_url, metric_name="fluxrig_rack_latency_seconds.count")
                
                gear_proc_sum = self.get_server_metrics(api_url=server_api_url, metric_name="fluxrig.gear.processing_time_ms.sum")
                gear_proc_count = self.get_server_metrics(api_url=server_api_url, metric_name="fluxrig.gear.processing_time_ms.count")
                
                # Consolidate by timestamp
                def consolidate_averages(sums, counts, factor=1.0):
                    if not sums: return []
                    c_map = {int(p['timestamp']): p['value'] for p in counts}
                    results = []
                    for p in sums:
                        ts = int(p['timestamp'])
                        if ts in c_map and c_map[ts] > 0:
                            avg = (p['value'] * factor) / c_map[ts]
                            results.append({"timestamp": p['timestamp'], "value": avg})
                    return results

                rack_lat_pts = consolidate_averages(rack_lat_sum, rack_lat_count, factor=1000.0) # sec -> ms
                gear_proc_pts = consolidate_averages(gear_proc_sum, gear_proc_count)
                
                # Filter by window again (consolidate_averages already works on what we fetched)
                rack_lat_pts = [p for p in rack_lat_pts if window_start <= p['timestamp'] <= window_end]
                gear_proc_pts = [p for p in gear_proc_pts if window_start <= p['timestamp'] <= window_end]

                lat_datasets = []
                if rack_lat_pts:
                    lat_datasets.append({
                        "label": "Rack (Internal Latency ms)", 
                        "data": [p['value'] for p in rack_lat_pts], 
                        "color": "rgba(255, 99, 132, 1)"
                    })
                if gear_proc_pts:
                    lat_datasets.append({
                        "label": "Gear (Processing ms)", 
                        "data": [p['value'] for p in gear_proc_pts], 
                        "color": "rgba(75, 192, 192, 1)"
                    })
                
                server_lat_chart = ""
                if lat_datasets:
                    # Use unique timestamps for labels
                    lat_labels = [datetime.fromtimestamp(p['timestamp']).strftime('%H:%M:%S') for p in (rack_lat_pts if rack_lat_pts else gear_proc_pts)]
                    server_lat_chart = self._renderer.render_line_chart(
                        title=f"{name}: Server Internal Latency",
                        labels=lat_labels,
                        datasets=lat_datasets
                    )

                # 2.3 Combined Server Chart
                server_chart_html = ""
                if datasets:
                    server_chart_html = self._renderer.render_line_chart(
                        title=f"{name}: Server & Gear Throughput",
                        labels=rack_labels if rack_labels else last_g_labs,
                        datasets=datasets
                    )
                else:
                    logger.warn(f"No server/gear throughput data found in window for {name}")
                
                # 2.4 Test Case Logs
                test_logs_html = ""
                if work_dir:
                    logs = self.get_test_logs(work_dir, start_ts, end_ts)
                    if logs:
                        test_logs_html = self._renderer.render_table(
                            title="Server Logs during Test",
                            headers=["Time", "Level", "Component", "Message"],
                            rows=logs,
                            width="100%"
                        )

            except Exception as e:
                logger.warn(f"Failed to process server/gear metrics for {name}: {e}")
                server_lat_chart = ""
                test_logs_html = ""

        # 1. Summary Card
        import statistics
        
        server_lat_p99 = 0.0
        if rack_lat_pts:
            server_lat_p99 = statistics.quantiles([p['value'] for p in rack_lat_pts], n=100)[98] if len(rack_lat_pts) > 1 else rack_lat_pts[0]['value']
            
        gear_lat_p99 = 0.0
        if gear_proc_pts:
            gear_lat_p99 = statistics.quantiles([p['value'] for p in gear_proc_pts], n=100)[98] if len(gear_proc_pts) > 1 else gear_proc_pts[0]['value']
            
        summary_rows = [
            ["Duration", f"{data.get('duration_sec', 0):.2f}s"],
            ["Total Requests (Client)", data.get('req_sent', 0)],
            ["Avg Client TPS", f"{data.get('actual_tps', 0):.2f}"],
            ["Client Latency P99", f"{data.get('latency_p99_ms', 0):.2f}ms"],
            ["Server Internal P99", f"{server_lat_p99:.2f}ms"],
            ["Gear Processing P99", f"{gear_lat_p99:.2f}ms"]
        ]
        
        # Add server errors if any
        try:
             # Fetch errors for the window
             err_sum = self.get_server_metrics(api_url=server_api_url, metric_name="fluxrig.gear.errors.sum")
             total_err = sum(p['value'] for p in err_sum if window_start <= p['timestamp'] <= window_end)
             if total_err > 0:
                 summary_rows.append(["Server-Side Errors", int(total_err)])
        except: pass

        tbl_summary = self._renderer.render_table(title="Key Metrics", headers=["Metric", "Value"], rows=summary_rows, width="100%")

        # 2. Charts Row
        charts_row = f'''
        <div class="grid-row">
            <div class="chart-card">{chart_tps}</div>
            <div class="chart-card">{chart_lat}</div>
        </div>
        '''

        # 3. Server Row
        server_row = ""
        if server_chart_html or server_lat_chart:
            server_row = f'''
            <div class="grid-row">
                <div class="chart-card">{server_chart_html if server_chart_html else "No Throughput Data"}</div>
                <div class="chart-card">{server_lat_chart if server_lat_chart else "No Latency Data"}</div>
            </div>
            '''

        # Combine into a result block
        test_case_html = f'''
        <div class="test-case-section">
            <h2 class="test-case-title">Test Case: {name}</h2>
            {f'<div class="test-case-description">{description}</div>' if description else ''}
            
            <div class="grid-row">
                <div class="full-row">{tbl_summary}</div>
            </div>
            
            {charts_row}
            {server_row}
        </div>
        '''

        # 4. Store for final report
        self.suite_results.append(test_case_html)
        if test_logs_html:
            self.suite_logs.append(f'<div class="chart-card" style="margin-top: 24px;"><h3>Logs: {name}</h3>{test_logs_html}</div>')
        logger.info(f"Recorded results for {name}")

    @keyword
    def generate_suite_summary_report(self, output_path, work_dir=None):
        """
        Generates a final HTML report aggregating all recorded results plus Telemetry logs.
        """
        if not os.path.isabs(output_path):
             output_dir = BuiltIn().get_variable_value("${OUTPUT_DIR}", os.getcwd())
             output_path = os.path.join(output_dir, output_path)
        else:
             # If absolute, verify relpath base
             output_dir = BuiltIn().get_variable_value("${OUTPUT_DIR}", os.getcwd())

        logger.info(f"Generated Suite Summary Report: {output_path}")
        try:
            rel_path = os.path.relpath(output_path, output_dir)
        except ValueError:
            rel_path = output_path
        
        all_components = list(self.suite_results)

        # Add Global Telemetry Analysis
        if work_dir:
            try:
                mixer_work = os.path.join(work_dir, "mixer")
                if os.path.exists(mixer_work):
                    # No Subtitle
                    telem_components = self.get_detailed_telemetry_components(mixer_work)
                    
                    # Split charts and tables, and extract Timeline Header
                    t_charts = []
                    t_tables = []
                    timeline_header = None
                    
                    for comp in telem_components:
                        if "Test Execution Timeline" in comp:
                            timeline_header = comp
                        elif "canvas id=" in comp:
                            t_charts.append(comp)
                        else:
                            t_tables.append(comp)
                            
                    # Insert Timeline Header at the ABSOLUTE TOP if found
                    if timeline_header:
                        all_components.insert(0, timeline_header)
                            
                    # Render Charts Grid (All graphs together)
                    if t_charts:
                         # Append to existing components or start new grid logic
                         
                        current_row = []
                        for chart in t_charts:
                            current_row.append(f'<div class="chart-card">{chart}</div>')
                            if len(current_row) == 2:
                                all_components.append(f'<div class="grid-row">{"".join(current_row)}</div>')
                                current_row = []
                        if current_row:
                             all_components.append(f'<div class="grid-row">{"".join(current_row)}</div>')

                    # Metrics Tables
                    for tbl in t_tables:
                        all_components.append(f'<div class="chart-card" style="margin-top: 24px;">{tbl}</div>')

            except Exception as e:
                logger.warn(f"Failed to add global telemetry to summary: {e}")


        # Legacy logs removed - using Consolidated Telemetry Logs instead.

        dashboard = self._renderer.render_dashboard(
            title="Performance Report",
            components=all_components
        )
        
        with open(output_path, 'w') as f:
            f.write(dashboard)
            
        logger.info(f"Generated Suite Summary Report: {output_path}")
        link_html = f'''
        <div style="margin: 20px 0; padding: 24px; border: 2px solid #3b82f6; background-color: #eff6ff; border-radius: 12px; text-align: center;">
            <p style="margin: 0 0 16px 0; color: #1e40af; font-weight: 700; font-family: sans-serif; font-size: 18px;">Performance Insights Ready</p>
            <a href="{rel_path}" target="_blank" style="display: inline-block; background-color: #3b82f6; color: white; padding: 12px 32px; text-decoration: none; border-radius: 8px; font-weight: 700; font-family: sans-serif; box-shadow: 0 4px 6px -1px rgba(59, 130, 246, 0.5); transition: all 0.2s;">Open Performance Dashboard</a>
        </div>
        '''
        logger.info(link_html, html=True)

    @keyword
    def generate_performance_report(self, json_path, output_path, server_api_url="http://localhost:8090", description=None):
        """
        [DEPRECATED] use record_performance_result and generate_suite_summary_report instead.
        Still works for single-test calls.
        """
        self.record_performance_result(json_path, name="Single Run", description=description, server_api_url=server_api_url)
        self.generate_suite_summary_report(output_path)

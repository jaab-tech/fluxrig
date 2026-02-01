import os
import sys
import json
from robot.api import logger

# Ensure we can import from sibling/parent packages
current_dir = os.path.dirname(os.path.abspath(__file__))
lib_dir = os.path.dirname(current_dir)
if lib_dir not in sys.path:
    sys.path.append(lib_dir)

from reporting.renderer import ReportRenderer

class ReportingKeywords:
    
    def __init__(self):
        self._renderer = ReportRenderer()
        
    def generate_performance_report(self, json_path, output_path):
        """
        Generates an HTML report with graphs from the iso8583-tool JSON output.
        
        Args:
            json_path: Path to the r_*.json file.
            output_path: Path where to save the HTML report.
        """
        if not os.path.exists(json_path):
            logger.warn(f"JSON Report not found: {json_path}")
            return

        with open(json_path, 'r') as f:
            data = json.load(f)
            
        time_series = data.get('time_series', [])
        if not time_series:
            logger.warn("No time series data in report")
            return
            
        # Parse Series
        # Sort by timestamp
        time_series.sort(key=lambda x: x['timestamp'])
        
        timestamps = [str(x['timestamp']) for x in time_series]
        req_sent = [x['req_sent'] for x in time_series]
        resp_recv = [x['resp_recv'] for x in time_series]
        latency_p99 = [x.get('latency_p99_ms', 0) for x in time_series]
        
        # 1. Throughput Chart
        # Requires renderer to support multi-dataset bar chart or line chart?
        # Renderer has render_bar_chart. It takes 'data' list.
        # We might need to extend renderer for multi-dataset or just show one.
        # Let's show "Throughput (TPS)" as Req Sent.
        
        chart_tps = self._renderer.render_bar_chart(
            title="Throughput (Req Sent)",
            labels=timestamps,
            data=req_sent,
            dataset_label="Req Sent/sec",
            color="rgba(75, 192, 192, 0.6)"
        )
        
        # 2. Latency Chart
        chart_lat = self._renderer.render_bar_chart(
             title="Latency P99 (ms)",
             labels=timestamps,
             data=latency_p99,
             dataset_label="P99 Latency (ms)",
             color="rgba(255, 99, 132, 0.6)"
        )
        
        # 3. Summary Table
        headers = ["Metric", "Value"]
        rows = [
            ["Duration", f"{data.get('duration_sec', 0):.2f}s"],
            ["Total Requests", data.get('req_sent', 0)],
            ["Total Responses", data.get('resp_recv', 0)],
            ["Avg TPS", f"{data.get('actual_tps', 0):.2f}"],
            ["Latency P99", f"{data.get('latency_p99_ms', 0):.2f}ms"],
             ["Latency Max", f"{data.get('latency_max_ms', 0):.2f}ms"]
        ]
        
        tbl_summary = self._renderer.render_table(
            title="Summary Statistics",
            headers=headers,
            rows=rows,
            width="50%"
        )
        
        # Dashboard
        dashboard = self._renderer.render_dashboard(
            title="ISO8583 Performance Report",
            components=[tbl_summary, chart_tps, chart_lat]
        )
        
        with open(output_path, 'w') as f:
            f.write(dashboard)
            
        logger.info(f"Generated HTML Report: {output_path}")

# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

"""
ReportRenderer: Jinja2-based HTML rendering for Robot Framework reports.
"""
import os
import json
import time
from jinja2 import Environment, FileSystemLoader, select_autoescape

import uuid

class ReportRenderer:
    """
    Renders HTML components using Jinja2 templates.
    Templates are stored in ./templates/ relative to this file.
    """
    
    def __init__(self):
        template_dir = os.path.join(os.path.dirname(__file__), "templates")
        self.env = Environment(
            loader=FileSystemLoader(template_dir),
            autoescape=select_autoescape(['html', 'xml'])
        )
        # Add json filter for embedding data in JS
        self.env.filters['tojson'] = lambda x: json.dumps(x)
    
    def render_table(self, title: str, headers: list, rows: list, width: str = "100%") -> str:
        """
        Renders an HTML table.
        
        Args:
            title: Table title/heading
            headers: List of column header strings
            rows: List of row data (each row is a list/tuple of values)
            width: CSS width for the table
        
        Returns:
            Rendered HTML string
        """
        template = self.env.get_template("table.html.j2")
        table_id = f"tbl_{uuid.uuid4().hex[:8]}"
        return template.render(
            title=title,
            headers=headers,
            rows=rows,
            width=width,
            table_id=table_id
        )
    
    def render_bar_chart(self, title: str, labels: list, data: list, 
                         dataset_label: str = "Value",
                         color: str = "rgba(54, 162, 235, 0.6)",
                         subtitle: str = None) -> str:
        """
        Renders a Chart.js bar chart.
        """
        return self._render_chart(title, labels, [{"label": dataset_label, "data": data, "backgroundColor": color}], "bar", subtitle=subtitle)

    def render_stacked_bar_chart(self, title: str, labels: list, datasets: list, subtitle: str = None) -> str:
        """
        Renders a Chart.js stacked bar chart with multiple datasets.
        
        Args:
            title: Chart title
            subtitle: Optional explanatory text below title
            labels: X-axis labels (shared timeline)
            datasets: List of dicts {label, data, color}
        """
        fmt_datasets = []
        for d in datasets:
            fmt_datasets.append({
                "label": d["label"],
                "data": d["data"],
                "backgroundColor": d.get("color", "rgba(54, 162, 235, 0.6)")
            })
        return self._render_chart(title, labels, fmt_datasets, "bar", stacked=True, subtitle=subtitle)

    def render_line_chart(self, title: str, labels: list, datasets: list, subtitle: str = None) -> str:
        """
        Renders a Chart.js line chart with multiple datasets.
        
        Args:
            title: Chart title
            subtitle: Optional explanatory text below title
            labels: X-axis labels
            datasets: List of dicts {label, data, color}
        """
        fmt_datasets = []
        for d in datasets:
            fmt_datasets.append({
                "label": d["label"],
                "data": d["data"],
                "borderColor": d.get("color", "rgba(54, 162, 235, 1)"),
                "backgroundColor": d.get("color", "rgba(54, 162, 235, 0.2)"),
                "fill": False,
                "tension": 0.1
            })
        return self._render_chart(title, labels, fmt_datasets, "line", subtitle=subtitle)

    def _render_chart(self, title: str, labels: list, datasets: list, chart_type: str, stacked: bool = False, subtitle: str = None) -> str:
        template = self.env.get_template("chart.html.j2")
        chart_id = f"chart_{uuid.uuid4().hex}"
        return template.render(
            title=title,
            subtitle=subtitle,
            chart_id=chart_id,
            labels=labels,
            datasets=datasets,
            chart_type=chart_type,
            stacked=stacked
        )
    
    def render_dashboard(self, title: str, components: list) -> str:
        """
        Renders a dashboard containing multiple components.
        
        Args:
            title: Dashboard title
            components: List of pre-rendered HTML component strings
        
        Returns:
            Rendered HTML dashboard string
        """
        template = self.env.get_template("dashboard.html.j2")
        return template.render(
            title=title,
            components=components
        )

# Singleton instance for convenience
renderer = ReportRenderer()

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
ReportRenderer: Jinja2-based HTML rendering for Robot Framework reports.
"""
import os
import json
import time
from jinja2 import Environment, FileSystemLoader, select_autoescape

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
        return template.render(
            title=title,
            headers=headers,
            rows=rows,
            width=width
        )
    
    def render_bar_chart(self, title: str, labels: list, data: list, 
                         dataset_label: str = "Value",
                         color: str = "rgba(54, 162, 235, 0.6)") -> str:
        """
        Renders a Chart.js bar chart.
        
        Args:
            title: Chart title
            labels: List of X-axis labels
            data: List of Y-axis values
            dataset_label: Label for the dataset
            color: Bar color (CSS rgba)
        
        Returns:
            Rendered HTML string with embedded Chart.js
        """
        template = self.env.get_template("chart.html.j2")
        chart_id = f"chart_{int(time.time() * 1000)}"
        return template.render(
            title=title,
            chart_id=chart_id,
            labels=labels,
            data=data,
            dataset_label=dataset_label,
            color=color
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

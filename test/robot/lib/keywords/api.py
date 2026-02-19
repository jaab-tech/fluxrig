# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

import subprocess
import time
import json
import os
from robot.api import logger
from robot.api.deco import keyword

# Import the renderer (relative import handled by Python path setup in FluxRigLibrary)
import sys
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from reporting.renderer import renderer

class ApiKeywords:
    """
    Keywords for interacting with FluxRig Mixer API.
    """

    @keyword
    def wait_for_healthy(self, port: int, timeout: int = 30):
        # Mixer serves health at /api/v1/health
        url = f"http://127.0.0.1:{port}/api/v1/health"
        start = time.time()
        
        last_error = None
        
        while time.time() - start < timeout:
            # Use curl
            try:
                # -s silent, -f fail silently on server error, -o /dev/null discard output
                subprocess.check_call(["curl", "-s", "-f", "-o", "/dev/null", url])
                logger.info(f"Health check passed for port {port}")
                return
            except subprocess.CalledProcessError as e:
                last_error = e
                # Check if process died
                if hasattr(self, 'processes') and "mixer" in self.processes:
                    proc = self.processes["mixer"]["proc"]
                    if proc.poll() is not None:
                         raise RuntimeError(f"Mixer process died with code {proc.returncode}")
                time.sleep(1)
            
        raise RuntimeError(f"Timeout waiting for health on port {port}. Last Error: {last_error}")

    @keyword
    def wait_for_rack_registration(self, mixer_port: int, rack_name: str, timeout: int = 30):
        """Waits for the Rack to appear in the Mixer's registry via API (using curl)."""
        url = f"http://127.0.0.1:{mixer_port}/api/v1/racks"
        start = time.time()
        
        while time.time() - start < timeout:
            try:
                result = subprocess.run(["curl", "-s", url], capture_output=True, text=True)
                if result.returncode == 0:
                    if rack_name in result.stdout:
                        logger.info(f"Rack '{rack_name}' registered successfully.")
                        return
                    else:
                        logger.debug(f"Rack '{rack_name}' not found yet...")
                else:
                     logger.debug(f"Curl Error: {result.stderr}")
            except Exception as e:
                logger.debug(f"Curl execution failed: {e}")
            
            time.sleep(1)
            
        raise RuntimeError(f"Timeout waiting for Rack '{rack_name}' registration")

    @keyword
    def import_scenario(self, mixer_port: int, file_path: str):
        """Imports a Scenario YAML via the Mixer API."""
        url = f"http://127.0.0.1:{mixer_port}/api/v1/scenario/import?activate=true"
        
        # Ensure file path is absolute
        if not os.path.isabs(file_path):
             file_path = os.path.abspath(file_path)
             
        if not os.path.exists(file_path):
             raise RuntimeError(f"Scenario file not found: {file_path}")
             
        # Use curl to POST data-binary
        cmd = [
            "curl", "-s", "-f", "-X", "POST", url,
            "-H", "Content-Type: application/x-yaml",
            "--data-binary", f"@{file_path}"
        ]
        
        logger.info(f"Importing Scenario: {file_path}")
        try:
            subprocess.check_call(cmd)
            logger.info("Scenario imported successfully.")
        except subprocess.CalledProcessError as e:
            raise RuntimeError(f"Failed to import scenario: {e}")

    @keyword
    def wait_for_pending_rack(self, mixer_port: int, timeout: int = 30):
        """Waits for ANY rack with status 'pending' to appear. Returns its machine_id."""
        url = f"http://127.0.0.1:{mixer_port}/api/v1/racks?status=pending"
        start = time.time()
        
        while time.time() - start < timeout:
            try:
                result = subprocess.run(["curl", "-s", url], capture_output=True, text=True)
                if result.returncode == 0:
                    try:
                        racks = json.loads(result.stdout)
                        if len(racks) > 0:
                            rack = racks[0]
                            # machine_id might be int or str, normalize return if needed, but returning as is is fine
                            mid = rack['machine_id']
                            logger.info(f"Found Pending Rack: {rack.get('name', 'N/A')} (ID: {mid})")
                            return mid
                    except json.JSONDecodeError:
                        logger.debug(f"Invalid JSON: {result.stdout}")
                else:
                     logger.debug(f"Curl Error: {result.stderr}")
            except Exception as e:
                logger.debug(f"Curl executed failed: {e}")
            
            time.sleep(1)
            
        raise RuntimeError("Timeout waiting for pending rack")

    @keyword
    def adopt_rack(self, mixer_port: int, machine_id: str, name: str):
        """
        Adopts a pending rack by sending a POST to /api/v1/racks/{id}/approve.
        """
        url = f"http://localhost:{mixer_port}/api/v1/racks/{machine_id}/approve"
        payload = json.dumps({"name": name})
        
        cmd = [
            "curl", "-s", "-f", "-X", "POST", url,
            "-H", "Content-Type: application/json",
            "-d", payload
        ]
        
        logger.info(f"Adopting Rack {machine_id} as '{name}'...")
        try:
            subprocess.check_call(cmd)
            logger.info("Adoption Request Successful")
        except subprocess.CalledProcessError as e:
            raise RuntimeError(f"Adoption failed for rack {machine_id}: {e}")

    @keyword
    def verify_rack_status(self, mixer_port: int, machine_id: int, expected_status: str, timeout: int = 30):
        """
        Verifies that a rack has the expected status.
        """
        url = f"http://localhost:{mixer_port}/api/v1/racks"
        start = time.time()
        
        while time.time() - start < timeout:
            try:
                output = subprocess.check_output(["curl", "-s", "-f", url])
                racks = json.loads(output)
                
                for rack in racks:
                    if str(rack.get("machine_id")) == str(machine_id):
                        status = rack.get("status")
                        if status == expected_status:
                            logger.info(f"Rack {machine_id} is now {status}")
                            return
                        else:
                            logger.debug(f"Rack {machine_id} status is {status}, waiting for {expected_status}")
            except Exception as e:
                logger.debug(f"Pooling rack status failed: {e}")
            
            time.sleep(1)
            
        raise RuntimeError(f"Timeout waiting for rack {machine_id} to become {expected_status}")

    @keyword
    def validate_rack_in_registry(self, mixer_port: int, machine_id: str, expected_status: str, expected_name: str = ""):
        """
        Validates that a specific Rack exists in the registry with the expected status and name.
        """
        url = f"http://localhost:{mixer_port}/api/v1/racks"
        try:
             output = subprocess.check_output(["curl", "-s", "-f", url])
             racks = json.loads(output)
        except subprocess.CalledProcessError as e:
             raise RuntimeError(f"Failed to fetch registry: {e}")

        found = None
        for r in racks:
            if str(r.get("machine_id")) == str(machine_id):
                found = r
                break
        
        if not found:
            raise RuntimeError(f"Rack {machine_id} not found in registry")
            
        actual_status = found.get("status")
        if actual_status != expected_status:
            raise RuntimeError(f"Rack {machine_id} status mismatch. Expected '{expected_status}', got '{actual_status}'")

        actual_name = found.get("name", "")
        if expected_name and actual_name != expected_name:
             raise RuntimeError(f"Rack {machine_id} name mismatch. Expected '{expected_name}', got '{actual_name}'")
        
        logger.info(f"Registry Validation Passed for Rack {machine_id} (Status: '{actual_status}', Name: '{actual_name}')")

    @keyword
    def log_registry_contents(self, mixer_port: int):
        """Fetches and logs complete registry contents as a dashboard with multiple tables."""
        base_url = f"http://localhost:{mixer_port}/api/v1"
        components = []
        
        try:
            # 1. Mixer Config/Health
            try:
                config_out = subprocess.check_output(["curl", "-s", "-f", f"{base_url}/config"])
                config_data = json.loads(config_out)
                
                # Flatten config for display
                config_rows = []
                for key, value in config_data.items():
                    if isinstance(value, dict):
                        for subkey, subval in value.items():
                            config_rows.append([f"{key}.{subkey}", str(subval)[:50]])
                    else:
                        config_rows.append([key, str(value)[:50]])
                
                config_table = renderer.render_table(
                    title="Mixer Configuration",
                    headers=["Setting", "Value"],
                    rows=config_rows[:10],  # Limit to first 10 for readability
                    width="60%"
                )
                components.append(config_table)
            except Exception as e:
                logger.debug(f"Failed to fetch config: {e}")
            
            # 2. Racks
            try:
                racks_out = subprocess.check_output(["curl", "-s", "-f", f"{base_url}/racks"])
                racks = json.loads(racks_out)
                
                rack_rows = []
                for r in racks:
                    rack_rows.append([
                        r.get('machine_id', ''),
                        r.get('name', ''),
                        r.get('status', ''),
                        r.get('ip', '') or '-',
                        r.get('first_seen', '')[:19] if r.get('first_seen') else ''
                    ])
                
                racks_table = renderer.render_table(
                    title="Racks",
                    headers=["ID", "Name", "Status", "IP", "First Seen"],
                    rows=rack_rows,
                    width="100%"
                )
                components.append(racks_table)
            except Exception as e:
                logger.debug(f"Failed to fetch racks: {e}")
            
            # 3. Topology (Gears and Scenario info)
            try:
                topo_out = subprocess.check_output(["curl", "-s", "-f", f"{base_url}/topology/list"])
                topo = json.loads(topo_out)
                
                # Gears table
                gears = topo.get('gears', [])
                if gears:
                    gear_rows = [[i+1, g] for i, g in enumerate(gears)]
                    gears_table = renderer.render_table(
                        title="Gears",
                        headers=["#", "Name"],
                        rows=gear_rows,
                        width="40%"
                    )
                    components.append(gears_table)
            except Exception as e:
                logger.debug(f"Failed to fetch topology: {e}")
            
            # 4. Topology Status (Scenario version, sync status)
            try:
                status_out = subprocess.check_output(["curl", "-s", "-f", f"{base_url}/topology/status"])
                status = json.loads(status_out)
                
                status_rows = [
                    ["Sync Status", status.get('sync_status', 'unknown')],
                    ["Active Scenario", status.get('active_ver', 'none')],
                    ["Total Racks", status.get('racks_total', 0)]
                ]
                
                status_table = renderer.render_table(
                    title="Topology Status",
                    headers=["Property", "Value"],
                    rows=status_rows,
                    width="40%"
                )
                components.append(status_table)
            except Exception as e:
                logger.debug(f"Failed to fetch topology status: {e}")
            
            # Render dashboard
            if components:
                dashboard = renderer.render_dashboard(
                    title="FluxRig Registry Overview",
                    components=components
                )
                logger.info(dashboard, html=True)
            else:
                logger.warn("No registry data available")
            
        except Exception as e:
            logger.error(f"Failed to fetch registry: {e}")

    @keyword
    def get_rack_status_by_name(self, mixer_port: int, rack_name: str):
        """Returns the status of the rack with the given name, or 'unknown'."""
        url = f"http://localhost:{mixer_port}/api/v1/racks"
        try:
            output = subprocess.check_output(["curl", "-s", "-f", url])
            racks = json.loads(output)
            for r in racks:
                if r.get("name") == rack_name:
                    return r.get("status")
        except Exception as e:
            logger.debug(f"Failed to fetch racks: {e}")
        return "unknown"

    @keyword
    def get_entity_stats(self, mixer_port: int, machine_id: str = None):
        """
        Fetches entity stats. Use machine_id to fetch specific entity stats.
        Returns a dictionary mapping entity IDs (string) to stats objects.
        If machine_id is specified, returns that entity's stats object directly (or error).
        """
        url = f"http://localhost:{mixer_port}/api/v1/entities"
        if machine_id:
             url += f"/{machine_id}/stats"
        else:
             url += "/stats"

        try:
             output = subprocess.check_output(["curl", "-s", "-f", url])
             return json.loads(output)
        except subprocess.CalledProcessError as e:
             raise RuntimeError(f"Failed to fetch stats from {url}: {e}")

    @keyword
    def verify_entity_metric_present(self, mixer_port: int, entity_name: str, metric_key: str):
        """
        Verifies that a specific metric key exists and has a positive value for the named entity.
        Fails if entity not found, key not found, or value <= 0.
        """
        stats_list = self.get_entity_stats(mixer_port)
        if stats_list is None:
            stats_list = []
        
        # stats_list IS a list of dicts: [{"id":..., "name":..., "stats":{...}}]
        # But wait, handleEntityStats returns a MAP of ID->Stats if fetching all?
        # Check server.go:432: _ = json.NewEncoder(w).Encode(ids) -> []EntityStats?
        # server.go:431: ids := s.metricsCache.GetAllStats() -> This returns []EntityStats.
        # So it is a list.
        
        found_entity = None
        if not found_entity:
            names = [s.get('entity_name') for s in stats_list]
            raise RuntimeError(f"Entity '{entity_name}' not found in stats. Available: {names}")
            
        metrics = found_entity.get('metrics', {})
        if metric_key not in metrics:
            raise RuntimeError(f"Metric '{metric_key}' not found for entity '{entity_name}'. Available: {list(metrics.keys())}")
            
        # Metric value is inside 'value' field of metric object
        metric_obj = metrics[metric_key]
        val = metric_obj.get('value', 0)
        
        # Check if number > 0
        if isinstance(val, (int, float)) and val <= 0:
             raise RuntimeError(f"Metric '{metric_key}' value is {val} (expected > 0)")
        
        logger.info(f"Verified metric '{metric_key}' for '{entity_name}': {val}")




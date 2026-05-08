# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

import os
import time
import duckdb
from robot.api import logger
from robot.api.deco import keyword, library

@library
class TelemetryLibrary:
    """
    fluxrig Telemetry Verification Library.
    Uses DuckDB to query Parquet files produced by Mixer.
    """
    
    ROBOT_LISTENER_API_VERSION = 3

    def __init__(self):
        self.conn = duckdb.connect()

    @keyword
    def validate_telemetry_stored(self, mixer_data_dir: str, rack_name: str, timeout: int = 20):
        """
        Verifies that logs/metrics from a specific rack name are present in the Mixer's telemetry storage.
        Retries until found or timeout.
        """
        # Parquet structure: <mixer_data_dir>/telemetry/logs/year=.../proto.parquet
        # We can use wildcard globbing in DuckDB: <mixer_data_dir>/telemetry/logs/**/*.parquet
        
        logs_path = os.path.join(mixer_data_dir, "telemetry", "logs", "**", "*.parquet")
        start = time.time()
        
        logger.info(f"Checking for telemetry from '{rack_name}' in {logs_path}...")
        
        while time.time() - start < timeout:
            try:
                # Query count of logs where attributes['host.name'] == rack_name
                # Note: attributes is usually a MAP or STRUCT in Parquet depending on OTel schema.
                # fluxrig writes strict OTel format.
                # Assuming attributes is a MAP(STRING, STRING) or similar.
                
                # Check if file exists first to avoid DuckDB error on empty glob
                # (DuckDB throws if no files match glob)
                if not self._files_exist(mixer_data_dir):
                     logger.debug("No parquet files found yet...")
                     time.sleep(2)
                     continue

                # Query
                query = f"""
                    SELECT count(*) 
                    FROM read_parquet('{logs_path}') 
                    WHERE entity_name = '{rack_name}'
                """
                
                count = self.conn.execute(query).fetchone()[0]
                
                if count > 0:
                    logger.info(f"Telemetry Validation Passed: Found {count} logs for rack '{rack_name}'")
                    return
                else:
                    logger.debug(f"Query returned 0 logs for '{rack_name}'...")
                    
            except Exception as e:
                logger.debug(f"DuckDB Query Error: {e}")
            
            time.sleep(2)
            
        raise AssertionError(f"Timeout waiting for telemetry from rack '{rack_name}'")

    def _files_exist(self, mixer_data_dir):
        telemetry_dir = os.path.join(mixer_data_dir, "telemetry", "logs")
        if not os.path.exists(telemetry_dir):
            return False
        # Do a quick walk to see if any .parquet exists
        for root, dirs, files in os.walk(telemetry_dir):
            for file in files:
                if file.endswith(".parquet"):
                    return True
        return False

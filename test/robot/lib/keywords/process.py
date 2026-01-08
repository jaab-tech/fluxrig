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
import signal
import shutil
import time
import tomli
from robot.api import logger
from robot.api.deco import keyword

class ProcessKeywords:
    """
    Keywords for managing FluxRig processes (Mixer, Rack) and workspace.
    Expects 'self.processes' and 'self.root_dir' to be available on the instance.
    """

    def _parse_toml(self, config_file: str) -> dict:
        with open(config_file, "rb") as f:
            return tomli.load(f)

    def _prepare_component_dir(self, comp_home):
        # Create Isolated Directory Structure
        os.makedirs(os.path.join(comp_home, "data"), exist_ok=True)
        os.makedirs(os.path.join(comp_home, "logs"), exist_ok=True)
        return comp_home

    @keyword
    def force_cleanup_environment(self):
        """
        Safely kills any lingering FluxRig binaries (mixer/rack) from previous runs.
        Does NOT use 'pkill -f' to avoid killing the IDE/Agent.
        """
        targets = ["fluxrig", "fluxrig-mixer"]
        logger.info(f"Ensuring environment is clean (killing {targets})...")
        
        for name in targets:
            try:
                # pgrep -x matches exact process name, avoiding path matching
                pids = subprocess.check_output(["pgrep", "-x", name]).decode().split()
                for pid in pids:
                    logger.info(f"Killing lingering {name} (PID: {pid})")
                    os.kill(int(pid), signal.SIGKILL)
            except subprocess.CalledProcessError:
                # No process found, which is good
                pass
            except Exception as e:
                logger.warn(f"Failed to kill {name}: {e}")

    @keyword
    def setup_workspace(self, suite_path: str, output_dir: str = None):
        """
        Creates a temporal workspace in /tmp and symlinks it to the suite directory.
        Symlinks 'results' to the Robot Framework Output Directory (if provided).
        """
        suite_name = os.path.basename(suite_path)
        timestamp = time.strftime("%Y%m%d_%H%M%S")
        work_dir = f"/tmp/fluxrig/work_robot_{suite_name}_{timestamp}"
        
        if os.path.exists(work_dir):
            shutil.rmtree(work_dir)
            
        os.makedirs(work_dir, exist_ok=True)
              
        # Link 'work' -> /tmp workspace
        link_path = os.path.join(suite_path, "work")
        if os.path.islink(link_path) or os.path.exists(link_path):
             os.remove(link_path)
        os.symlink(work_dir, link_path)
        
        # Link 'results' -> Robot Output Dir (where logs/reports are)
        if output_dir:
            results_link = os.path.join(suite_path, "results")
            if os.path.islink(results_link) or os.path.exists(results_link):
                 os.remove(results_link)
            # Ensure output_dir is absolute
            abs_output = os.path.abspath(output_dir)
            os.symlink(abs_output, results_link)
            logger.info(f"Workspace created: {work_dir} -> {link_path}")
            logger.info(f"Results linked: {abs_output} -> {results_link}")
        else:
             logger.info(f"Workspace created: {work_dir} -> {link_path} (No results link)")
             
        return work_dir

    @keyword
    def generate_cluster_key(self, work_dir: str):
        """
        Generates a cluster key in the component's data directory.
        Uses absolute paths.
        """
        # Key location: <work_dir>/data/cluster.key
        target_dir = os.path.join(work_dir, "data")
        os.makedirs(target_dir, exist_ok=True)
        
        bin_path = os.path.join(self.root_dir, "bin", "fluxrig")
        key_path = os.path.join(target_dir, "cluster.key")
        
        cmd = [bin_path, "keys", "gen-cluster", "-o", key_path]
        logger.info(f"Generating Key: {' '.join(cmd)}")
        
        subprocess.check_call(cmd, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        return key_path

    @keyword
    def start_mixer(self, config_file: str, work_dir: str, alias: str = "mixer"):
        """
        Starts the FluxRig Mixer process.
        Uses CWD Isolation so TOML relative paths populate the component directory.
        """
        bin_path = os.path.join(self.root_dir, "bin", "fluxrig-mixer")
        if not os.path.isabs(config_file):
            config_file = os.path.abspath(config_file)
            
        # Parse TOML for Port and Log File
        cfg = self._parse_toml(config_file)
        port = cfg.get("api", {}).get("port", 8090)
        log_rel_path = cfg.get("logging", {}).get("filename", "logs/mixer.log")

        # work_dir IS the component home now
        comp_home = self._prepare_component_dir(work_dir)
        
        # Determine expected absolute log path (for verification)
        # Since we run with CWD=comp_home, the log will be created at comp_home/log_rel_path
        abs_log_path = os.path.join(comp_home, log_rel_path)
        
        # Don't Override Logs/Store via Env - rely on CWD
        env = os.environ.copy()
        # Remove any lingering overrides if they exist in shell
        env.pop("FLUXRIG_STORE_DIR", None)
        env.pop("FLUXRIG_LOGGING_FILENAME", None)

        cmd = [bin_path, "-c", config_file]
        logger.info(f"Starting Mixer '{alias}': {' '.join(cmd)}")
        logger.info(f"  > CWD: {comp_home}")
        logger.info(f"  > Expected Log: {abs_log_path}")
        
        # Capture stdout/stderr to a debug log
        debug_log = os.path.join(comp_home, "logs", "process_stdout.log")
        
        proc = subprocess.Popen(
            cmd,
            stdout=open(debug_log, "w"),
            stderr=subprocess.STDOUT,
            cwd=comp_home, # ISOLATION: Run inside the component dir
            env=env,
            preexec_fn=os.setsid
        )
        
        # Store metadata
        self.processes[alias] = {
            "proc": proc,
            "log_file": abs_log_path,
            "home": comp_home
        }
        
        # Call wait_for_healthy (must be available in the main class or mixin)
        # Since this is a mixin, self.wait_for_healthy works if guaranteed to exist.
        if hasattr(self, 'wait_for_healthy'):
            self.wait_for_healthy(port)
        else:
            logger.warn("wait_for_healthy not found, skipping health check.")

    @keyword
    def start_rack(self, config_file: str, work_dir: str, mixer_home: str = None, alias: str = "rack"):
        """
        Starts a FluxRig Rack process.
        Uses CWD Isolation.
        """
        bin_path = os.path.join(self.root_dir, "bin", "fluxrig")
        if not os.path.isabs(config_file):
            config_file = os.path.abspath(config_file)

        cfg = self._parse_toml(config_file)
        log_rel_path = cfg.get("logging", {}).get("filename", "logs/rack.log")

        comp_home = self._prepare_component_dir(work_dir)
        store_dir = os.path.join(comp_home, "data")
        abs_log_path = os.path.join(comp_home, log_rel_path)

        # Copy Cluster Key
        if mixer_home:
             mixer_key = os.path.join(mixer_home, "data", "cluster.key")
             rack_key = os.path.join(store_dir, "cluster.key")
             if os.path.exists(mixer_key):
                 shutil.copy(mixer_key, rack_key)
                 logger.info(f"Provisioned cluster key to {rack_key}")
             else:
                 logger.warn(f"Mixer cluster key not found at {mixer_key}")
        
        env = os.environ.copy()
        env.pop("FLUXRIG_STORE_DIR", None)
        env.pop("FLUXRIG_LOGGING_FILENAME", None)

        cmd = [bin_path, "rack", "-c", config_file]
        logger.info(f"Starting Rack '{alias}': {' '.join(cmd)}")
        logger.info(f"  > CWD: {comp_home}")

        debug_log = os.path.join(comp_home, "logs", "process_stdout.log")
        
        proc = subprocess.Popen(
            cmd,
            stdout=open(debug_log, "w"),
            stderr=subprocess.STDOUT,
            cwd=comp_home, # ISOLATION
            env=env,
            preexec_fn=os.setsid
        )
        self.processes[alias] = {
            "proc": proc,
            "log_file": abs_log_path,
            "home": comp_home
        }

    @keyword
    def stop_process(self, alias: str):
        """Stops a specific process by alias."""
        if alias in self.processes:
            entry = self.processes[alias]
            proc = entry["proc"]
            logger.info(f"Stopping {alias} (PID: {proc.pid})")
            try:
                os.killpg(os.getpgid(proc.pid), signal.SIGTERM)
                proc.wait(timeout=5)
            except (ProcessLookupError, subprocess.TimeoutExpired):
                 try:
                     os.killpg(os.getpgid(proc.pid), signal.SIGKILL)
                 except:
                     pass
            del self.processes[alias]

    @keyword
    def stop_all_processes(self):
        """Stops all managed processes."""
        for alias in list(self.processes.keys()):
            self.stop_process(alias)

    @keyword
    def wait_for_port(self, port: int, timeout: int = 30):
        """Waits for a TCP port to be listening using lsof."""
        start = time.time()
        while time.time() - start < timeout:
            try:
                # lsof -i :<port> -sTCP:LISTEN -t
                cmd = ["lsof", "-i", f":{port}", "-sTCP:LISTEN", "-t"]
                output = subprocess.check_output(cmd)
                if output.strip():
                    logger.info(f"Port {port} is listening.")
                    return
            except subprocess.CalledProcessError:
                pass
            time.sleep(1)
        raise RuntimeError(f"Timeout waiting for port {port}")

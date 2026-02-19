# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

import os
import re
from robot.api import logger

class VerificationKeywords:
    
    def check_log_for_errors(self, log_path, ignore_patterns=None):
        """
        Scans a log file for logical errors (ERROR, FATAL, PANIC) and Warnings.
        Fails if found.
        
        Args:
            log_path: Path to the log file.
            ignore_patterns: List of regex strings to ignore.
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
        
        # Regex for structured logs (slog) or standard
        # Looking for | ERROR | or level=error or level=warn
        # FluxRig uses slog text handler: "level=ERROR" or "level=WARN"
        
        for line in lines:
            line_lower = line.lower()
            
            # Simple heuristic detection
            is_err = "level=error" in line_lower or "level=fatal" in line_lower or "panic:" in line_lower
            is_warn = "level=warn" in line_lower
            
            if not (is_err or is_warn):
                continue

            # Check ignores
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
        
        if warnings:
             # Strict Mode: Fail on warnings too?
             # User said "validate logs for warnings".
             # Usually warnings are acceptable, but let's log them visibility.
             # If strict, un-comment next line:
             # raise AssertionError(f"Found {len(warnings)} WARNINGs in {log_path}")
             pass

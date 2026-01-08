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
import sys

# Ensure local lib/keywords can be imported
current_dir = os.path.dirname(os.path.abspath(__file__))
if current_dir not in sys.path:
    sys.path.append(current_dir)

from robot.api.deco import library

# Import Mixins
from keywords.process import ProcessKeywords
from keywords.api import ApiKeywords
from keywords.telemetry import TelemetryKeywords

@library
class FluxRigLibrary(ProcessKeywords, ApiKeywords, TelemetryKeywords):
    """
    FluxRig Process Management Library.
    Composition of Process, API, and Telemetry keywords.
    """
    
    ROBOT_LISTENER_API_VERSION = 3
    ROBOT_LIBRARY_SCOPE = 'TEST SUITE'

    def __init__(self):
        self.processes = {}
        self.root_dir = self._find_root()
        
    def _find_root(self):
        # Assumes test/robot/lib/FluxRigLibrary.py -> ../../../
        current = os.path.dirname(os.path.abspath(__file__))
        return os.path.abspath(os.path.join(current, "../../../"))

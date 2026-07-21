#!/bin/bash
# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

set -e

DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
cd "$DIR"

echo "Building Zig Wasm payload..."
# Use zig locally instead of docker
mkdir -p build
zig build-exe src/polyglot.zig -target wasm32-freestanding -fno-entry -O ReleaseSmall --export=alloc --export=free --export=process
mv polyglot.wasm build/

echo "Running Wasm Polyglot E2E Test..."
# Use the unified robot runner
cd ../../../test/robot
./run.sh ../e2e/12_wasm_polyglot/test_wasm.robot

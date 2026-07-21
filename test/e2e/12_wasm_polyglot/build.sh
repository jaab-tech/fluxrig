#!/bin/bash
set -e

# Build Zig to Wasm (freestanding)
# We use docker to avoid requiring zig locally.
docker run --rm -v $(pwd):/app -w /app ziglang/zig:latest zig build-lib src/polyglot.zig -target wasm32-freestanding -dynamic -O ReleaseSmall

# The output is polyglot.wasm
mkdir -p build
mv polyglot.wasm build/
echo "Built Wasm payload to build/polyglot.wasm"

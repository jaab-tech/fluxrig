const std = @import("std");

// Expose the allocator to the host
const allocator = std.heap.wasm_allocator;

// Host functions we expect
extern "env" fn log(level: i32, ptr: [*]const u8, len: i32) void;

fn hostLog(level: i32, msg: []const u8) void {
    log(level, msg.ptr, @as(i32, @intCast(msg.len)));
}

// Exported ABI functions for Host to call
export fn alloc(len: u32) u32 {
    const slice = allocator.alloc(u8, len) catch return 0;
    return @intFromPtr(slice.ptr);
}

export fn free(ptr_val: u32, len: u32) void {
    const ptr: [*]u8 = @ptrFromInt(ptr_val);
    const slice = ptr[0..len];
    allocator.free(slice);
}

// process is the entrypoint
export fn process(ptr_val: u32, len: u32) u64 {
    hostLog(2, "Zig Wasm Gear: Process called!");

    // In a real gear, we would parse CBOR here.
    // For this simple polyglot test, we will just echo it back.
    // We allocate a new buffer to simulate transformation.
    
    const ptr: [*]u8 = @ptrFromInt(ptr_val);
    const input = ptr[0..len];
    
    // Create new response
    const output = allocator.alloc(u8, len) catch {
        hostLog(4, "Zig Wasm Gear: Alloc failed for output");
        return 0;
    };
    
    @memcpy(output, input);
    
    // We assume the host will free the output buffer
    
    const ret_ptr: u64 = @intFromPtr(output.ptr);
    const ret_len: u64 = len;
    
    return (ret_ptr << 32) | ret_len;
}

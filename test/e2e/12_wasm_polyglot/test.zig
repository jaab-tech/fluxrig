const std = @import("std"); export fn alloc(len: u32) [*]u8 { return std.heap.wasm_allocator.alloc(u8, len) catch return @as([*]u8, @ptrFromInt(0)); }

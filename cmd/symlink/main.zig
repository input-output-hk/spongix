const std = @import("std");
const builtin = @import("builtin");

// Zig 0.16 removed the cross-platform std.posix.{write,symlink} wrappers. Use raw Linux
// syscalls (no libc, keeps the static binary tiny) on Linux, and libc on macOS, which has
// no stable syscall ABI. Both shims return null on success or the errno name on failure.
const sys = switch (builtin.os.tag) {
    .linux => struct {
        fn write(s: []const u8) void {
            _ = std.os.linux.write(2, s.ptr, s.len);
        }
        fn symlink(target: [*:0]const u8, linkpath: [*:0]const u8) ?[]const u8 {
            const e = std.os.linux.errno(std.os.linux.symlink(target, linkpath));
            return if (e == .SUCCESS) null else @tagName(e);
        }
    },
    .macos => struct {
        fn write(s: []const u8) void {
            _ = std.c.write(2, s.ptr, s.len);
        }
        fn symlink(target: [*:0]const u8, linkpath: [*:0]const u8) ?[]const u8 {
            const e = std.c.errno(std.c.symlink(target, linkpath));
            return if (e == .SUCCESS) null else @tagName(e);
        }
    },
    else => @compileError("symlink binary only targets linux and macos"),
};

pub fn main(init: std.process.Init.Minimal) void {
    const argv = init.args.vector; // []const [*:0]const u8
    if (argv.len != 3) {
        sys.write("usage: symlink <target> <linkpath>\n");
        std.process.exit(1);
    }
    if (sys.symlink(argv[1], argv[2])) |err_name| {
        sys.write("symlink: ");
        sys.write(std.mem.span(argv[1]));
        sys.write(" -> ");
        sys.write(std.mem.span(argv[2]));
        sys.write(": ");
        sys.write(err_name);
        sys.write("\n");
        std.process.exit(1);
    }
}

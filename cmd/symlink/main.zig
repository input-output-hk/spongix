const std = @import("std");

fn writeStr(fd: std.posix.fd_t, s: []const u8) void {
    _ = std.posix.write(fd, s) catch {};
}

pub fn main() void {
    const argv = std.os.argv;
    const stderr: std.posix.fd_t = 2;
    if (argv.len != 3) {
        writeStr(stderr, "usage: symlink <target> <linkpath>\n");
        std.posix.exit(1);
    }
    const target = std.mem.span(argv[1]);
    const linkpath = std.mem.span(argv[2]);
    std.posix.symlink(target, linkpath) catch |err| {
        writeStr(stderr, "symlink: ");
        writeStr(stderr, target);
        writeStr(stderr, " -> ");
        writeStr(stderr, linkpath);
        writeStr(stderr, ": ");
        writeStr(stderr, @errorName(err));
        writeStr(stderr, "\n");
        std.posix.exit(1);
    };
}

"""Add a write restriction before sourcing .env or starting any agent process.

The runner supplies this program inline and supplies its policy in argv. Neither
is loaded from the agent's filesystem, and Bootstrap refuses an interpreter
under an agent-writable root. OpenShell remains the outer sandbox;
this additional, inherited Landlock ruleset only removes write permissions.
Linux Landlock ABI 3+ is required so truncate is mediated too.
"""

import contextlib
import ctypes
import hashlib
import json
import os
import stat
import sys

WRITE = 1 << 1
TRUNCATE = 1 << 14
WRITE_RIGHTS = WRITE | sum(1 << bit for bit in range(4, 15))


class Ruleset(ctypes.Structure):
    _fields_ = [("handled_access_fs", ctypes.c_uint64)]


class PathRule(ctypes.Structure):
    _pack_ = 1
    _layout_ = "ms"  # Python 3.14 deprecates _pack_ without it
    _fields_ = [("allowed_access", ctypes.c_uint64), ("parent_fd", ctypes.c_int32)]


def launch(policy, command):
    # Landlock cannot revoke writes through a descriptor opened beforehand.
    # Preserve only the runner's stdin/stdout/stderr across the boundary.
    for name in os.listdir("/proc/self/fd"):
        if int(name) >= 3:
            # The descriptor used by listdir may already have closed.
            with contextlib.suppress(OSError):
                os.close(int(name))
    libc = ctypes.CDLL(None, use_errno=True)
    libc.syscall.restype = ctypes.c_long

    def syscall(number, *args):
        result = libc.syscall(ctypes.c_long(number), *args)
        if result == -1:
            code = ctypes.get_errno()
            raise OSError(code, os.strerror(code))
        return result

    if syscall(444, ctypes.c_void_p(), ctypes.c_size_t(0), ctypes.c_uint(1)) < 3:
        raise RuntimeError("Codex runs require Linux Landlock ABI 3+")

    # Freeze the roots, including their directory entries. Granting an ancestor
    # such as /sandbox would also grant policy replacement, so descend through
    # ancestors and grant only siblings. Symlinks are never grant authorities.
    protected = set(policy["protected"])
    ruleset = syscall(
        444, ctypes.byref(Ruleset(WRITE_RIGHTS)), ctypes.c_size_t(8), ctypes.c_uint(0)
    )

    def grant(path):
        if any(path == p or path.startswith(p + "/") for p in protected):
            return
        info = os.lstat(path)
        if stat.S_ISLNK(info.st_mode):
            return
        if any(p.startswith(path + "/") for p in protected):
            if not stat.S_ISDIR(info.st_mode):
                raise RuntimeError("non-directory policy ancestor")
            for name in sorted(os.listdir(path)):
                grant(path + "/" + name)
            return
        fd = os.open(path, os.O_PATH | os.O_CLOEXEC | os.O_NOFOLLOW)
        try:
            mode = os.fstat(fd).st_mode
            if stat.S_ISDIR(mode):
                rights = WRITE_RIGHTS
            elif stat.S_ISREG(mode):
                rights = WRITE | TRUNCATE
            elif path == "/dev/null" and stat.S_ISCHR(mode):
                rights = WRITE
            else:
                return
            syscall(
                445,
                ctypes.c_int(ruleset),
                ctypes.c_int(1),
                ctypes.byref(PathRule(rights, fd)),
                ctypes.c_uint(0),
            )
        finally:
            os.close(fd)

    try:
        for path in policy["write_roots"]:
            grant(path)
        # Mutable native state is beneath a frozen CODEX_HOME. These are exact
        # runner-selected paths, never values from config.toml or .env.
        protected.clear()
        for path in policy["state_paths"]:
            grant(path)
        if libc.prctl(38, 1, 0, 0, 0) != 0:
            raise OSError(ctypes.get_errno(), "PR_SET_NO_NEW_PRIVS failed")
        syscall(446, ctypes.c_int(ruleset), ctypes.c_uint(0))
    finally:
        os.close(ruleset)

    # Validate AFTER restricting writes, closing the check-to-load race. Parent
    # directories are frozen too; reject preexisting symlinks and hardlinks.
    def regular(path, directory=False):
        current = "/"
        for part in path.strip("/").split("/"):
            current = os.path.join(current, part)
            info = os.lstat(current)
            if stat.S_ISLNK(info.st_mode):
                raise RuntimeError("symlink in Codex policy path")
        if directory:
            if not stat.S_ISDIR(info.st_mode):
                raise RuntimeError("Codex policy directory missing")
        elif not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
            raise RuntimeError("Codex policy must contain singly linked regular files")

    for path, expected in policy["files"].items():
        regular(path)
        with open(path, "rb") as source:
            content = source.read((1 << 20) + 1)
        if len(content) > 1 << 20 or hashlib.sha256(content).hexdigest() != expected:
            raise RuntimeError("Codex policy file missing or modified: " + path)
    for path, names in policy["directories"].items():
        regular(path, directory=True)
        if sorted(os.listdir(path)) != sorted(names):
            raise RuntimeError("Codex policy file set modified: " + path)
    os.execv(command[0], command)


try:
    launch(json.loads(sys.argv[1]), sys.argv[2:])
except (OSError, ValueError, KeyError, RuntimeError) as error:
    print("fullsend: Codex write protection failed: " + str(error), file=sys.stderr)
    sys.exit(78)

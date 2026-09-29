"""Native child policy. Invoked inline by a protected PreToolUse hook."""

import json
import os
import sys


def block(reason):
    # Codex treats exit 2 with stderr as blocking. Bypass Python's replaceable
    # stderr wrapper, and avoid shutdown flushing changing the exit status.
    try:
        message = ("fullsend: " + reason + "\n").encode()
        while message:
            written = os.write(2, message)
            if written <= 0:
                break
            message = message[written:]
    except BaseException:
        pass
    os._exit(2)


def check(payload, roles):
    # Codex prefixes namespaced tools: multi_agent_v1resume_agent, collaborationspawn_agent.
    name = str(payload.get("tool_name")) if isinstance(payload, dict) else ""
    if name.endswith("resume_agent"):
        return "resuming children does not preserve role policy; spawn a fresh child"
    if name.endswith("spawn_agent") and name != "spawn_agent":
        return "V2 collaboration is unsupported; native delegation requires the V1 spawn_agent"
    if name != "spawn_agent":
        return "invalid spawn hook payload"
    if payload.get("agent_id") or payload.get("agent_type"):
        return "children may not dispatch grandchildren"
    args = payload.get("tool_input")
    if not isinstance(args, dict):
        return "missing spawn arguments"
    if args.get("agent_type", "default") not in roles:
        return "unknown native role; select a registered role or default"
    if any(key in args and args[key] is not None for key in ("model", "reasoning_effort")):
        return "child model and effort are selected by the runtime; omit overrides"
    if "fork_turns" in args or "task_name" in args:
        return "native delegation requires the V1 tool schema; V2 arguments are unsupported"
    if args.get("fork_context") is not False:
        return "native children require fork_context=false"
    return None


try:
    reason = check(json.load(sys.stdin), json.loads(sys.argv[1]))
except BaseException:
    block("native child policy failed; refusing tool call")
if reason:
    block(reason)

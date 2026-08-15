#!/usr/bin/env python3
"""Extract a resume brief from a Codex CLI session rollout JSONL."""

from __future__ import annotations

import argparse
import json
import re
import sys
from collections import Counter, deque
from datetime import datetime
from pathlib import Path


def sessions_root() -> Path:
    return Path.home() / ".codex" / "sessions"


def session_index_path() -> Path:
    return Path.home() / ".codex" / "session_index.jsonl"


def read_session_meta(path: Path) -> dict:
    with path.open() as fh:
        line = fh.readline()
    if not line:
        return {}
    try:
        obj = json.loads(line)
    except json.JSONDecodeError:
        return {}
    if obj.get("type") != "session_meta":
        return {}
    payload = obj.get("payload") or {}
    return {
        "session_id": payload.get("session_id") or payload.get("id"),
        "cwd": payload.get("cwd"),
        "cli_version": payload.get("cli_version"),
        "model_provider": payload.get("model_provider"),
        "source": payload.get("source"),
        "timestamp": payload.get("timestamp") or obj.get("timestamp"),
    }


def session_id_from_filename(path: Path) -> str | None:
    # rollout-YYYY-mm-ddTHH-MM-SS-<uuid>.jsonl
    stem = path.stem
    parts = stem.split("-")
    # uuid is last 5 hyphen-separated groups after timestamp mess; easier: regex
    m = re.search(
        r"([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$",
        stem,
        flags=re.I,
    )
    return m.group(1) if m else None


def load_thread_names() -> dict[str, str]:
    path = session_index_path()
    if not path.is_file():
        return {}
    names: dict[str, str] = {}
    with path.open() as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            try:
                obj = json.loads(line)
            except json.JSONDecodeError:
                continue
            sid = obj.get("id")
            name = obj.get("thread_name")
            if sid and name:
                names[sid] = name
    return names


def resolve_session(cwd: str, session_id: str | None) -> Path:
    root = sessions_root()
    if not root.is_dir():
        raise SystemExit(f"No Codex sessions directory at {root}")

    if session_id:
        sid = session_id.strip()
        # UUID / partial id via filename, or exact thread name via index.
        hits = sorted(root.rglob(f"*{sid}*.jsonl"))
        if not hits:
            # Try thread name match from index
            names = load_thread_names()
            matched_ids = [
                i for i, name in names.items() if name == sid or sid.lower() in name.lower()
            ]
            for mid in matched_ids:
                hits.extend(root.rglob(f"*{mid}*.jsonl"))
        if not hits:
            raise SystemExit(f"No Codex session rollout found for id/name: {sid}")
        # Prefer exact uuid suffix matches when multiple
        exact = [
            h
            for h in hits
            if (session_id_from_filename(h) or "").startswith(sid)
            or sid in (session_id_from_filename(h) or "")
        ]
        pool = exact or hits
        return max(pool, key=lambda p: p.stat().st_mtime)

    # Latest session whose session_meta.cwd matches $PWD
    cwd_resolved = str(Path(cwd).resolve())
    candidates: list[Path] = []
    for path in root.rglob("rollout-*.jsonl"):
        meta = read_session_meta(path)
        meta_cwd = meta.get("cwd")
        if not meta_cwd:
            continue
        try:
            if str(Path(meta_cwd).resolve()) == cwd_resolved:
                candidates.append(path)
        except OSError:
            if meta_cwd == cwd or meta_cwd == cwd_resolved:
                candidates.append(path)
    if not candidates:
        raise SystemExit(
            f"No Codex sessions found for cwd {cwd!r} under {root}"
        )
    return max(candidates, key=lambda p: p.stat().st_mtime)


def truncate(text: str, limit: int) -> str:
    text = text.strip()
    if len(text) <= limit:
        return text
    return text[: limit - 1].rstrip() + "…"


def clean_user_text(text: str) -> str:
    skills = re.findall(r"\[\$([^\]]+)\]", text)
    skills += re.findall(r"<name>\s*([^<]+?)\s*</name>", text)
    text = re.sub(r"<skill>.*?</skill>", "", text, flags=re.S | re.I)
    text = re.sub(
        r"<environment_context>.*?</environment_context>", "", text, flags=re.S | re.I
    )
    text = re.sub(
        r"<recommended_plugins>.*?</recommended_plugins>", "", text, flags=re.S | re.I
    )
    text = re.sub(
        r"<permissions instructions>.*?</permissions instructions>",
        "",
        text,
        flags=re.S | re.I,
    )
    text = text.strip()
    if skills:
        label = "[skill] " + ", ".join(dict.fromkeys(s.strip() for s in skills if s.strip()))
        if not text:
            return label
        # Drop bare [$skill](path) prefix once skill is labeled
        text = re.sub(r"\[\$[^\]]+\]\([^)]+\)\s*", "", text).strip()
        return f"{label}\n{text}".strip() if text else label
    return text


def parse_tool_blob(raw) -> str:
    if isinstance(raw, dict) or isinstance(raw, list):
        return json.dumps(raw)
    return raw or ""


def clean_path(path: str) -> str | None:
    path = path.strip().strip('"').strip("'")
    # apply_patch blobs often continue with literal \n@@ markers
    path = path.split("\\n")[0].split("\n")[0].strip()
    if not path or len(path) > 512:
        return None
    if any(ch in path for ch in (" ", "\t", "{", "}")):
        return None
    return path


def extract_paths_from_tool_blob(blob: str, files: Counter[str]) -> None:
    for match in re.finditer(
        r"\*\*\* (?:Update|Add|Delete) File:\s*(\S+)", blob
    ):
        path = clean_path(match.group(1))
        if path:
            files[path] += 1
    for match in re.finditer(
        r'"(?:path|file_path|filePath)"\s*:\s*"([^"]+)"', blob
    ):
        path = clean_path(match.group(1))
        if path:
            files[path] += 1


def parse_session(path: Path) -> dict:
    meta = read_session_meta(path)
    if not meta.get("session_id"):
        meta["session_id"] = session_id_from_filename(path)

    names = load_thread_names()
    thread_name = names.get(meta.get("session_id") or "")

    user_turns: list[str] = []
    recent_users: deque[str] = deque(maxlen=4)
    recent_agents: deque[dict] = deque(maxlen=8)
    files: Counter[str] = Counter()
    skills: list[str] = []
    tools: Counter[str] = Counter()
    ending_signals: list[str] = []
    model = None

    with path.open() as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            try:
                obj = json.loads(line)
            except json.JSONDecodeError:
                continue

            kind = obj.get("type")
            payload = obj.get("payload") or {}

            if kind == "turn_context":
                if payload.get("thread_name"):
                    thread_name = payload["thread_name"]
                if payload.get("model"):
                    model = payload.get("model")
                continue

            if kind == "event_msg":
                et = payload.get("type")
                if et == "user_message":
                    raw = payload.get("message") or ""
                    text = clean_user_text(raw)
                    if not text:
                        continue
                    user_turns.append(text)
                    recent_users.append(text)
                    for match in re.finditer(r"\[skill\]\s*([^\n]+)", text):
                        for part in match.group(1).split(","):
                            skill = part.strip()
                            if skill:
                                skills.append(skill)
                elif et == "agent_message":
                    text = (payload.get("message") or "").strip()
                    if text:
                        recent_agents.append(
                            {
                                "phase": payload.get("phase"),
                                "text": text,
                            }
                        )
                elif et == "patch_apply_end":
                    changes = payload.get("changes") or {}
                    if isinstance(changes, dict):
                        for file_path in changes:
                            if isinstance(file_path, str) and file_path:
                                files[file_path] += 1
                elif et == "task_complete":
                    last = payload.get("last_agent_message")
                    if last:
                        ending_signals.append(str(last))
                elif et in {"error", "stream_error"}:
                    msg = payload.get("message") or payload.get("error") or et
                    ending_signals.append(f"{et}: {msg}")
                elif et == "token_count":
                    limits = payload.get("rate_limits") or {}
                    reached = limits.get("rate_limit_reached_type")
                    if reached:
                        ending_signals.append(f"rate_limit:{reached}")
                continue

            if kind != "response_item":
                continue

            pt = payload.get("type")
            if pt in {"function_call", "custom_tool_call"}:
                name = payload.get("name") or "unknown"
                tools[name] += 1
                blob = parse_tool_blob(
                    payload.get("arguments")
                    if pt == "function_call"
                    else payload.get("input")
                )
                extract_paths_from_tool_blob(blob, files)
            elif pt == "message" and payload.get("role") == "assistant":
                # Fallback when event_msg agent_message is sparse
                content = payload.get("content") or []
                texts = []
                if isinstance(content, list):
                    for block in content:
                        if not isinstance(block, dict):
                            continue
                        if block.get("type") in {"output_text", "text"}:
                            t = block.get("text") or ""
                            if t.strip():
                                texts.append(t)
                if texts and not recent_agents:
                    recent_agents.append(
                        {"phase": payload.get("phase"), "text": "\n".join(texts)}
                    )

    return {
        "meta": meta,
        "thread_name": thread_name,
        "model": model,
        "user_turns": user_turns,
        "recent_users": list(recent_users),
        "recent_agents": list(recent_agents),
        "files": files,
        "skills": skills,
        "tools": tools,
        "ending_signals": ending_signals,
        "path": path,
    }


def render(brief: dict) -> str:
    meta = brief["meta"]
    path: Path = brief["path"]
    mtime = datetime.fromtimestamp(path.stat().st_mtime).isoformat(
        timespec="seconds"
    )
    lines: list[str] = []
    lines.append("# Codex session brief")
    lines.append(f"session_id: {meta.get('session_id')}")
    lines.append(f"path: {path}")
    lines.append(f"mtime: {mtime}")
    if brief.get("thread_name"):
        lines.append(f"name: {brief['thread_name']}")
    if meta.get("cwd"):
        lines.append(f"cwd: {meta['cwd']}")
    provider = meta.get("model_provider")
    model = brief.get("model")
    if provider or model:
        lines.append(
            "model: "
            + "/".join(p for p in (provider, model) if p)
        )
    if meta.get("cli_version"):
        lines.append(f"cli_version: {meta['cli_version']}")
    if meta.get("source"):
        lines.append(f"source: {meta['source']}")

    recent_users = brief["recent_users"]
    if recent_users:
        lines.append(f"last_prompt: {truncate(recent_users[-1], 500)}")

    skills = brief["skills"]
    if skills:
        ordered = list(dict.fromkeys(skills))
        lines.append("")
        lines.append("## Skills invoked")
        for skill in ordered:
            lines.append(f"- {skill}")

    tools: Counter[str] = brief["tools"]
    if tools:
        lines.append("")
        lines.append("## Tools used")
        for name, count in tools.most_common(20):
            lines.append(f"- {name} ({count})")

    files: Counter[str] = brief["files"]
    if files:
        lines.append("")
        lines.append("## Files in play")
        for file_path, count in files.most_common(40):
            lines.append(f"- {file_path} ({count})")

    goal_turns = brief["user_turns"][:3]
    if goal_turns:
        lines.append("")
        lines.append("## Opening goal")
        for text in goal_turns:
            lines.append("")
            lines.append("### User")
            lines.append(truncate(text, 1200))

    # Recent turns: last users + last agents, users first then agents
    # (event stream doesn't give easy interleaved indices cheaply)
    if recent_users or brief["recent_agents"]:
        lines.append("")
        lines.append("## Recent turns")
        for text in recent_users:
            lines.append("")
            lines.append("### User")
            lines.append(truncate(text, 1500))
        for agent in brief["recent_agents"]:
            lines.append("")
            phase = agent.get("phase")
            label = f"### Assistant ({phase})" if phase else "### Assistant"
            lines.append(label)
            lines.append(truncate(agent.get("text") or "", 2000))

    endings = brief["ending_signals"]
    lines.append("")
    lines.append("## Ending")
    if endings:
        lines.append(truncate(str(endings[-1]), 800))
    elif brief["recent_agents"]:
        lines.append(truncate(brief["recent_agents"][-1].get("text") or "", 800))
    else:
        lines.append("(no clear ending text — session may have stopped mid-tool)")

    lines.append("")
    lines.append("## Resume instruction")
    lines.append(
        "Continue the interrupted work from this brief. "
        "Do not summarise and wait. Ground against the live workspace, then act."
    )
    return "\n".join(lines) + "\n"


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Extract a resume brief from a Codex session rollout."
    )
    parser.add_argument(
        "--cwd",
        default=str(Path.cwd()),
        help="Workspace cwd used to find the latest session when no id is given",
    )
    parser.add_argument(
        "session_id",
        nargs="?",
        default=None,
        help="Codex session UUID (full/partial) or thread name; omit for latest for --cwd",
    )
    parser.add_argument(
        "--jsonl",
        default=None,
        help="Explicit path to a rollout JSONL (skips resolution)",
    )
    args = parser.parse_args(argv)

    if args.jsonl:
        path = Path(args.jsonl).expanduser()
        if not path.is_file():
            raise SystemExit(f"JSONL not found: {path}")
    else:
        path = resolve_session(args.cwd, args.session_id)

    brief = parse_session(path)
    sys.stdout.write(render(brief))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

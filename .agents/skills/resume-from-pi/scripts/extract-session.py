#!/usr/bin/env python3
"""Extract a resume brief from a Pi coding-agent session JSONL transcript."""

from __future__ import annotations

import argparse
import json
import re
import sys
from collections import Counter
from datetime import datetime
from pathlib import Path


def encode_cwd(cwd: str) -> str:
    # ~/.pi/agent/sessions/--<path with / as ->--/
    return "--" + cwd.strip("/").replace("/", "-") + "--"


def sessions_root() -> Path:
    return Path.home() / ".pi" / "agent" / "sessions"


def resolve_session(cwd: str, session_id: str | None) -> Path:
    root = sessions_root()
    if session_id:
        sid = session_id.strip()
        # Filenames are <timestamp>_<uuid>.jsonl; accept full or partial id.
        hits = sorted(root.glob(f"*/*{sid}*.jsonl"))
        if not hits:
            hits = sorted(root.rglob(f"*{sid}*.jsonl"))
        if not hits:
            raise SystemExit(f"No Pi session JSONL found for id: {sid}")
        if len(hits) > 1:
            # Prefer exact uuid match in the _<id>.jsonl suffix when possible.
            exact = [
                h
                for h in hits
                if h.stem.endswith(f"_{sid}") or h.stem == sid or sid in h.stem.split("_")[-1]
            ]
            # If still multiple, take latest mtime among exact, else among all.
            pool = exact or hits
            return max(pool, key=lambda p: p.stat().st_mtime)
        return hits[0]

    project = root / encode_cwd(cwd)
    if not project.is_dir():
        raise SystemExit(
            f"No Pi session directory for cwd {cwd!r} (looked for {project})"
        )
    files = list(project.glob("*.jsonl"))
    if not files:
        raise SystemExit(f"No Pi session JSONL files in {project}")
    return max(files, key=lambda p: p.stat().st_mtime)


def extract_text(content) -> str:
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        parts = []
        for block in content:
            if isinstance(block, dict) and block.get("type") == "text":
                text = block.get("text") or ""
                if text:
                    parts.append(text)
        return "\n".join(parts)
    return ""


def clean_user_text(text: str) -> str:
    # Pi injects full skill bodies as <skill name="..." ...>...</skill> before the prompt.
    skills = re.findall(
        r'<skill\s+name="([^"]+)"[^>]*>', text, flags=re.I
    )
    stripped = re.sub(r"<skill\b[^>]*>.*?</skill>", "", text, flags=re.S | re.I)
    stripped = stripped.strip()
    if not stripped and skills:
        return "[skill] " + ", ".join(dict.fromkeys(skills))
    if skills:
        prefix = "[skill] " + ", ".join(dict.fromkeys(skills)) + "\n"
        return (prefix + stripped).strip()
    return stripped


def truncate(text: str, limit: int) -> str:
    text = text.strip()
    if len(text) <= limit:
        return text
    return text[: limit - 1].rstrip() + "…"


def path_from_tool(name: str, args: dict) -> str | None:
    for key in ("path", "file_path", "filePath", "filename"):
        value = args.get(key)
        if isinstance(value, str) and value:
            return value
    return None


def parse_args_field(raw) -> dict:
    if isinstance(raw, dict):
        return raw
    if isinstance(raw, str) and raw.strip():
        try:
            value = json.loads(raw)
            if isinstance(value, dict):
                return value
        except json.JSONDecodeError:
            return {}
    return {}


def parse_session(path: Path) -> dict:
    meta = {
        "session_id": None,
        "name": None,
        "cwd": None,
        "provider": None,
        "model": None,
        "thinking_level": None,
    }
    turns: list[dict] = []
    files: Counter[str] = Counter()
    skills: list[str] = []
    ending_signals: list[str] = []

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
            if kind == "session":
                meta["session_id"] = obj.get("id") or meta["session_id"]
                meta["cwd"] = obj.get("cwd") or meta["cwd"]
            elif kind == "session_info":
                meta["name"] = obj.get("name") or meta["name"]
            elif kind == "model_change":
                meta["provider"] = obj.get("provider") or meta["provider"]
                meta["model"] = obj.get("modelId") or meta["model"]
            elif kind == "thinking_level_change":
                meta["thinking_level"] = (
                    obj.get("thinkingLevel") or meta["thinking_level"]
                )
            elif kind != "message":
                continue

            message = obj.get("message") or {}
            role = message.get("role")
            content = message.get("content")
            timestamp = obj.get("timestamp") or message.get("timestamp")

            if role == "user":
                text = clean_user_text(extract_text(content))
                if not text:
                    continue
                for match in re.finditer(r"\[skill\]\s*([^\n,]+)", text):
                    for part in match.group(1).split(","):
                        skill = part.strip()
                        if skill:
                            skills.append(skill)
                # Also catch bare skill tags left in text
                for match in re.finditer(
                    r'<skill\s+name="([^"]+)"', text, flags=re.I
                ):
                    skills.append(match.group(1))
                turns.append(
                    {"role": "user", "text": text, "timestamp": timestamp}
                )
            elif role == "assistant":
                text = extract_text(content)
                tools: list[str] = []
                if isinstance(content, list):
                    for block in content:
                        if not isinstance(block, dict):
                            continue
                        if block.get("type") != "toolCall":
                            continue
                        tool_name = block.get("name") or "unknown"
                        tools.append(tool_name)
                        args = parse_args_field(block.get("arguments"))
                        file_path = path_from_tool(tool_name, args)
                        if file_path:
                            files[file_path] += 1
                stop = message.get("stopReason")
                if stop in {"error", "aborted", "length"}:
                    err = message.get("errorMessage") or stop
                    ending_signals.append(str(err))
                if text:
                    lower = text.lower()
                    if any(
                        s in lower
                        for s in (
                            "rate limit",
                            "usage limit",
                            "quota",
                            "context length",
                            "out of",
                        )
                    ):
                        ending_signals.append(text.strip())
                if text or tools:
                    turns.append(
                        {
                            "role": "assistant",
                            "text": text,
                            "tools": tools,
                            "timestamp": timestamp,
                            "stopReason": stop,
                        }
                    )
            # toolResult roles are skipped — files come from toolCall args

    if not meta["session_id"]:
        # Fallback: filename timestamp_uuid
        stem = path.stem
        if "_" in stem:
            meta["session_id"] = stem.split("_", 1)[1]
        else:
            meta["session_id"] = stem

    return {
        "meta": meta,
        "turns": turns,
        "files": files,
        "skills": skills,
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
    lines.append("# Pi session brief")
    lines.append(f"session_id: {meta['session_id']}")
    lines.append(f"path: {path}")
    lines.append(f"mtime: {mtime}")
    if meta.get("name"):
        lines.append(f"name: {meta['name']}")
    if meta.get("cwd"):
        lines.append(f"cwd: {meta['cwd']}")
    if meta.get("provider") or meta.get("model"):
        model = "/".join(
            p for p in (meta.get("provider"), meta.get("model")) if p
        )
        lines.append(f"model: {model}")
    if meta.get("thinking_level"):
        lines.append(f"thinking_level: {meta['thinking_level']}")

    # last user direction as last_prompt
    last_user = next(
        (t for t in reversed(brief["turns"]) if t["role"] == "user"), None
    )
    if last_user:
        lines.append(f"last_prompt: {truncate(last_user['text'], 500)}")

    skills = brief["skills"]
    if skills:
        seen: set[str] = set()
        ordered: list[str] = []
        for skill in skills:
            if skill not in seen:
                seen.add(skill)
                ordered.append(skill)
        lines.append("")
        lines.append("## Skills invoked")
        for skill in ordered:
            lines.append(f"- {skill}")

    files: Counter[str] = brief["files"]
    if files:
        lines.append("")
        lines.append("## Files in play")
        for file_path, count in files.most_common(40):
            lines.append(f"- {file_path} ({count})")

    turns = brief["turns"]
    goal_turns = [t for t in turns if t["role"] == "user"][:3]
    if goal_turns:
        lines.append("")
        lines.append("## Opening goal")
        for turn in goal_turns:
            lines.append("")
            lines.append("### User")
            lines.append(truncate(turn["text"], 1200))

    def is_noise(t: dict) -> bool:
        text = (t.get("text") or "").lower()
        return t["role"] == "assistant" and any(
            s in text for s in ("rate limit", "usage limit")
        )

    indexed = [
        (i, t)
        for i, t in enumerate(turns)
        if (t.get("text") or "").strip() and not is_noise(t)
    ]
    last_users = [pair for pair in indexed if pair[1]["role"] == "user"][-4:]
    last_assts = [pair for pair in indexed if pair[1]["role"] == "assistant"][-8:]
    recent_map = {i: t for i, t in last_users + last_assts}
    recent = [recent_map[i] for i in sorted(recent_map)]
    if recent:
        lines.append("")
        lines.append("## Recent turns")
        for turn in recent:
            lines.append("")
            role = "User" if turn["role"] == "user" else "Assistant"
            tools = turn.get("tools") or []
            tool_note = f" (tools: {', '.join(tools)})" if tools else ""
            lines.append(f"### {role}{tool_note}")
            text = turn.get("text") or ""
            if text.strip():
                limit = 1500 if turn["role"] == "user" else 2000
                lines.append(truncate(text, limit))
            elif tools:
                lines.append(f"[{', '.join(tools)}]")

    endings = brief["ending_signals"]
    lines.append("")
    lines.append("## Ending")
    if endings:
        lines.append(truncate(endings[-1], 400))
    else:
        last = next(
            (
                t
                for t in reversed(turns)
                if t["role"] == "assistant" and (t.get("text") or "").strip()
            ),
            None,
        )
        if last:
            stop = last.get("stopReason")
            prefix = f"[{stop}] " if stop and stop != "stop" else ""
            lines.append(prefix + truncate(last["text"], 800))
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
        description="Extract a resume brief from a Pi session."
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
        help="Pi session UUID (full or partial); omit for latest session for --cwd",
    )
    parser.add_argument(
        "--jsonl",
        default=None,
        help="Explicit path to a session JSONL (skips resolution)",
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

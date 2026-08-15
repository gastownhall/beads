#!/usr/bin/env python3
"""Extract a resume brief from a Claude Code session JSONL transcript."""

from __future__ import annotations

import argparse
import json
import re
import sys
from collections import Counter
from datetime import datetime
from pathlib import Path


def encode_cwd(cwd: str) -> str:
    return cwd.replace("/", "-")


def resolve_session(cwd: str, session_id: str | None) -> Path:
    root = Path.home() / ".claude" / "projects"
    if session_id:
        sid = session_id.strip()
        hits = sorted(root.glob(f"*/{sid}.jsonl"))
        if not hits:
            hits = sorted(root.rglob(f"{sid}.jsonl"))
        if not hits:
            raise SystemExit(f"No Claude session JSONL found for id: {sid}")
        return hits[0]

    project = root / encode_cwd(cwd)
    if not project.is_dir():
        raise SystemExit(
            f"No Claude project directory for cwd {cwd!r} (looked for {project})"
        )
    files = list(project.glob("*.jsonl"))
    if not files:
        raise SystemExit(f"No Claude session JSONL files in {project}")
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


def is_tool_result_only(content) -> bool:
    if not isinstance(content, list):
        return False
    has_tool_result = False
    has_user_text = False
    for block in content:
        if not isinstance(block, dict):
            continue
        if block.get("type") == "tool_result":
            has_tool_result = True
        elif block.get("type") == "text" and (block.get("text") or "").strip():
            has_user_text = True
    return has_tool_result and not has_user_text


def clean_user_text(text: str) -> str:
    text = re.sub(
        r"<command-message>.*?</command-message>\s*", "", text, flags=re.S
    )
    text = re.sub(
        r"<command-name>\s*(.*?)\s*</command-name>",
        r"[skill] \1",
        text,
        flags=re.S,
    )
    text = re.sub(
        r"<command-args>\s*(.*?)\s*</command-args>",
        r"[args] \1",
        text,
        flags=re.S,
    )
    text = re.sub(
        r"<task-notification>.*?</task-notification>",
        "[task-notification]",
        text,
        flags=re.S,
    )
    # Drop full skill-body injections; keep the invocation line above.
    if text.startswith("Base directory for this skill:"):
        return ""
    return text.strip()


def truncate(text: str, limit: int) -> str:
    text = text.strip()
    if len(text) <= limit:
        return text
    return text[: limit - 1].rstrip() + "…"


def path_from_tool(name: str, inp: dict) -> str | None:
    for key in ("file_path", "path", "filePath", "filename"):
        value = inp.get(key)
        if isinstance(value, str) and value:
            return value
    if name == "Bash":
        return None
    return None


def parse_session(path: Path) -> dict:
    meta = {
        "session_id": path.stem,
        "title": None,
        "cwd": None,
        "branch": None,
        "last_prompt": None,
        "version": None,
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
            if kind == "ai-title":
                meta["title"] = obj.get("aiTitle") or meta["title"]
            elif kind == "last-prompt":
                meta["last_prompt"] = obj.get("lastPrompt") or meta["last_prompt"]
            elif kind in {"user", "assistant"}:
                if obj.get("cwd"):
                    meta["cwd"] = obj["cwd"]
                if obj.get("gitBranch"):
                    meta["branch"] = obj["gitBranch"]
                if obj.get("version"):
                    meta["version"] = obj["version"]
                if obj.get("sessionId"):
                    meta["session_id"] = obj["sessionId"]

            if kind == "user":
                message = obj.get("message", {})
                content = (
                    message.get("content") if isinstance(message, dict) else message
                )
                if is_tool_result_only(content):
                    continue
                text = clean_user_text(extract_text(content))
                if not text:
                    continue
                for match in re.finditer(r"\[skill\]\s*(\S+)", text):
                    skills.append(match.group(1))
                turns.append(
                    {
                        "role": "user",
                        "text": text,
                        "timestamp": obj.get("timestamp"),
                    }
                )
            elif kind == "assistant":
                message = obj.get("message", {})
                content = (
                    message.get("content") if isinstance(message, dict) else []
                )
                text = extract_text(content)
                tools: list[str] = []
                if isinstance(content, list):
                    for block in content:
                        if not isinstance(block, dict):
                            continue
                        if block.get("type") != "tool_use":
                            continue
                        tool_name = block.get("name") or "unknown"
                        tools.append(tool_name)
                        inp = block.get("input") or {}
                        if isinstance(inp, dict):
                            file_path = path_from_tool(tool_name, inp)
                            if file_path:
                                files[file_path] += 1
                if text:
                    lower = text.lower()
                    if "session limit" in lower or "hit your limit" in lower:
                        ending_signals.append(text.strip())
                    elif "usage limit" in lower:
                        ending_signals.append(text.strip())
                if text or tools:
                    turns.append(
                        {
                            "role": "assistant",
                            "text": text,
                            "tools": tools,
                            "timestamp": obj.get("timestamp"),
                        }
                    )

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
    lines.append("# Claude session brief")
    lines.append(f"session_id: {meta['session_id']}")
    lines.append(f"path: {path}")
    lines.append(f"mtime: {mtime}")
    if meta.get("title"):
        lines.append(f"title: {meta['title']}")
    if meta.get("cwd"):
        lines.append(f"cwd: {meta['cwd']}")
    if meta.get("branch"):
        lines.append(f"git_branch: {meta['branch']}")
    if meta.get("version"):
        lines.append(f"claude_version: {meta['version']}")
    if meta.get("last_prompt"):
        lines.append(f"last_prompt: {truncate(meta['last_prompt'], 500)}")

    skills = brief["skills"]
    if skills:
        # Preserve order, unique
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

    # Keep last user directions AND last agent progress, merged in order.
    # A plain "last N turns" window is almost all assistant noise on long sessions.
    def is_limit_text(t: dict) -> bool:
        return t["role"] == "assistant" and "session limit" in (
            t.get("text") or ""
        ).lower()

    indexed = [
        (i, t)
        for i, t in enumerate(turns)
        if (t.get("text") or "").strip() and not is_limit_text(t)
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
            lines.append(truncate(last["text"], 800))
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
        description="Extract a resume brief from a Claude Code session."
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
        help="Claude session UUID; omit to use the latest session for --cwd",
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

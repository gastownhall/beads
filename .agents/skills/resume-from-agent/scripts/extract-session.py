#!/usr/bin/env python3
"""Extract a resume brief from any known coding-agent session store.

Cross-agent discovery: with no agent filter, pick the latest session whose cwd
matches. With --list, print ranked candidates. Adapters cover hermes, dirac,
goose, cursor, gemini, agy/antigravity, and the four first-class harnesses
(claude, codex, opencode, pi) — reusing sibling extractors when present.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import sqlite3
import subprocess
import sys
from collections import Counter, deque
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Callable, Iterable, Iterator


# ---------------------------------------------------------------------------
# Shared helpers
# ---------------------------------------------------------------------------


def home() -> Path:
    return Path.home()


def resolve_path(path: str | Path) -> str:
    try:
        return str(Path(path).expanduser().resolve())
    except OSError:
        return str(Path(path).expanduser())


def same_cwd(a: str | None, b: str | None) -> bool:
    if not a or not b:
        return False
    return resolve_path(a) == resolve_path(b)


def truncate(text: str, limit: int) -> str:
    text = (text or "").strip()
    if len(text) <= limit:
        return text
    return text[: limit - 1].rstrip() + "…"


def iso_mtime(path: Path) -> str:
    return datetime.fromtimestamp(path.stat().st_mtime).isoformat(timespec="seconds")


def epoch_to_iso(value: float | int | None) -> str | None:
    if value is None:
        return None
    try:
        v = float(value)
    except (TypeError, ValueError):
        return None
    if v > 10_000_000_000:  # ms
        v = v / 1000.0
    try:
        return datetime.fromtimestamp(v).isoformat(timespec="seconds")
    except (OverflowError, OSError, ValueError):
        return None


def extract_text(content: Any) -> str:
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        parts: list[str] = []
        for block in content:
            if isinstance(block, str):
                if block.strip():
                    parts.append(block)
                continue
            if not isinstance(block, dict):
                continue
            if block.get("type") == "text" or "text" in block:
                text = block.get("text") or ""
                if text:
                    parts.append(str(text))
            elif block.get("type") == "output_text":
                text = block.get("text") or ""
                if text:
                    parts.append(str(text))
        return "\n".join(parts)
    return ""


def strip_session_context(text: str) -> str:
    text = re.sub(
        r"<session_context>.*?</session_context>\s*",
        "",
        text,
        flags=re.S | re.I,
    )
    text = re.sub(
        r"<environment_details>.*?</environment_details>\s*",
        "",
        text,
        flags=re.S | re.I,
    )
    text = re.sub(r"<task>\s*(.*?)\s*</task>", r"\1", text, flags=re.S | re.I)
    return text.strip()


def clean_skill_injections(text: str) -> tuple[str, list[str]]:
    skills: list[str] = []
    skills += re.findall(r'<skill\s+name="([^"]+)"', text, flags=re.I)
    skills += re.findall(r"\[\$([^\]]+)\]", text)
    skills += re.findall(r"<command-name>\s*(.*?)\s*</command-name>", text, flags=re.S)
    skills += re.findall(
        r"Skill Name:\s*([^\n<]+)", text
    )
    stripped = re.sub(r"<skill\b[^>]*>.*?</skill>", "", text, flags=re.S | re.I)
    stripped = re.sub(
        r"<manually_attached_skills>.*?</manually_attached_skills>",
        "",
        stripped,
        flags=re.S | re.I,
    )
    stripped = re.sub(
        r"<command-message>.*?</command-message>\s*",
        "",
        stripped,
        flags=re.S,
    )
    stripped = re.sub(
        r"<command-name>\s*(.*?)\s*</command-name>",
        r"[skill] \1",
        stripped,
        flags=re.S,
    )
    stripped = re.sub(
        r"<command-args>\s*(.*?)\s*</command-args>",
        r"[args] \1",
        stripped,
        flags=re.S,
    )
    stripped = strip_session_context(stripped)
    if stripped.startswith("Base directory for this skill:"):
        stripped = ""
    skills = [s.strip() for s in skills if s and s.strip()]
    if skills:
        label = "[skill] " + ", ".join(dict.fromkeys(skills))
        stripped = f"{label}\n{stripped}".strip() if stripped else label
    return stripped.strip(), skills


def path_from_mapping(name: str, inp: dict) -> str | None:
    if not isinstance(inp, dict):
        return None
    # Skip pure search roots.
    if name.lower() in {
        "glob",
        "grep",
        "bash",
        "shell",
        "terminal",
        "run_terminal_cmd",
        "execute_bash",
    }:
        for key in ("file_path", "filePath", "target_file", "path"):
            value = inp.get(key)
            if isinstance(value, str) and value.strip() and len(value) < 512:
                # Only count if it looks like a file, not a shell command.
                if " " not in value.strip() or "/" in value:
                    if not value.strip().startswith("-"):
                        return value.strip()
        return None
    for key in (
        "file_path",
        "filePath",
        "path",
        "target_file",
        "target",
        "filename",
    ):
        value = inp.get(key)
        if isinstance(value, str) and value.strip() and len(value) < 512:
            return value.strip()
    return None


def parse_json_maybe(raw: Any) -> Any:
    if isinstance(raw, (dict, list)):
        return raw
    if isinstance(raw, str) and raw.strip():
        try:
            return json.loads(raw)
        except json.JSONDecodeError:
            return raw
    return raw


def open_sqlite_ro(path: Path) -> sqlite3.Connection:
    if not path.is_file():
        raise FileNotFoundError(str(path))
    uri = f"file:{path}?mode=ro"
    conn = sqlite3.connect(uri, uri=True, timeout=15.0)
    conn.row_factory = sqlite3.Row
    conn.execute("PRAGMA busy_timeout = 15000")
    conn.execute("PRAGMA query_only = ON")
    return conn


# ---------------------------------------------------------------------------
# Brief model
# ---------------------------------------------------------------------------


@dataclass
class Candidate:
    agent: str
    session_id: str
    cwd: str | None
    mtime: float
    title: str | None = None
    path: str | None = None
    extra: dict[str, Any] = field(default_factory=dict)

    def sort_key(self) -> float:
        return self.mtime


@dataclass
class Brief:
    agent: str
    session_id: str
    path: str | None = None
    cwd: str | None = None
    title: str | None = None
    model: str | None = None
    git_branch: str | None = None
    mtime_label: str | None = None
    last_prompt: str | None = None
    skills: list[str] = field(default_factory=list)
    files: Counter[str] = field(default_factory=Counter)
    tools: Counter[str] = field(default_factory=Counter)
    todos: list[str] = field(default_factory=list)
    opening_users: list[str] = field(default_factory=list)
    recent_turns: list[dict[str, Any]] = field(default_factory=list)
    ending: str | None = None
    notes: list[str] = field(default_factory=list)
    extras: dict[str, str] = field(default_factory=dict)


def render_brief(brief: Brief) -> str:
    lines: list[str] = []
    lines.append(f"# {brief.agent} session brief")
    lines.append(f"agent: {brief.agent}")
    lines.append(f"session_id: {brief.session_id}")
    if brief.path:
        lines.append(f"path: {brief.path}")
    if brief.mtime_label:
        lines.append(f"mtime: {brief.mtime_label}")
    if brief.title:
        lines.append(f"title: {brief.title}")
    if brief.cwd:
        lines.append(f"cwd: {brief.cwd}")
    if brief.git_branch:
        lines.append(f"git_branch: {brief.git_branch}")
    if brief.model:
        lines.append(f"model: {brief.model}")
    for key, value in brief.extras.items():
        lines.append(f"{key}: {value}")
    if brief.last_prompt:
        lines.append(f"last_prompt: {truncate(brief.last_prompt, 500)}")
    if brief.notes:
        lines.append("")
        lines.append("## Discovery notes")
        for note in brief.notes:
            lines.append(f"- {note}")
    if brief.skills:
        lines.append("")
        lines.append("## Skills invoked")
        for skill in dict.fromkeys(brief.skills):
            lines.append(f"- {skill}")
    if brief.todos:
        lines.append("")
        lines.append("## Todos")
        for todo in brief.todos:
            lines.append(f"- {todo}")
    if brief.tools:
        lines.append("")
        lines.append("## Tools used")
        for name, count in brief.tools.most_common(20):
            lines.append(f"- {name} ({count})")
    if brief.files:
        lines.append("")
        lines.append("## Files in play")
        for path, count in brief.files.most_common(40):
            lines.append(f"- {path} ({count})")
    if brief.opening_users:
        lines.append("")
        lines.append("## Opening goal")
        for text in brief.opening_users[:3]:
            lines.append("")
            lines.append("### User")
            lines.append(truncate(text, 1200))
    if brief.recent_turns:
        lines.append("")
        lines.append("## Recent turns")
        for turn in brief.recent_turns:
            lines.append("")
            role = turn.get("role") or "turn"
            tools = turn.get("tools") or []
            tool_note = f" (tools: {', '.join(tools)})" if tools else ""
            label = "User" if role == "user" else "Assistant"
            lines.append(f"### {label}{tool_note}")
            text = turn.get("text") or ""
            if text.strip():
                limit = 1500 if role == "user" else 2000
                lines.append(truncate(text, limit))
            elif tools:
                lines.append(f"[{', '.join(tools)}]")
    lines.append("")
    lines.append("## Ending")
    lines.append(
        truncate(brief.ending, 800)
        if brief.ending
        else "(no clear ending text — session may have stopped mid-tool)"
    )
    lines.append("")
    lines.append("## Resume instruction")
    lines.append(
        "Continue the interrupted work from this brief. "
        "Do not summarise and wait. Ground against the live workspace, then act."
    )
    return "\n".join(lines) + "\n"


def brief_from_turns(
    *,
    agent: str,
    session_id: str,
    path: str | None,
    cwd: str | None,
    title: str | None,
    model: str | None,
    git_branch: str | None,
    mtime_label: str | None,
    turns: list[dict[str, Any]],
    files: Counter[str],
    skills: list[str],
    tools: Counter[str] | None = None,
    ending_signals: list[str] | None = None,
    todos: list[str] | None = None,
    notes: list[str] | None = None,
    extras: dict[str, str] | None = None,
) -> Brief:
    user_texts = [
        t["text"]
        for t in turns
        if t.get("role") == "user" and (t.get("text") or "").strip()
    ]
    # Recent: last users + last assistants, order preserved.
    indexed = [
        (i, t)
        for i, t in enumerate(turns)
        if (t.get("text") or "").strip() or t.get("tools")
    ]
    last_users = [p for p in indexed if p[1].get("role") == "user"][-4:]
    last_assts = [p for p in indexed if p[1].get("role") != "user"][-8:]
    recent_map = {i: t for i, t in last_users + last_assts}
    recent = [recent_map[i] for i in sorted(recent_map)]

    ending = None
    if ending_signals:
        ending = str(ending_signals[-1])
    else:
        for t in reversed(turns):
            if t.get("role") != "user" and (t.get("text") or "").strip():
                ending = t["text"]
                break

    last_prompt = user_texts[-1] if user_texts else None
    tool_counter = tools or Counter()
    if not tool_counter:
        for t in turns:
            for name in t.get("tools") or []:
                tool_counter[name] += 1

    return Brief(
        agent=agent,
        session_id=session_id,
        path=path,
        cwd=cwd,
        title=title,
        model=model,
        git_branch=git_branch,
        mtime_label=mtime_label,
        last_prompt=last_prompt,
        skills=skills,
        files=files,
        tools=tool_counter,
        todos=todos or [],
        opening_users=user_texts[:3],
        recent_turns=recent,
        ending=ending,
        notes=notes or [],
        extras=extras or {},
    )


# ---------------------------------------------------------------------------
# Adapter protocol
# ---------------------------------------------------------------------------


DiscoverFn = Callable[[str, str | None], list[Candidate]]
ExtractFn = Callable[[Candidate], Brief]


@dataclass
class Adapter:
    name: str
    aliases: tuple[str, ...]
    discover: DiscoverFn
    extract: ExtractFn
    roots: tuple[str, ...]  # human-readable probe paths for errors


ADAPTERS: dict[str, Adapter] = {}


def register(adapter: Adapter) -> Adapter:
    ADAPTERS[adapter.name] = adapter
    return adapter


def agent_aliases() -> dict[str, str]:
    mapping: dict[str, str] = {}
    for name, adapter in ADAPTERS.items():
        mapping[name] = name
        for alias in adapter.aliases:
            mapping[alias.lower()] = name
    return mapping


# ---------------------------------------------------------------------------
# Hermes
# ---------------------------------------------------------------------------


def hermes_db() -> Path:
    return home() / ".hermes" / "state.db"


def discover_hermes(cwd: str, session_id: str | None) -> list[Candidate]:
    db = hermes_db()
    if not db.is_file():
        return []
    conn = open_sqlite_ro(db)
    try:
        if session_id:
            sid = session_id.strip()
            rows = conn.execute(
                """
                SELECT id, title, cwd, model, started_at, ended_at, message_count,
                       git_branch
                FROM sessions
                WHERE id = ? OR id LIKE ? OR ifnull(title,'') = ?
                   OR lower(ifnull(title,'')) LIKE lower(?)
                ORDER BY COALESCE(ended_at, started_at) DESC
                LIMIT 10
                """,
                (sid, f"%{sid}%", sid, f"%{sid}%"),
            ).fetchall()
        else:
            cwd_r = resolve_path(cwd)
            rows = conn.execute(
                """
                SELECT id, title, cwd, model, started_at, ended_at, message_count,
                       git_branch
                FROM sessions
                WHERE cwd = ? OR cwd = ?
                ORDER BY COALESCE(ended_at, started_at) DESC
                LIMIT 20
                """,
                (cwd, cwd_r),
            ).fetchall()
        out: list[Candidate] = []
        for row in rows:
            ts = row["ended_at"] or row["started_at"] or 0
            out.append(
                Candidate(
                    agent="hermes",
                    session_id=row["id"],
                    cwd=row["cwd"],
                    mtime=float(ts or 0),
                    title=row["title"],
                    path=str(db),
                    extra={
                        "model": row["model"],
                        "git_branch": row["git_branch"],
                        "message_count": row["message_count"],
                    },
                )
            )
        return out
    finally:
        conn.close()


def extract_hermes(cand: Candidate) -> Brief:
    conn = open_sqlite_ro(hermes_db())
    try:
        session = conn.execute(
            "SELECT * FROM sessions WHERE id = ?", (cand.session_id,)
        ).fetchone()
        if not session:
            raise SystemExit(f"Hermes session not found: {cand.session_id}")
        rows = conn.execute(
            """
            SELECT role, content, tool_calls, tool_name, timestamp, finish_reason
            FROM messages
            WHERE session_id = ? AND ifnull(active, 1) = 1
            ORDER BY timestamp ASC, id ASC
            """,
            (cand.session_id,),
        ).fetchall()
    finally:
        conn.close()

    turns: list[dict[str, Any]] = []
    files: Counter[str] = Counter()
    skills: list[str] = []
    tools: Counter[str] = Counter()
    endings: list[str] = []

    for row in rows:
        role = row["role"] or "unknown"
        content = row["content"] or ""
        tool_name = row["tool_name"]
        tool_calls_raw = row["tool_calls"]

        if role == "tool":
            if tool_name:
                tools[tool_name] += 1
            continue

        if role == "user":
            text, found = clean_skill_injections(str(content))
            skills.extend(found)
            if text:
                turns.append({"role": "user", "text": text})
            continue

        # assistant
        tool_names: list[str] = []
        if tool_calls_raw:
            parsed = parse_json_maybe(tool_calls_raw)
            if isinstance(parsed, list):
                for call in parsed:
                    if not isinstance(call, dict):
                        continue
                    fn = call.get("function") if isinstance(call.get("function"), dict) else call
                    name = (
                        (fn or {}).get("name")
                        or call.get("name")
                        or tool_name
                        or "tool"
                    )
                    tool_names.append(str(name))
                    tools[str(name)] += 1
                    args_raw = (fn or {}).get("arguments") or call.get("arguments") or {}
                    args = parse_json_maybe(args_raw)
                    if isinstance(args, dict):
                        path = path_from_mapping(str(name), args)
                        if path:
                            files[path] += 1
                    elif isinstance(args, str):
                        for match in re.finditer(
                            r'"(?:path|file_path|filePath)"\s*:\s*"([^"]+)"', args
                        ):
                            files[match.group(1)] += 1
        text = str(content or "").strip()
        if row["finish_reason"] and row["finish_reason"] not in {
            "stop",
            "end_turn",
            "tool_calls",
            None,
        }:
            endings.append(f"{row['finish_reason']}: {truncate(text, 200)}")
        if text or tool_names:
            turns.append(
                {"role": "assistant", "text": text, "tools": tool_names}
            )

    if session["end_reason"]:
        endings.append(f"end_reason: {session['end_reason']}")

    mtime = epoch_to_iso(session["ended_at"] or session["started_at"])
    return brief_from_turns(
        agent="hermes",
        session_id=session["id"],
        path=str(hermes_db()),
        cwd=session["cwd"],
        title=session["title"],
        model=session["model"],
        git_branch=session["git_branch"],
        mtime_label=mtime,
        turns=turns,
        files=files,
        skills=skills,
        tools=tools,
        ending_signals=endings,
        extras={
            k: str(v)
            for k, v in {
                "source": session["source"] if "source" in session.keys() else None,
                "message_count": session["message_count"],
            }.items()
            if v is not None
        },
    )


register(
    Adapter(
        name="hermes",
        aliases=("hermes-agent",),
        discover=discover_hermes,
        extract=extract_hermes,
        roots=("~/.hermes/state.db",),
    )
)


# ---------------------------------------------------------------------------
# Dirac
# ---------------------------------------------------------------------------


def dirac_root() -> Path:
    return home() / ".dirac" / "data"


def discover_dirac(cwd: str, session_id: str | None) -> list[Candidate]:
    history_path = dirac_root() / "state" / "taskHistory.json"
    if not history_path.is_file():
        return []
    try:
        history = json.loads(history_path.read_text())
    except json.JSONDecodeError:
        return []
    if not isinstance(history, list):
        return []

    cwd_r = resolve_path(cwd)
    out: list[Candidate] = []
    for item in history:
        if not isinstance(item, dict):
            continue
        tid = str(item.get("id") or "")
        if not tid:
            continue
        item_cwd = item.get("cwdOnTaskInitialization") or item.get(
            "workspaceRootPath"
        )
        if session_id:
            sid = session_id.strip()
            ulid = str(item.get("ulid") or "")
            task = str(item.get("task") or "")
            if not (
                tid == sid
                or tid.startswith(sid)
                or sid in tid
                or ulid == sid
                or sid in ulid
                or (sid.lower() in task.lower() if task else False)
            ):
                continue
        else:
            if not item_cwd or resolve_path(str(item_cwd)) not in {cwd_r, cwd}:
                # also accept exact string match without resolve failures
                if str(item_cwd) not in {cwd, cwd_r}:
                    continue
        task_dir = dirac_root() / "tasks" / tid
        mtime = float(item.get("ts") or 0) / (
            1000.0 if float(item.get("ts") or 0) > 10_000_000_000 else 1.0
        )
        if task_dir.is_dir():
            try:
                mtime = max(mtime, task_dir.stat().st_mtime)
            except OSError:
                pass
        out.append(
            Candidate(
                agent="dirac",
                session_id=tid,
                cwd=str(item_cwd) if item_cwd else None,
                mtime=mtime,
                title=str(item.get("task") or "")[:200] or None,
                path=str(task_dir) if task_dir.is_dir() else str(history_path),
                extra={"ulid": item.get("ulid"), "modelId": item.get("modelId")},
            )
        )
    return out


def extract_dirac(cand: Candidate) -> Brief:
    task_dir = dirac_root() / "tasks" / cand.session_id
    if not task_dir.is_dir():
        raise SystemExit(f"Dirac task directory missing: {task_dir}")

    meta: dict = {}
    meta_path = task_dir / "task_metadata.json"
    if meta_path.is_file():
        try:
            meta = json.loads(meta_path.read_text())
        except json.JSONDecodeError:
            meta = {}

    ui: list = []
    ui_path = task_dir / "ui_messages.json"
    if ui_path.is_file():
        try:
            loaded = json.loads(ui_path.read_text())
            if isinstance(loaded, list):
                ui = loaded
        except json.JSONDecodeError:
            ui = []

    api: list = []
    api_path = task_dir / "api_conversation_history.json"
    if api_path.is_file():
        try:
            loaded = json.loads(api_path.read_text())
            if isinstance(loaded, list):
                api = loaded
        except json.JSONDecodeError:
            api = []

    files: Counter[str] = Counter()
    for entry in meta.get("files_in_context") or []:
        if isinstance(entry, dict) and entry.get("path"):
            files[str(entry["path"])] += 1

    turns: list[dict[str, Any]] = []
    skills: list[str] = []
    endings: list[str] = []
    model = None
    title = cand.title

    for msg in ui:
        if not isinstance(msg, dict):
            continue
        if msg.get("say") == "task" and msg.get("text"):
            title = str(msg["text"])
            turns.append({"role": "user", "text": str(msg["text"])})
        if msg.get("ask") == "resume_task":
            endings.append("dirac ask: resume_task")
        if msg.get("type") == "say" and msg.get("say") in {
            "error",
            "api_req_started",
        }:
            if msg.get("text"):
                endings.append(str(msg["text"])[:400])
        mi = msg.get("modelInfo") or {}
        if isinstance(mi, dict) and mi.get("modelId"):
            model = (
                f"{mi.get('providerId') + '/' if mi.get('providerId') else ''}"
                f"{mi.get('modelId')}"
            )

    # Prefer API history for assistant text + tools when present.
    if api:
        turns = []
        for msg in api:
            if not isinstance(msg, dict):
                continue
            role = msg.get("role")
            text = strip_session_context(extract_text(msg.get("content")))
            text, found = clean_skill_injections(text)
            skills.extend(found)
            tool_names: list[str] = []
            content = msg.get("content")
            if isinstance(content, list):
                for block in content:
                    if not isinstance(block, dict):
                        continue
                    if block.get("type") in {"tool_use", "toolCall"}:
                        name = block.get("name") or "tool"
                        tool_names.append(str(name))
                        inp = block.get("input") or block.get("arguments") or {}
                        if isinstance(inp, dict):
                            path = path_from_mapping(str(name), inp)
                            if path:
                                files[path] += 1
            if role == "user" and text:
                # Skip pure tool-result user mirrors when huge and tagged
                if text.startswith("[") and "Result:" in text[:80]:
                    continue
                turns.append({"role": "user", "text": text})
            elif role == "assistant" and (text or tool_names):
                if "interrupted" in text.lower():
                    endings.append(text)
                turns.append(
                    {"role": "assistant", "text": text, "tools": tool_names}
                )

    mtime_label = iso_mtime(task_dir)
    return brief_from_turns(
        agent="dirac",
        session_id=cand.session_id,
        path=str(task_dir),
        cwd=cand.cwd,
        title=title,
        model=model or (str(cand.extra.get("modelId")) if cand.extra.get("modelId") else None),
        git_branch=None,
        mtime_label=mtime_label,
        turns=turns,
        files=files,
        skills=skills,
        ending_signals=endings,
        extras={"ulid": str(cand.extra["ulid"])} if cand.extra.get("ulid") else {},
    )


register(
    Adapter(
        name="dirac",
        aliases=("dirac-cli",),
        discover=discover_dirac,
        extract=extract_dirac,
        roots=("~/.dirac/data/tasks", "~/.dirac/data/state/taskHistory.json"),
    )
)


# ---------------------------------------------------------------------------
# Goose
# ---------------------------------------------------------------------------


def goose_sessions_dir() -> Path:
    return home() / ".local" / "share" / "goose" / "sessions"


def _goose_read_meta(path: Path) -> dict:
    try:
        with path.open() as fh:
            line = fh.readline()
        if not line.strip():
            return {}
        obj = json.loads(line)
        if isinstance(obj, dict) and "working_dir" in obj:
            return obj
        if isinstance(obj, dict) and obj.get("role"):
            # older format without meta line
            return {"working_dir": None, "_no_meta": True}
    except (OSError, json.JSONDecodeError):
        return {}
    return {}


def discover_goose(cwd: str, session_id: str | None) -> list[Candidate]:
    root = goose_sessions_dir()
    if not root.is_dir():
        return []
    cwd_r = resolve_path(cwd)
    out: list[Candidate] = []
    for path in root.glob("*.jsonl"):
        if path.stat().st_size == 0:
            continue
        meta = _goose_read_meta(path)
        sid = path.stem
        if session_id:
            sid_q = session_id.strip()
            desc = str(meta.get("description") or "")
            if not (
                sid == sid_q
                or sid_q in sid
                or (sid_q.lower() in desc.lower() if desc else False)
            ):
                continue
        else:
            wd = meta.get("working_dir")
            if not wd or resolve_path(str(wd)) not in {cwd_r, cwd}:
                if str(wd) not in {cwd, cwd_r}:
                    continue
        out.append(
            Candidate(
                agent="goose",
                session_id=sid,
                cwd=str(meta.get("working_dir")) if meta.get("working_dir") else None,
                mtime=path.stat().st_mtime,
                title=str(meta.get("description") or "") or None,
                path=str(path),
                extra={"message_count": meta.get("message_count")},
            )
        )
    return out


def extract_goose(cand: Candidate) -> Brief:
    path = Path(cand.path or "")
    if not path.is_file():
        raise SystemExit(f"Goose session not found: {cand.path}")

    turns: list[dict[str, Any]] = []
    files: Counter[str] = Counter()
    skills: list[str] = []
    tools: Counter[str] = Counter()
    meta: dict = {}
    endings: list[str] = []

    with path.open() as fh:
        first = True
        for line in fh:
            line = line.strip()
            if not line:
                continue
            try:
                obj = json.loads(line)
            except json.JSONDecodeError:
                continue
            if first and isinstance(obj, dict) and "working_dir" in obj and "role" not in obj:
                meta = obj
                first = False
                continue
            first = False
            if not isinstance(obj, dict):
                continue
            role = obj.get("role")
            content = obj.get("content")
            if role == "user":
                # toolResponse often arrives as user role
                if isinstance(content, list) and any(
                    isinstance(b, dict) and b.get("type") == "toolResponse"
                    for b in content
                ):
                    continue
                text = extract_text(content)
                text, found = clean_skill_injections(text)
                skills.extend(found)
                if text:
                    turns.append({"role": "user", "text": text})
            elif role == "assistant":
                text_parts: list[str] = []
                tool_names: list[str] = []
                if isinstance(content, list):
                    for block in content:
                        if not isinstance(block, dict):
                            continue
                        btype = block.get("type")
                        if btype == "text":
                            t = block.get("text") or ""
                            if t.strip():
                                text_parts.append(str(t))
                        elif btype == "toolRequest":
                            tc = block.get("toolCall") or {}
                            value = tc.get("value") if isinstance(tc, dict) else None
                            if not isinstance(value, dict):
                                value = block.get("toolCall") if isinstance(block.get("toolCall"), dict) else {}
                            name = (
                                (value or {}).get("name")
                                or block.get("name")
                                or "tool"
                            )
                            tool_names.append(str(name))
                            tools[str(name)] += 1
                            args = (value or {}).get("arguments") or {}
                            if isinstance(args, dict):
                                pth = path_from_mapping(str(name), args)
                                if pth:
                                    files[pth] += 1
                text = "\n".join(text_parts)
                if text or tool_names:
                    turns.append(
                        {"role": "assistant", "text": text, "tools": tool_names}
                    )

    return brief_from_turns(
        agent="goose",
        session_id=cand.session_id,
        path=str(path),
        cwd=str(meta.get("working_dir") or cand.cwd or ""),
        title=str(meta.get("description") or cand.title or "") or None,
        model=None,
        git_branch=None,
        mtime_label=iso_mtime(path),
        turns=turns,
        files=files,
        skills=skills,
        tools=tools,
        ending_signals=endings,
        extras={
            k: str(v)
            for k, v in {
                "message_count": meta.get("message_count"),
                "total_tokens": meta.get("total_tokens"),
            }.items()
            if v is not None
        },
    )


register(
    Adapter(
        name="goose",
        aliases=(),
        discover=discover_goose,
        extract=extract_goose,
        roots=("~/.local/share/goose/sessions",),
    )
)


# ---------------------------------------------------------------------------
# Cursor
# ---------------------------------------------------------------------------


def cursor_projects_root() -> Path:
    return home() / ".cursor" / "projects"


def encode_cursor_cwd(cwd: str) -> str:
    # /Users/foo/bar -> Users-foo-bar
    return cwd.strip("/").replace("/", "-")


def discover_cursor(cwd: str, session_id: str | None) -> list[Candidate]:
    root = cursor_projects_root()
    if not root.is_dir():
        return []
    out: list[Candidate] = []
    cwd_r = resolve_path(cwd)
    project_names = {encode_cursor_cwd(cwd), encode_cursor_cwd(cwd_r)}

    def consider(jsonl: Path, project_name: str) -> None:
        if "subagents" in jsonl.parts:
            return
        sid = jsonl.stem
        if session_id:
            q = session_id.strip()
            if not (sid == q or q in sid or sid.startswith(q)):
                return
        # recover cwd from project name best-effort
        recovered = "/" + project_name.replace("-", "/")
        # On macOS Users-... is correct enough for display; matching already done.
        out.append(
            Candidate(
                agent="cursor",
                session_id=sid,
                cwd=cwd_r if project_name in project_names else recovered,
                mtime=jsonl.stat().st_mtime,
                title=None,
                path=str(jsonl),
            )
        )

    if session_id:
        for jsonl in root.rglob("*.jsonl"):
            consider(jsonl, jsonl.parts[len(root.parts)] if len(jsonl.parts) > len(root.parts) else "")
        return out

    for name in project_names:
        project = root / name
        if not project.is_dir():
            continue
        for jsonl in project.glob("agent-transcripts/*/*.jsonl"):
            consider(jsonl, name)
        for jsonl in project.glob("agent-transcripts/*/*/*.jsonl"):
            # nested but skip handled in consider
            consider(jsonl, name)
    return out


def extract_cursor(cand: Candidate) -> Brief:
    path = Path(cand.path or "")
    if not path.is_file():
        raise SystemExit(f"Cursor transcript not found: {cand.path}")

    turns: list[dict[str, Any]] = []
    files: Counter[str] = Counter()
    skills: list[str] = []
    tools: Counter[str] = Counter()
    endings: list[str] = []

    with path.open() as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            try:
                obj = json.loads(line)
            except json.JSONDecodeError:
                continue
            if obj.get("type") == "turn_ended":
                endings.append(f"turn_ended: {obj.get('status') or 'unknown'}")
                continue
            role = obj.get("role")
            message = obj.get("message") or {}
            content = message.get("content") if isinstance(message, dict) else None
            if role == "user":
                text = extract_text(content)
                text, found = clean_skill_injections(text)
                skills.extend(found)
                if text:
                    turns.append({"role": "user", "text": text})
            elif role == "assistant":
                text = extract_text(content)
                tool_names: list[str] = []
                if isinstance(content, list):
                    for block in content:
                        if not isinstance(block, dict):
                            continue
                        if block.get("type") != "tool_use":
                            continue
                        name = block.get("name") or "tool"
                        tool_names.append(str(name))
                        tools[str(name)] += 1
                        inp = block.get("input") or {}
                        if isinstance(inp, dict):
                            pth = path_from_mapping(str(name), inp)
                            if pth:
                                files[pth] += 1
                if text or tool_names:
                    turns.append(
                        {"role": "assistant", "text": text, "tools": tool_names}
                    )

    return brief_from_turns(
        agent="cursor",
        session_id=cand.session_id,
        path=str(path),
        cwd=cand.cwd,
        title=cand.title,
        model=None,
        git_branch=None,
        mtime_label=iso_mtime(path),
        turns=turns,
        files=files,
        skills=skills,
        tools=tools,
        ending_signals=endings,
    )


register(
    Adapter(
        name="cursor",
        aliases=("cursor-agent", "cursor-cli"),
        discover=discover_cursor,
        extract=extract_cursor,
        roots=("~/.cursor/projects",),
    )
)


# ---------------------------------------------------------------------------
# Gemini CLI
# ---------------------------------------------------------------------------


def gemini_tmp_root() -> Path:
    return home() / ".gemini" / "tmp"


def _gemini_project_root(tmp_dir: Path) -> str | None:
    marker = tmp_dir / ".project_root"
    if marker.is_file():
        return marker.read_text().strip() or None
    # history mirror
    hist = home() / ".gemini" / "history" / tmp_dir.name / ".project_root"
    if hist.is_file():
        return hist.read_text().strip() or None
    return None


def discover_gemini(cwd: str, session_id: str | None) -> list[Candidate]:
    root = gemini_tmp_root()
    if not root.is_dir():
        return []
    cwd_r = resolve_path(cwd)
    out: list[Candidate] = []
    for tmp_dir in root.iterdir():
        if not tmp_dir.is_dir():
            continue
        chats = tmp_dir / "chats"
        if not chats.is_dir():
            continue
        project_cwd = _gemini_project_root(tmp_dir)
        if session_id is None:
            if not project_cwd or resolve_path(project_cwd) not in {cwd_r, cwd}:
                if project_cwd not in {cwd, cwd_r}:
                    continue
        for path in list(chats.glob("session-*.jsonl")) + list(
            chats.glob("session-*.json")
        ):
            sid = path.stem
            # session-2026-06-04T17-29-17fecec4 -> prefer trailing token
            short = sid.split("-")[-1] if "-" in sid else sid
            if session_id:
                q = session_id.strip()
                if not (
                    q in sid
                    or q in short
                    or sid.endswith(q)
                    or short.startswith(q)
                ):
                    # also allow full uuid inside file header later; filename first
                    if q not in path.name:
                        continue
            out.append(
                Candidate(
                    agent="gemini",
                    session_id=short,
                    cwd=project_cwd,
                    mtime=path.stat().st_mtime,
                    title=None,
                    path=str(path),
                    extra={"tmp_dir": tmp_dir.name},
                )
            )
    return out


def _iter_gemini_messages(path: Path) -> tuple[dict, list[dict]]:
    meta: dict = {}
    messages: list[dict] = []
    if path.suffix == ".json" and path.stat().st_size < 50_000_000:
        try:
            data = json.loads(path.read_text())
        except json.JSONDecodeError:
            data = None
        if isinstance(data, dict):
            meta = {
                k: data.get(k)
                for k in ("sessionId", "projectHash", "startTime", "lastUpdated")
                if k in data
            }
            if isinstance(data.get("messages"), list):
                return meta, list(data["messages"])
        if isinstance(data, list):
            return meta, [m for m in data if isinstance(m, dict)]

    with path.open() as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            try:
                obj = json.loads(line)
            except json.JSONDecodeError:
                continue
            if not isinstance(obj, dict):
                continue
            if "sessionId" in obj and "type" not in obj:
                meta = obj
                continue
            if "$set" in obj and isinstance(obj["$set"], dict):
                s = obj["$set"]
                if isinstance(s.get("messages"), list):
                    messages = list(s["messages"])
                if "lastUpdated" in s:
                    meta["lastUpdated"] = s["lastUpdated"]
                continue
            if obj.get("type") in {
                "user",
                "gemini",
                "model",
                "assistant",
                "info",
                "error",
            }:
                messages.append(obj)
    return meta, messages


def extract_gemini(cand: Candidate) -> Brief:
    path = Path(cand.path or "")
    if not path.is_file():
        raise SystemExit(f"Gemini session not found: {cand.path}")
    meta, messages = _iter_gemini_messages(path)
    session_id = str(meta.get("sessionId") or cand.session_id)

    turns: list[dict[str, Any]] = []
    files: Counter[str] = Counter()
    skills: list[str] = []
    tools: Counter[str] = Counter()
    endings: list[str] = []
    model = None
    title = None

    for msg in messages:
        mtype = msg.get("type")
        if mtype == "info":
            continue
        if mtype == "error":
            endings.append(extract_text(msg.get("content")) or "error")
            continue
        if mtype == "user":
            text = extract_text(msg.get("content"))
            text, found = clean_skill_injections(text)
            skills.extend(found)
            if text:
                turns.append({"role": "user", "text": text})
            continue
        if mtype in {"gemini", "model", "assistant"}:
            if msg.get("model"):
                model = str(msg["model"])
            text = extract_text(msg.get("content"))
            # thoughts as weak signal if no text
            if not text.strip() and msg.get("thoughts"):
                thoughts = msg["thoughts"]
                if isinstance(thoughts, list) and thoughts:
                    last = thoughts[-1]
                    if isinstance(last, dict):
                        text = str(
                            last.get("description") or last.get("subject") or ""
                        )
            tool_names: list[str] = []
            for call in msg.get("toolCalls") or []:
                if not isinstance(call, dict):
                    continue
                name = call.get("name") or "tool"
                tool_names.append(str(name))
                tools[str(name)] += 1
                if name == "update_topic":
                    args = call.get("args") or {}
                    if isinstance(args, dict) and args.get("title"):
                        title = str(args["title"])
                args = call.get("args") or call.get("arguments") or {}
                if isinstance(args, dict):
                    pth = path_from_mapping(str(name), args)
                    if pth:
                        files[pth] += 1
            if text or tool_names:
                turns.append(
                    {"role": "assistant", "text": text, "tools": tool_names}
                )

    return brief_from_turns(
        agent="gemini",
        session_id=session_id,
        path=str(path),
        cwd=cand.cwd,
        title=title or cand.title,
        model=model,
        git_branch=None,
        mtime_label=iso_mtime(path),
        turns=turns,
        files=files,
        skills=skills,
        tools=tools,
        ending_signals=endings,
        extras={
            k: str(v)
            for k, v in {
                "projectHash": meta.get("projectHash"),
                "tmp_dir": cand.extra.get("tmp_dir"),
            }.items()
            if v
        },
    )


register(
    Adapter(
        name="gemini",
        aliases=("gemini-cli",),
        discover=discover_gemini,
        extract=extract_gemini,
        roots=("~/.gemini/tmp", "~/.gemini/history"),
    )
)


# ---------------------------------------------------------------------------
# Antigravity / agy (best-effort)
# ---------------------------------------------------------------------------


def agy_conversations_dir() -> Path:
    return home() / ".gemini" / "antigravity-cli" / "conversations"


def agy_cwd_maps() -> dict[str, str]:
    """cwd -> conversation/cascade id."""
    mapping: dict[str, str] = {}
    for rel in (
        Path(".gemini") / "antigravity-cli" / "cache" / "last_conversations.json",
        Path(".gemini") / "antigravity-cli" / "cache" / "projects.json",
    ):
        path = home() / rel
        if not path.is_file():
            continue
        try:
            data = json.loads(path.read_text())
        except json.JSONDecodeError:
            continue
        if isinstance(data, dict):
            for k, v in data.items():
                if isinstance(k, str) and isinstance(v, str):
                    mapping[resolve_path(k)] = v
                    mapping[k] = v
    return mapping


def discover_agy(cwd: str, session_id: str | None) -> list[Candidate]:
    root = agy_conversations_dir()
    if not root.is_dir():
        return []
    cwd_r = resolve_path(cwd)
    cwd_map = agy_cwd_maps()
    wanted_ids: set[str] = set()
    if session_id is None:
        for key, val in cwd_map.items():
            if resolve_path(key) == cwd_r or key in {cwd, cwd_r}:
                wanted_ids.add(val)
        # also scan trajectory_metadata_blob for cwd string — expensive; skip unless empty
    out: list[Candidate] = []
    for db in root.glob("*.db"):
        sid = db.stem
        if session_id:
            q = session_id.strip()
            if not (sid == q or q in sid):
                continue
        elif wanted_ids and sid not in wanted_ids:
            # brain ids sometimes differ from conversation file names; allow mtime scan of mapped only
            continue
        elif not wanted_ids and session_id is None:
            continue
        out.append(
            Candidate(
                agent="agy",
                session_id=sid,
                cwd=cwd_r if sid in wanted_ids or not session_id else None,
                mtime=db.stat().st_mtime,
                title=None,
                path=str(db),
            )
        )
    # If session_id lookup found nothing via name, still try
    return out


def extract_agy(cand: Candidate) -> Brief:
    path = Path(cand.path or "")
    if not path.is_file():
        raise SystemExit(f"Antigravity conversation db not found: {cand.path}")

    conn = open_sqlite_ro(path)
    try:
        # Recover cwd from metadata blob if possible.
        cwd = cand.cwd
        try:
            blob_row = conn.execute(
                "SELECT data FROM trajectory_metadata_blob WHERE id = 'main' LIMIT 1"
            ).fetchone()
            if blob_row and blob_row[0]:
                text_bits = re.findall(rb"file://([^\x00-\x1f]{3,500})", blob_row[0])
                for bit in text_bits:
                    try:
                        decoded = bit.decode("utf-8", errors="ignore")
                        if decoded.startswith("/"):
                            cwd = decoded
                            break
                    except Exception:
                        pass
        except sqlite3.Error:
            pass

        printable: list[str] = []
        try:
            rows = conn.execute(
                "SELECT idx, step_payload, task_details, render_info FROM steps ORDER BY idx"
            ).fetchall()
        except sqlite3.Error:
            rows = []
        for row in rows:
            for blob in row[1:]:
                if not blob:
                    continue
                for match in re.findall(rb"[\x20-\x7e]{12,}", blob):
                    s = match.decode("ascii", errors="ignore").strip()
                    if s.startswith("file://"):
                        continue
                    if s.startswith("http"):
                        continue
                    if re.fullmatch(r"[A-Za-z0-9+/=_-]{20,}", s):
                        continue
                    printable.append(s)
    finally:
        conn.close()

    # Dedup preserving order, keep last meaningful chunks as turns.
    seen: set[str] = set()
    uniq: list[str] = []
    for s in printable:
        if s in seen:
            continue
        seen.add(s)
        uniq.append(s)

    turns: list[dict[str, Any]] = []
    # Heuristic: longer human-looking strings first as user-ish, later as assistant
    interesting = [s for s in uniq if len(s) > 40 and " " in s][-12:]
    for i, s in enumerate(interesting):
        role = "user" if i == 0 else "assistant"
        turns.append({"role": role, "text": s})

    notes = [
        "Antigravity/agy stores trajectories as protobuf blobs; "
        "this brief is best-effort string extraction, not a full transcript."
    ]
    return brief_from_turns(
        agent="agy",
        session_id=cand.session_id,
        path=str(path),
        cwd=cwd,
        title=cand.title,
        model=None,
        git_branch=None,
        mtime_label=iso_mtime(path),
        turns=turns,
        files=Counter(),
        skills=[],
        ending_signals=[interesting[-1]] if interesting else [],
        notes=notes,
    )


register(
    Adapter(
        name="agy",
        aliases=("antigravity", "antigravity-cli", "jetski"),
        discover=discover_agy,
        extract=extract_agy,
        roots=(
            "~/.gemini/antigravity-cli/conversations",
            "~/.gemini/antigravity-cli/cache/last_conversations.json",
        ),
    )
)


# ---------------------------------------------------------------------------
# First-class harnesses via sibling extractors or light adapters
# ---------------------------------------------------------------------------


def _skill_search_roots() -> list[Path]:
    # __file__ = .../<collection>/resume-from-agent/scripts/extract-session.py
    here = Path(__file__).resolve()
    roots: list[Path] = [
        here.parents[2],  # skills collection (repo or install tree)
        home() / ".agents" / "skills",
        home() / ".pi" / "agent" / "skills",
        home() / ".codex" / "skills",
        home() / ".claude" / "skills",
        home() / ".cursor" / "skills",
    ]
    out: list[Path] = []
    seen: set[Path] = set()
    for r in roots:
        try:
            r = r.resolve()
        except OSError:
            continue
        if r in seen or not r.is_dir():
            continue
        seen.add(r)
        out.append(r)
    return out


def find_sibling_extractor(agent: str) -> Path | None:
    name = f"resume-from-{agent}"
    for root in _skill_search_roots():
        candidate = root / name / "scripts" / "extract-session.py"
        if candidate.is_file():
            return candidate
        # root may already be the skills collection
        for path in root.glob(f"**/resume-from-{agent}/scripts/extract-session.py"):
            if path.is_file():
                return path
    return None


def stamp_harness(text: str, harness: str) -> str:
    """Ensure top-matter `agent:` is the harness name.

    Sibling briefs (esp. OpenCode) may already have `agent:` for an in-session
    role; rename those to `session_agent:` so discovery stays unambiguous.
    Idempotent: safe to call more than once.
    """
    lines = text.splitlines()
    out: list[str] = []
    known = set(agent_aliases()) | set(ADAPTERS)
    for i, line in enumerate(lines):
        if i < 30 and line.startswith("agent:"):
            val = line.split(":", 1)[1].strip()
            if val.lower() == harness.lower() or val.lower() in known:
                # Drop — single canonical harness line is inserted below.
                continue
            out.append(f"session_agent: {val}")
            continue
        if i < 30 and line.startswith("session_agent:"):
            out.append(line)
            continue
        out.append(line)
    if out and out[0].startswith("#"):
        out.insert(1, f"agent: {harness}")
    else:
        out.insert(0, f"agent: {harness}")
    body = "\n".join(out)
    if not body.endswith("\n"):
        body += "\n"
    return body


def run_sibling_extractor(
    agent: str, cwd: str, session_id: str | None
) -> str | None:
    script = find_sibling_extractor(agent)
    if not script:
        return None
    cmd = [sys.executable, str(script), "--cwd", cwd]
    if session_id:
        cmd.append(session_id)
    try:
        proc = subprocess.run(
            cmd,
            check=False,
            capture_output=True,
            text=True,
            timeout=120,
        )
    except (OSError, subprocess.TimeoutExpired):
        return None
    if proc.returncode != 0:
        return None
    text = proc.stdout or ""
    if not text.strip():
        return None
    return stamp_harness(text, agent)


def discover_via_sibling_path_scan(
    agent: str,
    cwd: str,
    session_id: str | None,
    path_globs: list[tuple[Path, str]],
    cwd_from_path: Callable[[Path], str | None] | None = None,
) -> list[Candidate]:
    """Fallback discovery when we only know filesystem layout."""
    out: list[Candidate] = []
    cwd_r = resolve_path(cwd)
    for base, pattern in path_globs:
        if not base.exists():
            continue
        for path in base.glob(pattern):
            if not path.is_file():
                continue
            if session_id and session_id.strip() not in path.name:
                continue
            path_cwd = cwd_from_path(path) if cwd_from_path else None
            if session_id is None:
                if path_cwd and resolve_path(path_cwd) not in {cwd_r, cwd}:
                    continue
                if path_cwd is None:
                    # For dir-encoded stores, check parent name
                    pass
            out.append(
                Candidate(
                    agent=agent,
                    session_id=path.stem,
                    cwd=path_cwd or cwd,
                    mtime=path.stat().st_mtime,
                    path=str(path),
                )
            )
    return out


def encode_claude_cwd(cwd: str) -> str:
    return cwd.replace("/", "-")


def discover_claude(cwd: str, session_id: str | None) -> list[Candidate]:
    root = home() / ".claude" / "projects"
    if not root.is_dir():
        return []
    out: list[Candidate] = []
    if session_id:
        sid = session_id.strip()
        hits = list(root.glob(f"*/{sid}.jsonl")) + list(root.rglob(f"{sid}.jsonl"))
        for path in hits:
            out.append(
                Candidate(
                    agent="claude",
                    session_id=path.stem,
                    cwd=None,
                    mtime=path.stat().st_mtime,
                    path=str(path),
                )
            )
        return out
    project = root / encode_claude_cwd(cwd)
    # also try resolved
    project2 = root / encode_claude_cwd(resolve_path(cwd))
    for proj in {project, project2}:
        if not proj.is_dir():
            continue
        for path in proj.glob("*.jsonl"):
            out.append(
                Candidate(
                    agent="claude",
                    session_id=path.stem,
                    cwd=cwd,
                    mtime=path.stat().st_mtime,
                    path=str(path),
                )
            )
    return out


def extract_claude_or_sibling(cand: Candidate) -> Brief:
    text = run_sibling_extractor("claude", cand.cwd or os.getcwd(), cand.session_id)
    if text:
        return _brief_from_preformatted(text, agent="claude", cand=cand)
    # Minimal fallback parse
    path = Path(cand.path or "")
    if not path.is_file():
        raise SystemExit(
            "Claude session found but no extractor available and path missing"
        )
    return _generic_jsonl_brief(path, agent="claude", session_id=cand.session_id, cwd=cand.cwd)


def encode_pi_cwd(cwd: str) -> str:
    return "--" + cwd.strip("/").replace("/", "-") + "--"


def discover_pi(cwd: str, session_id: str | None) -> list[Candidate]:
    root = home() / ".pi" / "agent" / "sessions"
    if not root.is_dir():
        return []
    out: list[Candidate] = []
    if session_id:
        sid = session_id.strip()
        for path in root.rglob(f"*{sid}*.jsonl"):
            out.append(
                Candidate(
                    agent="pi",
                    session_id=sid,
                    cwd=None,
                    mtime=path.stat().st_mtime,
                    path=str(path),
                )
            )
        return out
    for key in {encode_pi_cwd(cwd), encode_pi_cwd(resolve_path(cwd))}:
        project = root / key
        if not project.is_dir():
            continue
        for path in project.glob("*.jsonl"):
            stem = path.stem
            sid = stem.split("_", 1)[1] if "_" in stem else stem
            out.append(
                Candidate(
                    agent="pi",
                    session_id=sid,
                    cwd=cwd,
                    mtime=path.stat().st_mtime,
                    path=str(path),
                )
            )
    return out


def extract_pi_or_sibling(cand: Candidate) -> Brief:
    text = run_sibling_extractor("pi", cand.cwd or os.getcwd(), cand.session_id)
    if text:
        return _brief_from_preformatted(text, agent="pi", cand=cand)
    path = Path(cand.path or "")
    if not path.is_file():
        raise SystemExit("Pi session found but extractor unavailable")
    return _generic_jsonl_brief(path, agent="pi", session_id=cand.session_id, cwd=cand.cwd)


def discover_codex(cwd: str, session_id: str | None) -> list[Candidate]:
    root = home() / ".codex" / "sessions"
    if not root.is_dir():
        return []
    out: list[Candidate] = []
    cwd_r = resolve_path(cwd)
    if session_id:
        sid = session_id.strip()
        for path in root.rglob(f"*{sid}*.jsonl"):
            out.append(
                Candidate(
                    agent="codex",
                    session_id=sid,
                    cwd=None,
                    mtime=path.stat().st_mtime,
                    path=str(path),
                )
            )
        return out
    for path in root.rglob("rollout-*.jsonl"):
        try:
            with path.open() as fh:
                line = fh.readline()
            obj = json.loads(line) if line.strip() else {}
        except (OSError, json.JSONDecodeError):
            continue
        payload = obj.get("payload") or {}
        meta_cwd = payload.get("cwd")
        if not meta_cwd or resolve_path(str(meta_cwd)) not in {cwd_r, cwd}:
            continue
        sid = payload.get("session_id") or payload.get("id") or path.stem
        out.append(
            Candidate(
                agent="codex",
                session_id=str(sid),
                cwd=str(meta_cwd),
                mtime=path.stat().st_mtime,
                path=str(path),
            )
        )
    return out


def extract_codex_or_sibling(cand: Candidate) -> Brief:
    text = run_sibling_extractor("codex", cand.cwd or os.getcwd(), cand.session_id)
    if text:
        return _brief_from_preformatted(text, agent="codex", cand=cand)
    path = Path(cand.path or "")
    if not path.is_file():
        raise SystemExit("Codex session found but extractor unavailable")
    return _generic_jsonl_brief(path, agent="codex", session_id=cand.session_id, cwd=cand.cwd)


def discover_opencode(cwd: str, session_id: str | None) -> list[Candidate]:
    db = Path(
        os.environ.get("OPENCODE_DB")
        or (home() / ".local" / "share" / "opencode" / "opencode.db")
    )
    if not db.is_file():
        return []
    conn = open_sqlite_ro(db)
    try:
        # Detect columns defensively
        cols = {
            r[1]
            for r in conn.execute("PRAGMA table_info(session)").fetchall()
        }
        if "id" not in cols:
            return []
        cwd_r = resolve_path(cwd)
        if session_id:
            sid = session_id.strip()
            rows = conn.execute(
                "SELECT * FROM session WHERE id = ? OR id LIKE ? LIMIT 10",
                (sid, f"%{sid}%"),
            ).fetchall()
        else:
            clauses = []
            params: list[str] = []
            for col in ("directory", "path"):
                if col in cols:
                    clauses.append(f"{col} = ? OR {col} = ?")
                    params.extend([cwd, cwd_r])
            if not clauses:
                return []
            sql = f"SELECT * FROM session WHERE {' OR '.join(clauses)} ORDER BY rowid DESC LIMIT 20"
            rows = conn.execute(sql, params).fetchall()
        out: list[Candidate] = []
        for row in rows:
            data = dict(row)
            sid = str(data.get("id"))
            directory = data.get("directory") or data.get("path")
            # time fields may be ms
            ts = data.get("time_updated") or data.get("time_created") or 0
            try:
                mtime = float(ts)
                if mtime > 10_000_000_000:
                    mtime = mtime / 1000.0
            except (TypeError, ValueError):
                mtime = db.stat().st_mtime
            out.append(
                Candidate(
                    agent="opencode",
                    session_id=sid,
                    cwd=str(directory) if directory else None,
                    mtime=mtime,
                    title=str(data["title"]) if data.get("title") else None,
                    path=str(db),
                )
            )
        return out
    except sqlite3.Error:
        return []
    finally:
        conn.close()


def extract_opencode_or_sibling(cand: Candidate) -> Brief:
    text = run_sibling_extractor(
        "opencode", cand.cwd or os.getcwd(), cand.session_id
    )
    if text:
        return _brief_from_preformatted(text, agent="opencode", cand=cand)
    raise SystemExit(
        "OpenCode session found but sibling extractor unavailable; "
        "install resume-from-opencode or pass a different agent"
    )


def _brief_from_preformatted(text: str, agent: str, cand: Candidate) -> Brief:
    """Wrap sibling extractor stdout as a Brief for unified note injection."""
    # Parse a few fields for Candidate consistency; keep body as ending/raw.
    session_id = cand.session_id
    cwd = cand.cwd
    title = cand.title
    path = cand.path
    mtime_label = None
    for line in text.splitlines()[:40]:
        if line.startswith("session_id:"):
            session_id = line.split(":", 1)[1].strip()
        elif line.startswith("cwd:"):
            cwd = line.split(":", 1)[1].strip()
        elif line.startswith("title:") or line.startswith("name:"):
            title = line.split(":", 1)[1].strip()
        elif line.startswith("path:"):
            path = line.split(":", 1)[1].strip()
        elif line.startswith("mtime:") or line.startswith("updated:"):
            mtime_label = line.split(":", 1)[1].strip()
    brief = Brief(
        agent=agent,
        session_id=session_id,
        path=path,
        cwd=cwd,
        title=title,
        mtime_label=mtime_label,
    )
    # Stash full text in extras for main() to print directly.
    brief.extras["__raw__"] = text
    return brief


def _generic_jsonl_brief(
    path: Path, agent: str, session_id: str, cwd: str | None
) -> Brief:
    turns: list[dict[str, Any]] = []
    files: Counter[str] = Counter()
    skills: list[str] = []
    with path.open() as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            try:
                obj = json.loads(line)
            except json.JSONDecodeError:
                continue
            if not isinstance(obj, dict):
                continue
            role = obj.get("role") or obj.get("type")
            message = obj.get("message") if isinstance(obj.get("message"), dict) else obj
            content = message.get("content") if isinstance(message, dict) else None
            text = extract_text(content) if content is not None else extract_text(
                obj.get("text") or obj.get("content")
            )
            text, found = clean_skill_injections(text or "")
            skills.extend(found)
            tool_names: list[str] = []
            if isinstance(content, list):
                for block in content:
                    if not isinstance(block, dict):
                        continue
                    if block.get("type") in {"tool_use", "toolCall"}:
                        name = block.get("name") or "tool"
                        tool_names.append(str(name))
                        inp = block.get("input") or parse_json_maybe(
                            block.get("arguments")
                        )
                        if isinstance(inp, dict):
                            pth = path_from_mapping(str(name), inp)
                            if pth:
                                files[pth] += 1
            if role in {"user", "human"} and text:
                turns.append({"role": "user", "text": text})
            elif role in {"assistant", "ai", "model", "gemini"} and (
                text or tool_names
            ):
                turns.append(
                    {"role": "assistant", "text": text, "tools": tool_names}
                )
    return brief_from_turns(
        agent=agent,
        session_id=session_id,
        path=str(path),
        cwd=cwd,
        title=None,
        model=None,
        git_branch=None,
        mtime_label=iso_mtime(path),
        turns=turns,
        files=files,
        skills=skills,
        notes=["Parsed with generic JSONL fallback."],
    )


register(
    Adapter(
        name="claude",
        aliases=("claude-code", "anthropic"),
        discover=discover_claude,
        extract=extract_claude_or_sibling,
        roots=("~/.claude/projects",),
    )
)
register(
    Adapter(
        name="pi",
        aliases=("pi-coding-agent",),
        discover=discover_pi,
        extract=extract_pi_or_sibling,
        roots=("~/.pi/agent/sessions",),
    )
)
register(
    Adapter(
        name="codex",
        aliases=("codex-cli",),
        discover=discover_codex,
        extract=extract_codex_or_sibling,
        roots=("~/.codex/sessions",),
    )
)
register(
    Adapter(
        name="opencode",
        aliases=("open-code",),
        discover=discover_opencode,
        extract=extract_opencode_or_sibling,
        roots=("~/.local/share/opencode/opencode.db",),
    )
)


# Auggie placeholder — discover always empty until store is known.
def discover_auggie(cwd: str, session_id: str | None) -> list[Candidate]:
    return []


def extract_auggie(cand: Candidate) -> Brief:
    raise SystemExit("Auggie/Augment session store is not configured on this machine")


register(
    Adapter(
        name="auggie",
        aliases=("augment", "augment-cli"),
        discover=discover_auggie,
        extract=extract_auggie,
        roots=("(unknown — auggie not observed on this machine)",),
    )
)


# ---------------------------------------------------------------------------
# Orchestration
# ---------------------------------------------------------------------------


def discover_all(
    cwd: str,
    session_id: str | None,
    agent_filter: str | None,
) -> list[Candidate]:
    aliases = agent_aliases()
    if agent_filter:
        key = aliases.get(agent_filter.lower())
        if not key:
            known = ", ".join(sorted(ADAPTERS))
            raise SystemExit(
                f"Unknown agent {agent_filter!r}. Known: {known}"
            )
        adapters = [ADAPTERS[key]]
    else:
        adapters = list(ADAPTERS.values())

    found: list[Candidate] = []
    errors: list[str] = []
    for adapter in adapters:
        try:
            found.extend(adapter.discover(cwd, session_id))
        except Exception as exc:  # noqa: BLE001 — keep discovery resilient
            errors.append(f"{adapter.name}: {exc}")
    # de-dupe by (agent, session_id, path)
    uniq: dict[tuple[str, str, str | None], Candidate] = {}
    for c in found:
        uniq[(c.agent, c.session_id, c.path)] = c
    ranked = sorted(uniq.values(), key=lambda c: c.mtime, reverse=True)
    if not ranked and errors and agent_filter:
        raise SystemExit(
            "Discovery failed for "
            + agent_filter
            + ":\n"
            + "\n".join(errors)
        )
    return ranked


def format_list(candidates: list[Candidate]) -> str:
    if not candidates:
        return "No matching sessions.\n"
    lines = ["# Session candidates (newest first)", ""]
    for i, c in enumerate(candidates[:30], 1):
        ts = datetime.fromtimestamp(c.mtime).isoformat(timespec="seconds") if c.mtime else "?"
        title = f" — {c.title}" if c.title else ""
        cwd = f" cwd={c.cwd}" if c.cwd else ""
        path = f" path={c.path}" if c.path else ""
        lines.append(
            f"{i}. {c.agent}  id={c.session_id}  mtime={ts}{title}{cwd}{path}"
        )
    if len(candidates) > 30:
        lines.append(f"… and {len(candidates) - 30} more")
    return "\n".join(lines) + "\n"


def pick_candidate(
    candidates: list[Candidate],
) -> tuple[Candidate, list[str]]:
    if not candidates:
        raise SystemExit("No matching sessions.")
    winner = candidates[0]
    notes: list[str] = []
    if len(candidates) > 1:
        runner = candidates[1]
        delta = abs(winner.mtime - runner.mtime)
        if runner.agent != winner.agent and delta <= 300:
            notes.append(
                f"Close runner-up: {runner.agent} id={runner.session_id} "
                f"({int(delta)}s apart). Pass the agent name to force it."
            )
        notes.append(
            f"Selected {winner.agent} over {len(candidates) - 1} other match(es)."
        )
    return winner, notes


def extract_path_generic(path: Path, agent_label: str = "unknown") -> Brief:
    if path.suffix == ".db":
        # Try agy-style
        cand = Candidate(
            agent="agy",
            session_id=path.stem,
            cwd=None,
            mtime=path.stat().st_mtime,
            path=str(path),
        )
        return extract_agy(cand)
    if path.suffix in {".jsonl", ".json"}:
        return _generic_jsonl_brief(
            path, agent=agent_label, session_id=path.stem, cwd=None
        )
    raise SystemExit(f"Unsupported path type for generic extract: {path}")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description=(
            "Extract a resume brief from the latest (or specified) coding-agent "
            "session across known stores."
        )
    )
    parser.add_argument(
        "--cwd",
        default=str(Path.cwd()),
        help="Workspace cwd used to find sessions (default: process cwd)",
    )
    parser.add_argument(
        "--agent",
        default=None,
        help="Restrict to one agent (hermes, dirac, goose, cursor, gemini, agy, claude, …)",
    )
    parser.add_argument(
        "--list",
        action="store_true",
        help="List matching candidates instead of extracting a brief",
    )
    parser.add_argument(
        "--path",
        default=None,
        help="Explicit transcript/db path (skips discovery)",
    )
    parser.add_argument(
        "ref",
        nargs="*",
        help=(
            "Optional agent name and/or session id. "
            "Examples: hermes | 20260701_134450_4ae33b | goose 20251016_150658"
        ),
    )
    args = parser.parse_args(argv)

    agent_filter = args.agent
    session_id = None
    aliases = agent_aliases()

    for token in args.ref:
        if agent_filter is None and token.lower() in aliases:
            agent_filter = aliases[token.lower()]
        elif session_id is None:
            session_id = token
        else:
            # allow multi-word title search joined
            session_id = f"{session_id} {token}"

    if args.path:
        path = Path(args.path).expanduser()
        if not path.exists():
            raise SystemExit(f"Path not found: {path}")
        label = agent_filter or "unknown"
        brief = extract_path_generic(path, agent_label=label)
        sys.stdout.write(render_brief(brief))
        return 0

    candidates = discover_all(args.cwd, session_id, agent_filter)
    if args.list:
        sys.stdout.write(format_list(candidates))
        if not candidates:
            roots = []
            for a in ADAPTERS.values():
                if agent_filter and a.name != agent_filter:
                    continue
                roots.extend(a.roots)
            sys.stdout.write(
                "Probed: " + ", ".join(roots) + "\n"
            )
        return 0 if candidates else 1

    if not candidates:
        roots: list[str] = []
        for a in ADAPTERS.values():
            if agent_filter and a.name != (
                agent_aliases().get(agent_filter, agent_filter)
            ):
                continue
            roots.extend(a.roots)
        msg = f"No sessions found for cwd {args.cwd!r}"
        if agent_filter:
            msg += f" agent={agent_filter}"
        if session_id:
            msg += f" id={session_id!r}"
        msg += "\nProbed: " + ", ".join(roots)
        raise SystemExit(msg)

    winner, notes = pick_candidate(candidates)
    adapter = ADAPTERS[winner.agent]
    brief = adapter.extract(winner)
    brief.notes = notes + list(brief.notes)

    raw = brief.extras.pop("__raw__", None)
    if raw:
        # Sibling extractor already rendered a full brief; inject discovery notes.
        raw = stamp_harness(raw, winner.agent)
        if notes:
            lines = raw.splitlines()
            insert_at = 1
            for i, line in enumerate(lines[:30]):
                if line.startswith("## "):
                    insert_at = i
                    break
            block = ["## Discovery notes", *[f"- {n}" for n in notes], ""]
            lines = lines[:insert_at] + block + lines[insert_at:]
            sys.stdout.write("\n".join(lines) + "\n")
        else:
            sys.stdout.write(raw if raw.endswith("\n") else raw + "\n")
        return 0

    sys.stdout.write(render_brief(brief))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

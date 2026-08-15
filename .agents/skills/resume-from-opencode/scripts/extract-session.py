#!/usr/bin/env python3
"""Extract a resume brief from an OpenCode session (SQLite store)."""

from __future__ import annotations

import argparse
import json
import os
import re
import sqlite3
import sys
from collections import Counter, deque
from datetime import datetime, timezone
from pathlib import Path


def default_db_path() -> Path:
    override = os.environ.get("OPENCODE_DB")
    if override:
        return Path(override).expanduser()
    return Path.home() / ".local" / "share" / "opencode" / "opencode.db"


def connect(db_path: Path) -> sqlite3.Connection:
    if not db_path.is_file():
        raise SystemExit(f"No OpenCode database at {db_path}")
    # Read-only URI so a live OpenCode process can keep the DB open.
    uri = f"file:{db_path}?mode=ro"
    conn = sqlite3.connect(uri, uri=True, timeout=15.0)
    conn.row_factory = sqlite3.Row
    conn.execute("PRAGMA busy_timeout = 15000")
    conn.execute("PRAGMA query_only = ON")
    return conn


def resolve_cwd(path: str) -> str:
    try:
        return str(Path(path).expanduser().resolve())
    except OSError:
        return str(Path(path).expanduser())


def parse_model(raw) -> str | None:
    if raw is None:
        return None
    if isinstance(raw, dict):
        provider = raw.get("providerID") or raw.get("providerId")
        model_id = raw.get("id") or raw.get("modelID") or raw.get("modelId")
        variant = raw.get("variant")
        parts = [p for p in (provider, model_id) if p]
        label = "/".join(parts) if parts else None
        if label and variant:
            return f"{label} ({variant})"
        return label
    if isinstance(raw, str):
        text = raw.strip()
        if not text:
            return None
        try:
            return parse_model(json.loads(text))
        except json.JSONDecodeError:
            return text
    return None


def ms_to_iso(ms) -> str | None:
    if ms is None:
        return None
    try:
        value = int(ms)
    except (TypeError, ValueError):
        return None
    # OpenCode stores epoch ms
    if value > 10_000_000_000:
        value = value / 1000.0
    return datetime.fromtimestamp(value, tz=timezone.utc).astimezone().isoformat(
        timespec="seconds"
    )


def truncate(text: str, limit: int) -> str:
    text = text.strip()
    if len(text) <= limit:
        return text
    return text[: limit - 1].rstrip() + "…"


def clean_user_text(text: str) -> str:
    skills = re.findall(r"\[\$([^\]]+)\]", text)
    skills += re.findall(r"<name>\s*([^<]+?)\s*</name>", text)
    skills += re.findall(r'name="([^"]+)"', text) if "<skill" in text else []
    text = re.sub(r"<skill\b[^>]*>.*?</skill>", "", text, flags=re.S | re.I)
    text = re.sub(r"<skill_content\b[^>]*>.*?</skill_content>", "", text, flags=re.S | re.I)
    text = re.sub(
        r"<environment_context>.*?</environment_context>", "", text, flags=re.S | re.I
    )
    text = text.strip()
    # Drop giant HTML dumps / pasted pages that drown the brief
    if text.count("<") > 30 and len(text) > 2000:
        text = truncate(re.sub(r"<[^>]+>", " ", text), 400)
    if skills:
        label = "[skill] " + ", ".join(
            dict.fromkeys(s.strip() for s in skills if s.strip())
        )
        if not text:
            return label
        text = re.sub(r"\[\$[^\]]+\]\([^)]+\)\s*", "", text).strip()
        return f"{label}\n{text}".strip() if text else label
    return text


def load_json(raw) -> dict:
    if raw is None:
        return {}
    if isinstance(raw, dict):
        return raw
    try:
        obj = json.loads(raw)
    except (TypeError, json.JSONDecodeError):
        return {}
    return obj if isinstance(obj, dict) else {}


def resolve_session(
    conn: sqlite3.Connection, cwd: str, session_id: str | None
) -> sqlite3.Row:
    if session_id:
        sid = session_id.strip()
        # Prefer exact id, then prefix/partial, then slug, then title.
        row = conn.execute(
            "SELECT * FROM session WHERE id = ? LIMIT 1", (sid,)
        ).fetchone()
        if row:
            return row
        rows = conn.execute(
            """
            SELECT * FROM session
            WHERE id LIKE ?
               OR slug = ?
               OR slug LIKE ?
               OR title = ?
               OR lower(title) LIKE lower(?)
            ORDER BY
              CASE
                WHEN id = ? THEN 0
                WHEN id LIKE ? THEN 1
                WHEN slug = ? THEN 2
                WHEN title = ? THEN 3
                ELSE 4
              END,
              time_updated DESC
            LIMIT 5
            """,
            (
                f"%{sid}%",
                sid,
                f"%{sid}%",
                sid,
                f"%{sid}%",
                sid,
                f"{sid}%",
                sid,
                sid,
            ),
        ).fetchall()
        if not rows:
            raise SystemExit(f"No OpenCode session found for id/slug/title: {sid}")
        return rows[0]

    cwd_resolved = resolve_cwd(cwd)
    candidates = conn.execute(
        """
        SELECT * FROM session
        WHERE directory = ? OR directory = ?
           OR path = ? OR path = ?
        ORDER BY
          CASE WHEN time_archived IS NULL THEN 0 ELSE 1 END,
          time_updated DESC
        LIMIT 1
        """,
        (cwd, cwd_resolved, cwd, cwd_resolved),
    ).fetchone()
    if candidates:
        return candidates

    # Fallback: project worktree match via project table
    row = conn.execute(
        """
        SELECT s.*
        FROM session s
        JOIN project p ON p.id = s.project_id
        WHERE p.worktree = ? OR p.worktree = ?
        ORDER BY
          CASE WHEN s.time_archived IS NULL THEN 0 ELSE 1 END,
          s.time_updated DESC
        LIMIT 1
        """,
        (cwd, cwd_resolved),
    ).fetchone()
    if row:
        return row
    raise SystemExit(
        f"No OpenCode sessions found for cwd {cwd!r} in {default_db_path()}"
    )


def paths_from_patch(patch_text: str, files: Counter[str]) -> None:
    for match in re.finditer(
        r"\*\*\* (?:Update|Add|Delete) File:\s*([^\n\r]+)", patch_text or ""
    ):
        path = match.group(1).strip().split("\\n")[0].strip()
        if path and len(path) < 512:
            files[path] += 1


def extract_file_from_input(tool: str, inp: dict, files: Counter[str]) -> None:
    if not isinstance(inp, dict):
        return
    # Search roots / workdirs are not "files in play".
    if tool in {"glob", "grep", "bash", "task", "todowrite", "todo", "question"}:
        for key in ("filePath", "file_path", "target_file"):
            value = inp.get(key)
            if isinstance(value, str) and value.strip() and len(value) < 512:
                files[value.strip()] += 1
                return
        return
    for key in ("filePath", "file_path", "path", "target", "target_file"):
        value = inp.get(key)
        if isinstance(value, str) and value.strip() and len(value) < 512:
            files[value.strip()] += 1
            return
    if tool in {"apply_patch", "applyPatch"}:
        patch = inp.get("patchText") or inp.get("patch") or inp.get("diff") or ""
        if isinstance(patch, str):
            paths_from_patch(patch, files)


def parse_session(conn: sqlite3.Connection, session: sqlite3.Row) -> dict:
    sid = session["id"]

    messages = conn.execute(
        """
        SELECT id, time_created, data
        FROM message
        WHERE session_id = ?
        ORDER BY time_created ASC, id ASC
        """,
        (sid,),
    ).fetchall()

    # Prefer message+part join; fall back to session_message if message empty
    parts = conn.execute(
        """
        SELECT id, message_id, time_created, data
        FROM part
        WHERE session_id = ?
        ORDER BY time_created ASC, id ASC
        """,
        (sid,),
    ).fetchall()

    msg_role: dict[str, str] = {}
    for msg in messages:
        data = load_json(msg["data"])
        role = data.get("role") or "unknown"
        msg_role[msg["id"]] = role

    user_turns: list[str] = []
    recent_users: deque[str] = deque(maxlen=4)
    recent_agents: deque[str] = deque(maxlen=8)
    tools: Counter[str] = Counter()
    files: Counter[str] = Counter()
    skills: list[str] = []
    ending_signals: list[str] = []
    running_tools: list[str] = []

    for part in parts:
        data = load_json(part["data"])
        ptype = data.get("type")
        role = msg_role.get(part["message_id"], "unknown")

        if ptype == "text":
            text = data.get("text") or ""
            if not isinstance(text, str) or not text.strip():
                continue
            if role == "user":
                cleaned = clean_user_text(text)
                if cleaned:
                    user_turns.append(cleaned)
                    recent_users.append(cleaned)
            elif role == "assistant":
                recent_agents.append(text.strip())
            continue

        if ptype != "tool":
            continue

        tool = data.get("tool") or "unknown"
        tools[tool] += 1
        state = data.get("state") or {}
        if not isinstance(state, dict):
            state = {}
        status = state.get("status")
        inp = state.get("input") or {}
        if not isinstance(inp, dict):
            inp = {}

        if tool == "skill":
            name = inp.get("name") or inp.get("skill")
            if isinstance(name, str) and name.strip():
                skills.append(name.strip())

        extract_file_from_input(tool, inp, files)

        if status == "running":
            running_tools.append(tool)
        elif status == "error":
            err = state.get("error") or state.get("output") or "error"
            ending_signals.append(f"tool_error:{tool}: {truncate(str(err), 200)}")

    todos = conn.execute(
        """
        SELECT content, status, priority, position
        FROM todo
        WHERE session_id = ?
        ORDER BY position ASC
        """,
        (sid,),
    ).fetchall()

    if running_tools:
        ending_signals.append(
            "mid-tool: " + ", ".join(dict.fromkeys(running_tools))
        )

    if not user_turns and not parts:
        # Legacy / alternate storage: session_message blobs
        sm_rows = conn.execute(
            """
            SELECT type, data, time_created
            FROM session_message
            WHERE session_id = ?
            ORDER BY seq ASC, time_created ASC
            """,
            (sid,),
        ).fetchall()
        for row in sm_rows:
            data = load_json(row["data"])
            role = data.get("role") or row["type"]
            text = data.get("text") or data.get("content") or ""
            if isinstance(text, list):
                text = "\n".join(
                    b.get("text", "") for b in text if isinstance(b, dict)
                )
            if not isinstance(text, str) or not text.strip():
                continue
            if role == "user":
                cleaned = clean_user_text(text)
                if cleaned:
                    user_turns.append(cleaned)
                    recent_users.append(cleaned)
            elif role in {"assistant", "agent"}:
                recent_agents.append(text.strip())

    model = parse_model(session["model"])
    # Prefer model from latest assistant message if session.model empty
    if not model:
        for msg in reversed(messages):
            data = load_json(msg["data"])
            if data.get("role") == "assistant":
                model = parse_model(
                    {
                        "providerID": data.get("providerID"),
                        "id": data.get("modelID"),
                        "variant": data.get("variant"),
                    }
                )
                if model:
                    break

    return {
        "session": session,
        "model": model,
        "user_turns": user_turns,
        "recent_users": list(recent_users),
        "recent_agents": list(recent_agents),
        "tools": tools,
        "files": files,
        "skills": skills,
        "todos": todos,
        "ending_signals": ending_signals,
        "message_count": len(messages),
        "part_count": len(parts),
    }


def render(brief: dict, db_path: Path) -> str:
    session = brief["session"]
    lines: list[str] = []
    lines.append("# OpenCode session brief")
    lines.append(f"session_id: {session['id']}")
    if session["slug"]:
        lines.append(f"slug: {session['slug']}")
    lines.append(f"db: {db_path}")
    if session["title"]:
        lines.append(f"title: {session['title']}")
    directory = session["directory"] or session["path"] or ""
    if directory:
        lines.append(f"cwd: {directory}")
    if session["path"] and session["path"] != directory:
        lines.append(f"path: {session['path']}")
    if session["agent"]:
        lines.append(f"agent: {session['agent']}")
    if brief.get("model"):
        lines.append(f"model: {brief['model']}")
    if session["version"]:
        lines.append(f"version: {session['version']}")
    updated = ms_to_iso(session["time_updated"])
    created = ms_to_iso(session["time_created"])
    if updated:
        lines.append(f"updated: {updated}")
    if created:
        lines.append(f"created: {created}")
    if session["time_archived"]:
        lines.append(f"archived: {ms_to_iso(session['time_archived'])}")
    lines.append(
        f"counts: messages={brief['message_count']} parts={brief['part_count']}"
    )

    recent_users = brief["recent_users"]
    if recent_users:
        lines.append(f"last_prompt: {truncate(recent_users[-1], 500)}")

    skills = brief["skills"]
    if skills:
        lines.append("")
        lines.append("## Skills invoked")
        for skill in dict.fromkeys(skills):
            lines.append(f"- {skill}")

    todos = brief["todos"]
    if todos:
        lines.append("")
        lines.append("## Todos")
        for todo in todos:
            status = todo["status"] or "?"
            content = todo["content"] or ""
            lines.append(f"- [{status}] {truncate(content, 200)}")

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
        for path, count in files.most_common(40):
            lines.append(f"- {path} ({count})")

    goal_turns = brief["user_turns"][:3]
    if goal_turns:
        lines.append("")
        lines.append("## Opening goal")
        for text in goal_turns:
            lines.append("")
            lines.append("### User")
            lines.append(truncate(text, 1200))

    if recent_users or brief["recent_agents"]:
        lines.append("")
        lines.append("## Recent turns")
        for text in recent_users:
            lines.append("")
            lines.append("### User")
            lines.append(truncate(text, 1500))
        for text in brief["recent_agents"]:
            lines.append("")
            lines.append("### Assistant")
            lines.append(truncate(text, 2000))

    endings = brief["ending_signals"]
    lines.append("")
    lines.append("## Ending")
    if endings:
        lines.append(truncate(str(endings[-1]), 800))
    elif brief["recent_agents"]:
        lines.append(truncate(brief["recent_agents"][-1], 800))
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
        description="Extract a resume brief from an OpenCode session."
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
        help="OpenCode session id (full/partial), slug, or title; omit for latest for --cwd",
    )
    parser.add_argument(
        "--db",
        default=None,
        help="Path to opencode.db (default: ~/.local/share/opencode/opencode.db)",
    )
    args = parser.parse_args(argv)

    db_path = Path(args.db).expanduser() if args.db else default_db_path()
    conn = connect(db_path)
    try:
        session = resolve_session(conn, args.cwd, args.session_id)
        brief = parse_session(conn, session)
        sys.stdout.write(render(brief, db_path))
    finally:
        conn.close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

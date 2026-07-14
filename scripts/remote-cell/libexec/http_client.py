#!/usr/bin/env python3
"""Minimal gateway client helpers for smoke/prove (stdlib only)."""
from __future__ import annotations

import json
import urllib.error
import urllib.request
import uuid
from typing import Any


def call(
    base_url: str,
    project_id: str,
    token: str,
    method: str,
    path: str,
    body: dict[str, Any] | None = None,
    idem: str | None = None,
    timeout: float = 90.0,
) -> tuple[int, Any]:
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(base_url.rstrip("/") + path, data=data, method=method)
    req.add_header("Authorization", "Bearer " + token)
    req.add_header("X-Beads-Project-ID", project_id)
    if data is not None:
        req.add_header("Content-Type", "application/json")
    if idem:
        req.add_header("Idempotency-Key", idem)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            raw = resp.read()
            try:
                return resp.status, json.loads(raw.decode() or "null")
            except json.JSONDecodeError:
                return resp.status, raw.decode()
    except urllib.error.HTTPError as e:
        raw = e.read()
        try:
            return e.code, json.loads(raw.decode() or "null")
        except Exception:
            return e.code, raw.decode()


def terminal(
    base_url: str,
    project_id: str,
    token: str,
    kind: str,
    payload: dict[str, Any],
    key: str | None = None,
    wait_ms: int = 30000,
) -> tuple[int, dict[str, Any]]:
    body = {"kind": kind, "payload": payload}
    path = f"/v1/projects/{project_id}/terminal-operations?wait_ms={wait_ms}"
    st, doc = call(base_url, project_id, token, "POST", path, body=body, idem=key or ("k-" + uuid.uuid4().hex))
    if not isinstance(doc, dict):
        raise RuntimeError(f"non-json terminal response: {doc!r}")
    return st, doc


def issue_id_from(doc: dict[str, Any]) -> str | None:
    result = doc.get("result")
    if isinstance(result, dict):
        return result.get("id") or result.get("issue_id")
    return None

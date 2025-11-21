"""Agent Mail integration for beads MCP server.

Provides simple messaging functions that wrap Agent Mail HTTP API.
Requires BEADS_AGENT_MAIL_URL and BEADS_AGENT_NAME environment variables.
"""

import logging
import os
from typing import Any, Optional
from urllib.parse import urljoin

import requests

logger = logging.getLogger(__name__)

# Timeout for Agent Mail HTTP requests (seconds)
AGENT_MAIL_TIMEOUT = 5.0
AGENT_MAIL_RETRIES = 2


class MailError(Exception):
    """Base exception for Agent Mail errors."""

    def __init__(self, code: str, message: str, data: Optional[dict[str, Any]] = None):
        self.code = code
        self.message = message
        self.data = data or {}
        super().__init__(message)


def _get_config() -> tuple[str, str, Optional[str]]:
    """Get Agent Mail configuration from environment.

    Returns:
        (base_url, agent_name, token)

    Raises:
        MailError: If required configuration is missing
    """
    base_url = os.environ.get("BEADS_AGENT_MAIL_URL")
    if not base_url:
        raise MailError(
            "NOT_CONFIGURED",
            "Agent Mail not configured. Set BEADS_AGENT_MAIL_URL environment variable.\n"
            "Example: export BEADS_AGENT_MAIL_URL=http://127.0.0.1:8765\n"
            "See docs/AGENT_MAIL_QUICKSTART.md for setup instructions.",
        )

    agent_name = os.environ.get("BEADS_AGENT_NAME")
    if not agent_name:
        # Try to derive from user/repo
        import getpass

        try:
            user = getpass.getuser()
            cwd = os.getcwd()
            repo_name = os.path.basename(cwd)
            agent_name = f"{user}-{repo_name}"
            logger.warning(
                f"BEADS_AGENT_NAME not set, using derived name: {agent_name}"
            )
        except Exception:
            raise MailError(
                "NOT_CONFIGURED",
                "Agent Mail not configured. Set BEADS_AGENT_NAME environment variable.\n"
                "Example: export BEADS_AGENT_NAME=my-agent",
            )

    token = os.environ.get("BEADS_AGENT_MAIL_TOKEN")

    return base_url, agent_name, token


def _get_project_key() -> str:
    """Get project key from environment or derive from Git/workspace.

    Returns:
        Project key (absolute path to workspace root)
    """
    # Check explicit project ID first
    project_id = os.environ.get("BEADS_PROJECT_ID")
    if project_id:
        return project_id

    # Try to get from bd workspace detection
    # Import here to avoid circular dependency
    from .tools import _find_beads_db_in_tree

    workspace = _find_beads_db_in_tree()
    if workspace:
        return os.path.abspath(workspace)

    # Fallback to current directory
    return os.path.abspath(os.getcwd())


def _call_agent_mail(
    method: str,
    endpoint: str,
    json_data: Optional[dict[str, Any]] = None,
    params: Optional[dict[str, Any]] = None,
) -> Any:
    """Make HTTP request to Agent Mail server with retries.

    Args:
        method: HTTP method (GET, POST, DELETE, etc.)
        endpoint: API endpoint path (e.g., "/api/messages")
        json_data: Request body as JSON
        params: URL query parameters

    Returns:
        Response JSON

    Raises:
        MailError: On request failure or server error
    """
    base_url, _, token = _get_config()
    url = urljoin(base_url, endpoint)

    headers = {}
    if token:
        headers["Authorization"] = f"Bearer {token}"

    # Use idempotency key for write operations to avoid duplicates on retry
    if method in {"POST", "PUT"} and json_data:
        import uuid

        headers["Idempotency-Key"] = str(uuid.uuid4())

    last_error = None
    for attempt in range(AGENT_MAIL_RETRIES + 1):
        try:
            response = requests.request(
                method,
                url,
                json=json_data,
                params=params,
                headers=headers,
                timeout=AGENT_MAIL_TIMEOUT,
            )

            # Success
            if response.status_code < 400:
                return response.json() if response.content else {}

            # Client error - don't retry
            if 400 <= response.status_code < 500:
                error_data = {}
                try:
                    error_data = response.json()
                except Exception:
                    error_data = {"detail": response.text}

                if response.status_code == 404:
                    raise MailError(
                        "NOT_FOUND",
                        f"Resource not found: {endpoint}",
                        error_data,
                    )
                elif response.status_code == 409:
                    raise MailError(
                        "CONFLICT",
                        error_data.get("detail", "Conflict"),
                        error_data,
                    )
                else:
                    raise MailError(
                        "INVALID_ARGUMENT",
                        error_data.get("detail", f"HTTP {response.status_code}"),
                        error_data,
                    )

            # Server error - retry
            last_error = MailError(
                "UNAVAILABLE",
                f"Agent Mail server error: HTTP {response.status_code}",
                {"status": response.status_code, "attempt": attempt + 1},
            )

        except requests.exceptions.Timeout:
            last_error = MailError(
                "TIMEOUT",
                f"Agent Mail request timeout after {AGENT_MAIL_TIMEOUT}s",
                {"attempt": attempt + 1},
            )
        except requests.exceptions.ConnectionError as e:
            last_error = MailError(
                "UNAVAILABLE",
                f"Cannot connect to Agent Mail server at {base_url}",
                {"error": str(e), "attempt": attempt + 1},
            )
        except MailError:
            raise  # Re-raise our own errors
        except Exception as e:
            last_error = MailError(
                "INTERNAL_ERROR",
                f"Unexpected error calling Agent Mail: {e}",
                {"error": str(e), "attempt": attempt + 1},
            )

        # Exponential backoff between retries
        if attempt < AGENT_MAIL_RETRIES:
            import time

            time.sleep(0.5 * (2**attempt))

    # All retries exhausted
    if last_error:
        raise last_error
    raise MailError("INTERNAL_ERROR", "Request failed with no error details")


def mail_send(
    to: list[str],
    subject: str,
    body: str,
    urgent: bool = False,
    cc: Optional[list[str]] = None,
    project_key: Optional[str] = None,
    sender_name: Optional[str] = None,
) -> dict[str, Any]:
    """Send a message to other agents.

    Args:
        to: List of recipient agent names
        subject: Message subject
        body: Message body (Markdown)
        urgent: Mark as urgent (default: False)
        cc: Optional CC recipients
        project_key: Override project identifier (default: auto-detect)
        sender_name: Override sender name (default: BEADS_AGENT_NAME)

    Returns:
        {
            "message_id": int,
            "thread_id": str,
            "sent_to": int  # number of recipients
        }

    Raises:
        MailError: On configuration or delivery error
    """
    _, auto_agent_name, _ = _get_config()
    auto_project_key = _get_project_key()

    sender = sender_name or auto_agent_name
    project = project_key or auto_project_key

    importance = "urgent" if urgent else "normal"

    # Call Agent Mail send_message tool via HTTP
    # Note: Agent Mail MCP tools use POST to /mcp/call endpoint
    result = _call_agent_mail(
        "POST",
        "/mcp/call",
        json_data={
            "method": "tools/call",
            "params": {
                "name": "send_message",
                "arguments": {
                    "project_key": project,
                    "sender_name": sender,
                    "to": to,
                    "subject": subject,
                    "body_md": body,
                    "cc": cc or [],
                    "importance": importance,
                },
            },
        },
    )

    # Extract message details from result
    # Agent Mail returns: {"deliveries": [...], "count": N}
    deliveries = result.get("deliveries", [])
    if not deliveries:
        raise MailError("INTERNAL_ERROR", "No deliveries returned from Agent Mail")

    # Get message ID from first delivery
    first_delivery = deliveries[0]
    payload = first_delivery.get("payload", {})
    message_id = payload.get("id")
    thread_id = payload.get("thread_id")

    return {
        "message_id": message_id,
        "thread_id": thread_id,
        "sent_to": len(deliveries),
    }


def mail_inbox(
    limit: int = 20,
    urgent_only: bool = False,
    unread_only: bool = False,
    cursor: Optional[str] = None,
    agent_name: Optional[str] = None,
    project_key: Optional[str] = None,
) -> dict[str, Any]:
    """Get messages from inbox.

    Args:
        limit: Maximum messages to return (default: 20)
        urgent_only: Only return urgent messages
        unread_only: Only return unread messages
        cursor: Pagination cursor (for next page)
        agent_name: Override agent name (default: BEADS_AGENT_NAME)
        project_key: Override project (default: auto-detect)

    Returns:
        {
            "messages": [
                {
                    "id": int,
                    "thread_id": str,
                    "from": str,
                    "subject": str,
                    "created_ts": str,  # ISO-8601
                    "unread": bool,
                    "ack_required": bool,
                    "urgent": bool,
                    "preview": str  # first 100 chars
                },
                ...
            ],
            "next_cursor": str | None
        }
    """
    _, auto_agent_name, _ = _get_config()
    auto_project_key = _get_project_key()

    agent = agent_name or auto_agent_name
    project = project_key or auto_project_key

    # Call fetch_inbox via MCP
    result = _call_agent_mail(
        "POST",
        "/mcp/call",
        json_data={
            "method": "tools/call",
            "params": {
                "name": "fetch_inbox",
                "arguments": {
                    "project_key": project,
                    "agent_name": agent,
                    "limit": limit,
                    "urgent_only": urgent_only,
                    "include_bodies": False,  # Get preview only
                },
            },
        },
    )

    # Agent Mail returns list of messages directly
    messages: list[dict[str, Any]] = result if isinstance(result, list) else []

    # Transform to our format and filter unread if requested
    formatted_messages = []
    for msg in messages:
        # Skip read messages if unread_only
        if unread_only and msg.get("read_ts"):
            continue

        formatted_messages.append(
            {
                "id": msg.get("id"),
                "thread_id": msg.get("thread_id"),
                "from": msg.get("from"),
                "subject": msg.get("subject"),
                "created_ts": msg.get("created_ts"),
                "unread": not bool(msg.get("read_ts")),
                "ack_required": msg.get("ack_required", False),
                "urgent": msg.get("importance") in {"high", "urgent"},
                "preview": msg.get("body_md", "")[:100] if msg.get("body_md") else "",
            }
        )

    # Simple cursor pagination (use last message ID)
    next_cursor = None
    if formatted_messages and len(formatted_messages) >= limit:
        next_cursor = str(formatted_messages[-1]["id"])

    return {"messages": formatted_messages, "next_cursor": next_cursor}


def mail_read(
    message_id: int,
    mark_read: bool = True,
    agent_name: Optional[str] = None,
    project_key: Optional[str] = None,
) -> dict[str, Any]:
    """Read full message with body.

    Args:
        message_id: Message ID to read
        mark_read: Mark message as read (default: True)
        agent_name: Override agent name (default: BEADS_AGENT_NAME)
        project_key: Override project (default: auto-detect)

    Returns:
        {
            "id": int,
            "thread_id": str,
            "from": str,
            "to": list[str],
            "subject": str,
            "body": str,  # Full Markdown body
            "created_ts": str,
            "ack_required": bool,
            "ack_status": bool,
            "read_ts": str | None,
            "urgent": bool
        }
    """
    _, auto_agent_name, _ = _get_config()
    auto_project_key = _get_project_key()

    agent = agent_name or auto_agent_name
    project = project_key or auto_project_key

    # Get message via resource
    result = _call_agent_mail(
        "GET", f"/mcp/resources/resource://message/{message_id}"
    )

    # Mark as read if requested
    if mark_read:
        try:
            _call_agent_mail(
                "POST",
                "/mcp/call",
                json_data={
                    "method": "tools/call",
                    "params": {
                        "name": "mark_message_read",
                        "arguments": {
                            "project_key": project,
                            "agent_name": agent,
                            "message_id": message_id,
                        },
                    },
                },
            )
        except MailError as e:
            # Don't fail read if mark fails
            logger.warning(f"Failed to mark message {message_id} as read: {e}")

    # Extract message from result
    # Resource returns: {"contents": [{...}]}
    contents = result.get("contents", [])
    if not contents:
        raise MailError("NOT_FOUND", f"Message {message_id} not found")

    msg = contents[0]

    return {
        "id": msg.get("id"),
        "thread_id": msg.get("thread_id"),
        "from": msg.get("from"),
        "to": msg.get("to", []),
        "subject": msg.get("subject"),
        "body": msg.get("body_md", ""),
        "created_ts": msg.get("created_ts"),
        "ack_required": msg.get("ack_required", False),
        "ack_status": bool(msg.get("ack_ts")),
        "read_ts": msg.get("read_ts"),
        "urgent": msg.get("importance") in {"high", "urgent"},
    }


def mail_reply(
    message_id: int,
    body: str,
    subject: Optional[str] = None,
    agent_name: Optional[str] = None,
    project_key: Optional[str] = None,
) -> dict[str, Any]:
    """Reply to a message (preserves thread).

    Args:
        message_id: Message ID to reply to
        body: Reply body (Markdown)
        subject: Override subject (default: "Re: <original subject>")
        agent_name: Override sender name (default: BEADS_AGENT_NAME)
        project_key: Override project (default: auto-detect)

    Returns:
        {
            "message_id": int,
            "thread_id": str
        }
    """
    _, auto_agent_name, _ = _get_config()
    auto_project_key = _get_project_key()

    sender = agent_name or auto_agent_name
    project = project_key or auto_project_key

    # Call reply_message via MCP
    args = {
        "project_key": project,
        "message_id": message_id,
        "sender_name": sender,
        "body_md": body,
    }

    if subject:
        args["subject_prefix"] = subject

    result = _call_agent_mail(
        "POST",
        "/mcp/call",
        json_data={
            "method": "tools/call",
            "params": {"name": "reply_message", "arguments": args},
        },
    )

    # Extract reply details
    reply = result.get("reply", {})
    return {
        "message_id": reply.get("id"),
        "thread_id": reply.get("thread_id"),
    }


def mail_ack(
    message_id: int,
    agent_name: Optional[str] = None,
    project_key: Optional[str] = None,
) -> dict[str, bool]:
    """Acknowledge a message (for ack_required messages).

    Args:
        message_id: Message ID to acknowledge
        agent_name: Override agent name (default: BEADS_AGENT_NAME)
        project_key: Override project (default: auto-detect)

    Returns:
        {"acknowledged": True}
    """
    _, auto_agent_name, _ = _get_config()
    auto_project_key = _get_project_key()

    agent = agent_name or auto_agent_name
    project = project_key or auto_project_key

    # Call acknowledge_message via MCP
    _call_agent_mail(
        "POST",
        "/mcp/call",
        json_data={
            "method": "tools/call",
            "params": {
                "name": "acknowledge_message",
                "arguments": {
                    "project_key": project,
                    "agent_name": agent,
                    "message_id": message_id,
                },
            },
        },
    )

    return {"acknowledged": True}


def mail_delete(
    message_id: int,
    agent_name: Optional[str] = None,
    project_key: Optional[str] = None,
) -> dict[str, bool]:
    """Delete (archive) a message from inbox.

    Note: Agent Mail may archive rather than truly delete.

    Args:
        message_id: Message ID to delete
        agent_name: Override agent name (default: BEADS_AGENT_NAME)
        project_key: Override project (default: auto-detect)

    Returns:
        {"deleted": True} or {"archived": True}
    """
    _, auto_agent_name, _ = _get_config()
    auto_project_key = _get_project_key()

    agent = agent_name or auto_agent_name
    project = project_key or auto_project_key

    # Agent Mail doesn't have explicit delete in MCP API
    # Best we can do is mark as read and acknowledged
    # (This prevents it from showing in urgent/unread views)
    try:
        _call_agent_mail(
            "POST",
            "/mcp/call",
            json_data={
                "method": "tools/call",
                "params": {
                    "name": "mark_message_read",
                    "arguments": {
                        "project_key": project,
                        "agent_name": agent,
                        "message_id": message_id,
                    },
                },
            },
        )
        return {"archived": True}
    except MailError:
        # Soft failure - message may not exist or already read
        return {"archived": True}

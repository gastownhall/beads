"""MCP tools for Agent Mail messaging."""

import logging
from typing import Annotated, Any

from .mail import (
    MailError,
    mail_ack,
    mail_delete,
    mail_inbox,
    mail_read,
    mail_reply,
    mail_send,
)
from .models import (
    MailAckParams,
    MailDeleteParams,
    MailInboxParams,
    MailReadParams,
    MailReplyParams,
    MailSendParams,
)

logger = logging.getLogger(__name__)


def beads_mail_send(params: MailSendParams) -> dict[str, Any]:
    """Send a message to other agents via Agent Mail.

    Requires BEADS_AGENT_MAIL_URL and BEADS_AGENT_NAME environment variables.
    Auto-detects project from workspace root.

    Example:
        mail_send(to=["alice"], subject="Review PR", body="Can you review PR #42?")

    Args:
        params: Message parameters (to, subject, body, etc.)

    Returns:
        {message_id: int, thread_id: str, sent_to: int}

    Raises:
        MailError: On configuration or delivery error
    """
    try:
        return mail_send(
            to=params.to,
            subject=params.subject,
            body=params.body,
            urgent=params.urgent,
            cc=params.cc,
            project_key=params.project_key,
            sender_name=params.sender_name,
        )
    except MailError as e:
        logger.error(f"mail_send failed: {e.message}")
        return {"error": e.code, "message": e.message, "data": e.data}


def beads_mail_inbox(
    params: Annotated[MailInboxParams, "Parameters"] = MailInboxParams(),
) -> dict[str, Any]:
    """Get messages from Agent Mail inbox.

    Requires BEADS_AGENT_MAIL_URL and BEADS_AGENT_NAME environment variables.

    Example:
        mail_inbox(limit=10, unread_only=True)

    Args:
        params: Inbox filter parameters

    Returns:
        {
            messages: [{id, thread_id, from, subject, created_ts, unread, ack_required, urgent, preview}, ...],
            next_cursor: str | None
        }

    Raises:
        MailError: On configuration or fetch error
    """
    try:
        return mail_inbox(
            limit=params.limit,
            urgent_only=params.urgent_only,
            unread_only=params.unread_only,
            cursor=params.cursor,
            agent_name=params.agent_name,
            project_key=params.project_key,
        )
    except MailError as e:
        logger.error(f"mail_inbox failed: {e.message}")
        return {"error": e.code, "message": e.message, "data": e.data}


def beads_mail_read(params: MailReadParams) -> dict[str, Any]:
    """Read full message with body from Agent Mail.

    By default, marks the message as read. Set mark_read=False to preview without marking.

    Example:
        mail_read(message_id=123)

    Args:
        params: Read parameters (message_id, mark_read)

    Returns:
        {
            id, thread_id, from, to, subject, body,
            created_ts, ack_required, ack_status, read_ts, urgent
        }

    Raises:
        MailError: On configuration or read error
    """
    try:
        return mail_read(
            message_id=params.message_id,
            mark_read=params.mark_read,
            agent_name=params.agent_name,
            project_key=params.project_key,
        )
    except MailError as e:
        logger.error(f"mail_read failed: {e.message}")
        return {"error": e.code, "message": e.message, "data": e.data}


def beads_mail_reply(params: MailReplyParams) -> dict[str, Any]:
    """Reply to a message (preserves thread).

    Automatically inherits thread_id from the original message.

    Example:
        mail_reply(message_id=123, body="Thanks, will review today!")

    Args:
        params: Reply parameters (message_id, body, subject)

    Returns:
        {message_id: int, thread_id: str}

    Raises:
        MailError: On configuration or reply error
    """
    try:
        return mail_reply(
            message_id=params.message_id,
            body=params.body,
            subject=params.subject,
            agent_name=params.agent_name,
            project_key=params.project_key,
        )
    except MailError as e:
        logger.error(f"mail_reply failed: {e.message}")
        return {"error": e.code, "message": e.message, "data": e.data}


def beads_mail_ack(params: MailAckParams) -> dict[str, Any]:
    """Acknowledge a message (for ack_required messages).

    Safe to call even if message doesn't require acknowledgement.

    Example:
        mail_ack(message_id=123)

    Args:
        params: Acknowledgement parameters (message_id)

    Returns:
        {acknowledged: True}

    Raises:
        MailError: On configuration or ack error
    """
    try:
        return mail_ack(
            message_id=params.message_id,
            agent_name=params.agent_name,
            project_key=params.project_key,
        )
    except MailError as e:
        logger.error(f"mail_ack failed: {e.message}")
        return {"error": e.code, "acknowledged": False, "message": e.message}


def beads_mail_delete(params: MailDeleteParams) -> dict[str, Any]:
    """Delete (archive) a message from Agent Mail inbox.

    Note: Agent Mail archives messages rather than permanently deleting them.

    Example:
        mail_delete(message_id=123)

    Args:
        params: Delete parameters (message_id)

    Returns:
        {deleted: True} or {archived: True}

    Raises:
        MailError: On configuration or delete error
    """
    try:
        return mail_delete(
            message_id=params.message_id,
            agent_name=params.agent_name,
            project_key=params.project_key,
        )
    except MailError as e:
        logger.error(f"mail_delete failed: {e.message}")
        return {"error": e.code, "deleted": False, "message": e.message}

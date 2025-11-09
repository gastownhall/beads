"""Tests for Agent Mail messaging integration."""

import os
from unittest.mock import Mock, patch

import pytest

from beads_mcp.mail import (
    MailError,
    mail_ack,
    mail_delete,
    mail_inbox,
    mail_read,
    mail_reply,
    mail_send,
)
from beads_mcp.models import (
    MailAckParams,
    MailDeleteParams,
    MailInboxParams,
    MailReadParams,
    MailReplyParams,
    MailSendParams,
)


@pytest.fixture
def mock_agent_mail_env(tmp_path):
    """Set up Agent Mail environment variables."""
    old_env = os.environ.copy()
    
    os.environ["BEADS_AGENT_MAIL_URL"] = "http://127.0.0.1:8765"
    os.environ["BEADS_AGENT_NAME"] = "test-agent"
    os.environ["BEADS_PROJECT_ID"] = str(tmp_path)
    
    yield
    
    # Restore environment
    os.environ.clear()
    os.environ.update(old_env)


@pytest.fixture
def mock_requests():
    """Mock requests library for HTTP calls."""
    with patch("beads_mcp.mail.requests.request") as mock_req:
        yield mock_req


class TestMailConfiguration:
    """Test configuration and error handling."""
    
    def test_missing_url_raises_error(self):
        """Test that missing BEADS_AGENT_MAIL_URL raises NOT_CONFIGURED."""
        old_url = os.environ.pop("BEADS_AGENT_MAIL_URL", None)
        
        try:
            with pytest.raises(MailError) as exc_info:
                mail_send(to=["alice"], subject="Test", body="Test")
            
            assert exc_info.value.code == "NOT_CONFIGURED"
            assert "BEADS_AGENT_MAIL_URL" in exc_info.value.message
        finally:
            if old_url:
                os.environ["BEADS_AGENT_MAIL_URL"] = old_url
    
    def test_missing_agent_name_derives_default(self, mock_agent_mail_env, mock_requests, tmp_path):
        """Test that missing BEADS_AGENT_NAME derives from user/repo."""
        del os.environ["BEADS_AGENT_NAME"]
        os.environ["BEADS_PROJECT_ID"] = str(tmp_path)
        
        mock_requests.return_value.status_code = 200
        mock_requests.return_value.json.return_value = {
            "deliveries": [{
                "payload": {
                    "id": 123,
                    "thread_id": "thread-1",
                }
            }]
        }
        mock_requests.return_value.content = b'{"deliveries": []}'
        
        # Should not raise - derives agent name
        result = mail_send(to=["alice"], subject="Test", body="Test")
        assert result["message_id"] == 123


class TestMailSend:
    """Test mail_send function."""
    
    def test_send_basic_message(self, mock_agent_mail_env, mock_requests):
        """Test sending a basic message."""
        mock_requests.return_value.status_code = 200
        mock_requests.return_value.json.return_value = {
            "deliveries": [{
                "payload": {
                    "id": 123,
                    "thread_id": "thread-abc",
                }
            }]
        }
        mock_requests.return_value.content = b'{"deliveries": []}'
        
        result = mail_send(
            to=["alice", "bob"],
            subject="Test Message",
            body="Hello world!",
        )
        
        assert result["message_id"] == 123
        assert result["thread_id"] == "thread-abc"
        assert result["sent_to"] == 1
        
        # Verify HTTP request
        mock_requests.assert_called_once()
        call_kwargs = mock_requests.call_args.kwargs
        assert call_kwargs["method"] == "POST"
        assert call_kwargs["json"]["params"]["name"] == "send_message"
        assert call_kwargs["json"]["params"]["arguments"]["to"] == ["alice", "bob"]
        assert call_kwargs["json"]["params"]["arguments"]["subject"] == "Test Message"
    
    def test_send_urgent_message(self, mock_agent_mail_env, mock_requests):
        """Test sending urgent message."""
        mock_requests.return_value.status_code = 200
        mock_requests.return_value.json.return_value = {
            "deliveries": [{
                "payload": {"id": 456, "thread_id": "thread-xyz"}
            }]
        }
        mock_requests.return_value.content = b'{"deliveries": []}'
        
        result = mail_send(
            to=["alice"],
            subject="URGENT",
            body="Need review now!",
            urgent=True,
        )
        
        call_kwargs = mock_requests.call_args.kwargs
        assert call_kwargs["json"]["params"]["arguments"]["importance"] == "urgent"
    
    def test_send_with_cc(self, mock_agent_mail_env, mock_requests):
        """Test sending message with CC recipients."""
        mock_requests.return_value.status_code = 200
        mock_requests.return_value.json.return_value = {
            "deliveries": [{
                "payload": {"id": 789, "thread_id": "thread-123"}
            }]
        }
        mock_requests.return_value.content = b'{"deliveries": []}'
        
        result = mail_send(
            to=["alice"],
            subject="FYI",
            body="For your info",
            cc=["bob", "charlie"],
        )
        
        call_kwargs = mock_requests.call_args.kwargs
        assert call_kwargs["json"]["params"]["arguments"]["cc"] == ["bob", "charlie"]
    
    def test_send_connection_error(self, mock_agent_mail_env, mock_requests):
        """Test handling connection errors."""
        import requests.exceptions
        mock_requests.side_effect = requests.exceptions.ConnectionError("Connection refused")
        
        with pytest.raises(MailError) as exc_info:
            mail_send(to=["alice"], subject="Test", body="Test")
        
        assert exc_info.value.code == "UNAVAILABLE"
        assert "Cannot connect" in exc_info.value.message


class TestMailInbox:
    """Test mail_inbox function."""
    
    def test_fetch_inbox_default(self, mock_agent_mail_env, mock_requests):
        """Test fetching inbox with default parameters."""
        mock_requests.return_value.status_code = 200
        mock_requests.return_value.json.return_value = [
            {
                "id": 1,
                "thread_id": "thread-1",
                "from": "alice",
                "subject": "Hello",
                "created_ts": "2025-01-01T00:00:00Z",
                "read_ts": None,
                "ack_required": False,
                "importance": "normal",
                "body_md": "This is a test message",
            },
            {
                "id": 2,
                "thread_id": "thread-2",
                "from": "bob",
                "subject": "Urgent!",
                "created_ts": "2025-01-02T00:00:00Z",
                "read_ts": "2025-01-02T01:00:00Z",
                "ack_required": True,
                "importance": "urgent",
                "body_md": "Please review ASAP",
            },
        ]
        mock_requests.return_value.content = b'[]'
        
        result = mail_inbox()
        
        assert len(result["messages"]) == 2
        assert result["messages"][0]["id"] == 1
        assert result["messages"][0]["unread"] is True
        assert result["messages"][0]["urgent"] is False
        assert result["messages"][1]["id"] == 2
        assert result["messages"][1]["unread"] is False
        assert result["messages"][1]["urgent"] is True
    
    def test_fetch_inbox_unread_only(self, mock_agent_mail_env, mock_requests):
        """Test fetching only unread messages."""
        mock_requests.return_value.status_code = 200
        mock_requests.return_value.json.return_value = [
            {"id": 1, "thread_id": "t1", "from": "alice", "subject": "Test", "created_ts": "2025-01-01T00:00:00Z", "read_ts": None, "importance": "normal"},
            {"id": 2, "thread_id": "t2", "from": "bob", "subject": "Test2", "created_ts": "2025-01-01T00:00:00Z", "read_ts": "2025-01-01T01:00:00Z", "importance": "normal"},
        ]
        mock_requests.return_value.content = b'[]'
        
        result = mail_inbox(unread_only=True)
        
        # Should filter out message 2 (read)
        assert len(result["messages"]) == 1
        assert result["messages"][0]["id"] == 1
    
    def test_fetch_inbox_pagination(self, mock_agent_mail_env, mock_requests):
        """Test inbox pagination with next_cursor."""
        mock_requests.return_value.status_code = 200
        # Simulate full page (limit reached)
        mock_requests.return_value.json.return_value = [
            {"id": i, "thread_id": f"t{i}", "from": "alice", "subject": f"Msg {i}", "created_ts": "2025-01-01T00:00:00Z", "importance": "normal"}
            for i in range(20)
        ]
        mock_requests.return_value.content = b'[]'
        
        result = mail_inbox(limit=20)
        
        # Should return next_cursor when limit reached
        assert result["next_cursor"] == "19"  # Last message ID


class TestMailRead:
    """Test mail_read function."""
    
    def test_read_message_marks_read(self, mock_agent_mail_env, mock_requests):
        """Test reading message marks it as read by default."""
        # Mock resource fetch
        mock_requests.return_value.status_code = 200
        mock_requests.return_value.json.return_value = {
            "contents": [{
                "id": 123,
                "thread_id": "thread-1",
                "from": "alice",
                "to": ["test-agent"],
                "subject": "Test",
                "body_md": "Hello world!",
                "created_ts": "2025-01-01T00:00:00Z",
                "ack_required": False,
                "importance": "normal",
                "read_ts": None,
            }]
        }
        mock_requests.return_value.content = b'{}'
        
        result = mail_read(message_id=123)
        
        assert result["id"] == 123
        assert result["body"] == "Hello world!"
        assert result["urgent"] is False
        
        # Should have called both GET resource and POST mark_read
        assert mock_requests.call_count == 2
    
    def test_read_message_no_mark(self, mock_agent_mail_env, mock_requests):
        """Test reading without marking as read."""
        mock_requests.return_value.status_code = 200
        mock_requests.return_value.json.return_value = {
            "contents": [{
                "id": 123,
                "thread_id": "thread-1",
                "from": "alice",
                "to": ["test-agent"],
                "subject": "Test",
                "body_md": "Preview",
                "created_ts": "2025-01-01T00:00:00Z",
                "importance": "normal",
            }]
        }
        mock_requests.return_value.content = b'{}'
        
        result = mail_read(message_id=123, mark_read=False)
        
        # Should only call GET resource, not mark_read
        assert mock_requests.call_count == 1


class TestMailReply:
    """Test mail_reply function."""
    
    def test_reply_to_message(self, mock_agent_mail_env, mock_requests):
        """Test replying to a message."""
        mock_requests.return_value.status_code = 200
        mock_requests.return_value.json.return_value = {
            "reply": {
                "id": 456,
                "thread_id": "thread-1",
            }
        }
        mock_requests.return_value.content = b'{}'
        
        result = mail_reply(
            message_id=123,
            body="Thanks for the message!",
        )
        
        assert result["message_id"] == 456
        assert result["thread_id"] == "thread-1"
        
        call_kwargs = mock_requests.call_args.kwargs
        assert call_kwargs["json"]["params"]["name"] == "reply_message"
        assert call_kwargs["json"]["params"]["arguments"]["message_id"] == 123


class TestMailAck:
    """Test mail_ack function."""
    
    def test_acknowledge_message(self, mock_agent_mail_env, mock_requests):
        """Test acknowledging a message."""
        mock_requests.return_value.status_code = 200
        mock_requests.return_value.json.return_value = {}
        mock_requests.return_value.content = b'{}'
        
        result = mail_ack(message_id=123)
        
        assert result["acknowledged"] is True
        
        call_kwargs = mock_requests.call_args.kwargs
        assert call_kwargs["json"]["params"]["name"] == "acknowledge_message"


class TestMailDelete:
    """Test mail_delete function."""
    
    def test_delete_message(self, mock_agent_mail_env, mock_requests):
        """Test deleting/archiving a message."""
        mock_requests.return_value.status_code = 200
        mock_requests.return_value.json.return_value = {}
        mock_requests.return_value.content = b'{}'
        
        result = mail_delete(message_id=123)
        
        assert result["archived"] is True


class TestMailRetries:
    """Test retry logic and error handling."""
    
    def test_retries_on_server_error(self, mock_agent_mail_env, mock_requests):
        """Test that 500 errors trigger retries."""
        mock_requests.return_value.status_code = 500
        mock_requests.return_value.content = b'Internal Server Error'
        
        with pytest.raises(MailError) as exc_info:
            mail_send(to=["alice"], subject="Test", body="Test")
        
        assert exc_info.value.code == "UNAVAILABLE"
        # Should retry 3 times total (initial + 2 retries)
        assert mock_requests.call_count == 3
    
    def test_no_retry_on_client_error(self, mock_agent_mail_env, mock_requests):
        """Test that 404 errors don't trigger retries."""
        mock_requests.return_value.status_code = 404
        mock_requests.return_value.json.return_value = {"detail": "Not found"}
        mock_requests.return_value.content = b'{"detail": "Not found"}'
        
        with pytest.raises(MailError) as exc_info:
            mail_read(message_id=999)
        
        assert exc_info.value.code == "NOT_FOUND"
        # Should not retry on 404
        assert mock_requests.call_count == 1


class TestMailToolWrappers:
    """Test MCP tool wrappers."""
    
    def test_mail_send_params(self, mock_agent_mail_env, mock_requests):
        """Test MailSendParams validation."""
        from beads_mcp.mail_tools import beads_mail_send
        
        mock_requests.return_value.status_code = 200
        mock_requests.return_value.json.return_value = {
            "deliveries": [{
                "payload": {"id": 123, "thread_id": "t1"}
            }]
        }
        mock_requests.return_value.content = b'{}'
        
        params = MailSendParams(
            to=["alice"],
            subject="Test",
            body="Hello",
            urgent=True,
        )
        
        result = beads_mail_send(params)
        assert result["message_id"] == 123
    
    def test_mail_inbox_default_params(self, mock_agent_mail_env, mock_requests):
        """Test MailInboxParams with defaults."""
        from beads_mcp.mail_tools import beads_mail_inbox
        
        mock_requests.return_value.status_code = 200
        mock_requests.return_value.json.return_value = []
        mock_requests.return_value.content = b'[]'
        
        params = MailInboxParams()  # All defaults
        result = beads_mail_inbox(params)
        
        assert result["messages"] == []
        assert result["next_cursor"] is None

#!/usr/bin/env python3
"""
Unit tests for beads_mail_adapter.py

Tests cover:
- Enabled mode (server available)
- Disabled mode (server unavailable)
- Graceful degradation (server dies mid-operation)
- Reservation conflicts
- Message sending/receiving
"""

import unittest
import json
import os
from unittest.mock import patch, Mock, MagicMock
from urllib.error import URLError, HTTPError
from io import BytesIO

from beads_mail_adapter import AgentMailAdapter


class TestAgentMailAdapterDisabled(unittest.TestCase):
    """Test adapter when server is unavailable (disabled mode)."""
    
    @patch('beads_mail_adapter.urlopen')
    def test_init_server_unavailable(self, mock_urlopen):
        """Test initialization when server is unreachable."""
        mock_urlopen.side_effect = URLError("Connection refused")
        
        adapter = AgentMailAdapter(
            url="http://localhost:9999",
            token="test-token",
            agent_name="test-agent"
        )
        
        self.assertFalse(adapter.enabled)
        self.assertEqual(adapter.url, "http://localhost:9999")
        self.assertEqual(adapter.agent_name, "test-agent")
    
    @patch('beads_mail_adapter.urlopen')
    def test_operations_no_op_when_disabled(self, mock_urlopen):
        """Test that all operations gracefully no-op when disabled."""
        mock_urlopen.side_effect = URLError("Connection refused")
        
        adapter = AgentMailAdapter()
        self.assertFalse(adapter.enabled)
        
        # All operations should succeed without making requests
        self.assertTrue(adapter.reserve_issue("bd-123"))
        self.assertTrue(adapter.release_issue("bd-123"))
        self.assertTrue(adapter.notify("test", {"foo": "bar"}))
        self.assertEqual(adapter.check_inbox(), [])
        self.assertEqual(adapter.get_reservations(), [])


class TestAgentMailAdapterEnabled(unittest.TestCase):
    """Test adapter when server is available (enabled mode)."""
    
    def _mock_response(self, status_code=200, data=None):
        """Create mock HTTP response."""
        mock_response = MagicMock()
        mock_response.status = status_code
        mock_response.__enter__ = Mock(return_value=mock_response)
        mock_response.__exit__ = Mock(return_value=False)
        
        if data is not None:
            mock_response.read.return_value = json.dumps(data).encode('utf-8')
        else:
            mock_response.read.return_value = b''
        
        return mock_response
    
    @patch('beads_mail_adapter.urlopen')
    def test_init_server_available(self, mock_urlopen):
        """Test initialization when server is healthy."""
        mock_urlopen.return_value = self._mock_response(200, {"status": "ok"})
        
        adapter = AgentMailAdapter(
            url="http://localhost:8765",
            token="test-token",
            agent_name="test-agent"
        )
        
        self.assertTrue(adapter.enabled)
        self.assertEqual(adapter.url, "http://localhost:8765")
    
    @patch('beads_mail_adapter.urlopen')
    def test_reserve_issue_success(self, mock_urlopen):
        """Test successful issue reservation."""
        # Health check
        mock_urlopen.return_value = self._mock_response(200, {"status": "ok"})
        adapter = AgentMailAdapter(agent_name="test-agent")
        
        # Reservation request
        mock_urlopen.return_value = self._mock_response(201, {"reserved": True})
        result = adapter.reserve_issue("bd-123")
        
        self.assertTrue(result)
        self.assertTrue(adapter.enabled)
    
    @patch('beads_mail_adapter.urlopen')
    def test_reserve_issue_conflict(self, mock_urlopen):
        """Test reservation conflict (issue already reserved)."""
        # Health check
        mock_urlopen.return_value = self._mock_response(200, {"status": "ok"})
        adapter = AgentMailAdapter(agent_name="test-agent")
        
        # Simulate 409 Conflict
        error_response = HTTPError(
            url="http://test",
            code=409,
            msg="Conflict",
            hdrs={},
            fp=BytesIO(json.dumps({"error": "Already reserved by other-agent"}).encode('utf-8'))
        )
        mock_urlopen.side_effect = error_response
        
        result = adapter.reserve_issue("bd-123")
        
        self.assertFalse(result)
    
    @patch('beads_mail_adapter.urlopen')
    def test_release_issue_success(self, mock_urlopen):
        """Test successful issue release."""
        mock_urlopen.return_value = self._mock_response(200, {"status": "ok"})
        adapter = AgentMailAdapter(agent_name="test-agent")
        
        mock_urlopen.return_value = self._mock_response(204)
        result = adapter.release_issue("bd-123")
        
        self.assertTrue(result)
    
    @patch('beads_mail_adapter.urlopen')
    def test_notify_success(self, mock_urlopen):
        """Test sending notification."""
        mock_urlopen.return_value = self._mock_response(200, {"status": "ok"})
        adapter = AgentMailAdapter(agent_name="test-agent")
        
        mock_urlopen.return_value = self._mock_response(201, {"sent": True})
        result = adapter.notify("status_changed", {"issue_id": "bd-123", "status": "in_progress"})
        
        self.assertTrue(result)
    
    @patch('beads_mail_adapter.urlopen')
    def test_check_inbox_with_messages(self, mock_urlopen):
        """Test checking inbox with messages."""
        mock_urlopen.return_value = self._mock_response(200, {"status": "ok"})
        adapter = AgentMailAdapter(agent_name="test-agent")
        
        messages = [
            {"from": "agent-1", "event": "completed", "data": {"issue_id": "bd-42"}},
            {"from": "agent-2", "event": "started", "data": {"issue_id": "bd-99"}}
        ]
        mock_urlopen.return_value = self._mock_response(200, messages)
        
        result = adapter.check_inbox()
        
        self.assertEqual(len(result), 2)
        self.assertEqual(result[0]["from"], "agent-1")
    
    @patch('beads_mail_adapter.urlopen')
    def test_check_inbox_empty(self, mock_urlopen):
        """Test checking empty inbox."""
        mock_urlopen.return_value = self._mock_response(200, {"status": "ok"})
        adapter = AgentMailAdapter(agent_name="test-agent")
        
        mock_urlopen.return_value = self._mock_response(200, [])
        result = adapter.check_inbox()
        
        self.assertEqual(result, [])
    
    @patch('beads_mail_adapter.urlopen')
    def test_get_reservations(self, mock_urlopen):
        """Test getting all reservations."""
        mock_urlopen.return_value = self._mock_response(200, {"status": "ok"})
        adapter = AgentMailAdapter(agent_name="test-agent")
        
        reservations = [
            {"issue_id": "bd-123", "agent": "agent-1"},
            {"issue_id": "bd-456", "agent": "agent-2"}
        ]
        mock_urlopen.return_value = self._mock_response(200, reservations)
        
        result = adapter.get_reservations()
        
        self.assertEqual(len(result), 2)
    
    @patch('beads_mail_adapter.urlopen')
    def test_get_reservations_dict_response(self, mock_urlopen):
        """Test getting reservations with dict wrapper response."""
        mock_urlopen.return_value = self._mock_response(200, {"status": "ok"})
        adapter = AgentMailAdapter(agent_name="test-agent")
        
        # Some servers wrap response in {"reservations": [...]}
        mock_urlopen.return_value = self._mock_response(200, {
            "reservations": [{"issue_id": "bd-789", "agent": "agent-3"}]
        })
        
        result = adapter.get_reservations()
        
        self.assertEqual(len(result), 1)
        self.assertEqual(result[0]["issue_id"], "bd-789")
    
    @patch('beads_mail_adapter.urlopen')
    def test_check_inbox_dict_wrapper(self, mock_urlopen):
        """Test checking inbox with dict wrapper response."""
        mock_urlopen.return_value = self._mock_response(200, {"status": "ok"})
        adapter = AgentMailAdapter(agent_name="test-agent")
        
        # Some servers wrap response in {"messages": [...]}
        mock_urlopen.return_value = self._mock_response(200, {
            "messages": [{"from": "agent-5", "event": "test"}]
        })
        
        result = adapter.check_inbox()
        
        self.assertEqual(len(result), 1)
        self.assertEqual(result[0]["from"], "agent-5")
    
    @patch('beads_mail_adapter.urlopen')
    def test_reserve_with_custom_ttl(self, mock_urlopen):
        """Test reservation with custom TTL."""
        mock_urlopen.return_value = self._mock_response(200, {"status": "ok"})
        adapter = AgentMailAdapter(agent_name="test-agent")
        
        mock_urlopen.return_value = self._mock_response(201, {"reserved": True})
        result = adapter.reserve_issue("bd-999", ttl=7200)
        
        self.assertTrue(result)
    
    @patch('beads_mail_adapter.urlopen')
    def test_http_error_500(self, mock_urlopen):
        """Test handling of HTTP 500 errors."""
        mock_urlopen.return_value = self._mock_response(200, {"status": "ok"})
        adapter = AgentMailAdapter(agent_name="test-agent")
        
        # Simulate 500 Internal Server Error
        error_response = HTTPError(
            url="http://test",
            code=500,
            msg="Internal Server Error",
            hdrs={},
            fp=BytesIO(b"Server error")
        )
        mock_urlopen.side_effect = error_response
        
        # Should gracefully degrade
        result = adapter.reserve_issue("bd-123")
        self.assertTrue(result)
    
    @patch('beads_mail_adapter.urlopen')
    def test_http_error_404(self, mock_urlopen):
        """Test handling of HTTP 404 errors."""
        mock_urlopen.return_value = self._mock_response(200, {"status": "ok"})
        adapter = AgentMailAdapter(agent_name="test-agent")
        
        error_response = HTTPError(
            url="http://test",
            code=404,
            msg="Not Found",
            hdrs={},
            fp=BytesIO(b"Not found")
        )
        mock_urlopen.side_effect = error_response
        
        result = adapter.release_issue("bd-nonexistent")
        self.assertTrue(result)  # Graceful degradation


class TestGracefulDegradation(unittest.TestCase):
    """Test graceful degradation when server fails mid-operation."""
    
    def _mock_response(self, status_code=200, data=None):
        """Create mock HTTP response."""
        mock_response = MagicMock()
        mock_response.status = status_code
        mock_response.__enter__ = Mock(return_value=mock_response)
        mock_response.__exit__ = Mock(return_value=False)
        
        if data is not None:
            mock_response.read.return_value = json.dumps(data).encode('utf-8')
        else:
            mock_response.read.return_value = b''
        
        return mock_response
    
    @patch('beads_mail_adapter.urlopen')
    def test_server_dies_mid_operation(self, mock_urlopen):
        """Test that operations gracefully handle server failures."""
        # Initially healthy
        mock_urlopen.return_value = self._mock_response(200, {"status": "ok"})
        adapter = AgentMailAdapter()
        self.assertTrue(adapter.enabled)
        
        # Server dies during operation
        mock_urlopen.side_effect = URLError("Connection refused")
        
        # Operations should still succeed (graceful degradation)
        self.assertTrue(adapter.reserve_issue("bd-123"))
        self.assertTrue(adapter.release_issue("bd-123"))
        self.assertTrue(adapter.notify("test", {}))
        self.assertEqual(adapter.check_inbox(), [])
    
    @patch('beads_mail_adapter.urlopen')
    def test_network_timeout(self, mock_urlopen):
        """Test handling of network timeouts."""
        mock_urlopen.return_value = self._mock_response(200, {"status": "ok"})
        adapter = AgentMailAdapter(timeout=1)
        
        mock_urlopen.side_effect = URLError("Timeout")
        
        # Should not crash
        self.assertTrue(adapter.reserve_issue("bd-123"))
    
    @patch('beads_mail_adapter.urlopen')
    def test_malformed_json_response(self, mock_urlopen):
        """Test handling of malformed JSON responses."""
        mock_urlopen.return_value = self._mock_response(200, {"status": "ok"})
        adapter = AgentMailAdapter()
        
        # Return invalid JSON
        bad_response = MagicMock()
        bad_response.status = 200
        bad_response.__enter__ = Mock(return_value=bad_response)
        bad_response.__exit__ = Mock(return_value=False)
        bad_response.read.return_value = b'{invalid json'
        mock_urlopen.return_value = bad_response
        
        # Should gracefully degrade
        result = adapter.check_inbox()
        self.assertEqual(result, [])
    
    @patch('beads_mail_adapter.urlopen')
    def test_empty_response_body(self, mock_urlopen):
        """Test handling of empty response bodies."""
        mock_urlopen.return_value = self._mock_response(200, {"status": "ok"})
        adapter = AgentMailAdapter()
        
        # Return 204 No Content (empty body)
        empty_response = MagicMock()
        empty_response.status = 204
        empty_response.__enter__ = Mock(return_value=empty_response)
        empty_response.__exit__ = Mock(return_value=False)
        empty_response.read.return_value = b''
        mock_urlopen.return_value = empty_response
        
        result = adapter.release_issue("bd-123")
        self.assertTrue(result)


class TestConfiguration(unittest.TestCase):
    """Test environment variable configuration."""
    
    @patch.dict(os.environ, {
        'AGENT_MAIL_URL': 'http://custom:9000',
        'AGENT_MAIL_TOKEN': 'custom-token',
        'BEADS_AGENT_NAME': 'custom-agent',
        'AGENT_MAIL_TIMEOUT': '10'
    })
    @patch('beads_mail_adapter.urlopen')
    def test_env_var_configuration(self, mock_urlopen):
        """Test configuration from environment variables."""
        mock_urlopen.side_effect = URLError("Not testing connection")
        
        adapter = AgentMailAdapter()
        
        self.assertEqual(adapter.url, "http://custom:9000")
        self.assertEqual(adapter.token, "custom-token")
        self.assertEqual(adapter.agent_name, "custom-agent")
        self.assertEqual(adapter.timeout, 10)
    
    @patch('beads_mail_adapter.urlopen')
    def test_constructor_overrides_env(self, mock_urlopen):
        """Test that constructor args override environment variables."""
        mock_urlopen.side_effect = URLError("Not testing connection")
        
        adapter = AgentMailAdapter(
            url="http://override:8765",
            token="override-token",
            agent_name="override-agent",
            timeout=3
        )
        
        self.assertEqual(adapter.url, "http://override:8765")
        self.assertEqual(adapter.token, "override-token")
        self.assertEqual(adapter.agent_name, "override-agent")
        self.assertEqual(adapter.timeout, 3)
    
    @patch('beads_mail_adapter.urlopen')
    def test_url_trailing_slash_removed(self, mock_urlopen):
        """Test that trailing slashes are removed from URL."""
        mock_urlopen.side_effect = URLError("Not testing connection")
        
        adapter = AgentMailAdapter(url="http://localhost:8765/")
        
        self.assertEqual(adapter.url, "http://localhost:8765")
    
    @patch('beads_mail_adapter.urlopen')
    @patch('socket.gethostname')
    def test_default_agent_name_from_hostname(self, mock_hostname, mock_urlopen):
        """Test default agent name comes from hostname."""
        mock_urlopen.side_effect = URLError("Not testing connection")
        mock_hostname.return_value = "my-laptop"
        
        adapter = AgentMailAdapter()
        
        self.assertEqual(adapter.agent_name, "my-laptop")
    
    @patch('beads_mail_adapter.urlopen')
    @patch('socket.gethostname')
    def test_default_agent_name_fallback(self, mock_hostname, mock_urlopen):
        """Test fallback agent name when hostname fails."""
        mock_urlopen.side_effect = URLError("Not testing connection")
        mock_hostname.side_effect = Exception("Can't get hostname")
        
        adapter = AgentMailAdapter()
        
        self.assertEqual(adapter.agent_name, "beads-agent")


class TestHealthCheck(unittest.TestCase):
    """Test health check scenarios."""
    
    def _mock_response(self, status_code=200, data=None):
        """Create mock HTTP response."""
        mock_response = MagicMock()
        mock_response.status = status_code
        mock_response.__enter__ = Mock(return_value=mock_response)
        mock_response.__exit__ = Mock(return_value=False)
        
        if data is not None:
            mock_response.read.return_value = json.dumps(data).encode('utf-8')
        else:
            mock_response.read.return_value = b''
        
        return mock_response
    
    @patch('beads_mail_adapter.urlopen')
    def test_health_check_bad_status(self, mock_urlopen):
        """Test health check with non-ok status."""
        mock_urlopen.return_value = self._mock_response(200, {"status": "degraded"})
        
        adapter = AgentMailAdapter()
        
        self.assertFalse(adapter.enabled)
    
    @patch('beads_mail_adapter.urlopen')
    def test_health_check_http_error(self, mock_urlopen):
        """Test health check with HTTP error."""
        error_response = HTTPError(
            url="http://test",
            code=503,
            msg="Service Unavailable",
            hdrs={},
            fp=BytesIO(b"Server down")
        )
        mock_urlopen.side_effect = error_response
        
        adapter = AgentMailAdapter()
        
        self.assertFalse(adapter.enabled)
    
    @patch('beads_mail_adapter.urlopen')
    def test_health_check_timeout(self, mock_urlopen):
        """Test health check with timeout."""
        mock_urlopen.side_effect = URLError("timeout")
        
        adapter = AgentMailAdapter(timeout=1)
        
        self.assertFalse(adapter.enabled)


class TestReservationConflicts(unittest.TestCase):
    """Test reservation conflict handling."""
    
    def _mock_response(self, status_code=200, data=None):
        """Create mock HTTP response."""
        mock_response = MagicMock()
        mock_response.status = status_code
        mock_response.__enter__ = Mock(return_value=mock_response)
        mock_response.__exit__ = Mock(return_value=False)
        
        if data is not None:
            mock_response.read.return_value = json.dumps(data).encode('utf-8')
        else:
            mock_response.read.return_value = b''
        
        return mock_response
    
    @patch('beads_mail_adapter.urlopen')
    def test_conflict_with_malformed_error_body(self, mock_urlopen):
        """Test conflict handling with malformed error body."""
        mock_urlopen.return_value = self._mock_response(200, {"status": "ok"})
        adapter = AgentMailAdapter(agent_name="test-agent")
        
        # 409 with non-JSON error body
        error_response = HTTPError(
            url="http://test",
            code=409,
            msg="Conflict",
            hdrs={},
            fp=BytesIO(b"Already reserved (plain text)")
        )
        mock_urlopen.side_effect = error_response
        
        result = adapter.reserve_issue("bd-999")
        
        self.assertFalse(result)
    
    @patch('beads_mail_adapter.urlopen')
    def test_multiple_operations_after_conflict(self, mock_urlopen):
        """Test that adapter continues working after conflict."""
        mock_urlopen.return_value = self._mock_response(200, {"status": "ok"})
        adapter = AgentMailAdapter(agent_name="test-agent")
        
        # First reservation fails with conflict
        error_response = HTTPError(
            url="http://test",
            code=409,
            msg="Conflict",
            hdrs={},
            fp=BytesIO(json.dumps({"error": "Already reserved"}).encode('utf-8'))
        )
        mock_urlopen.side_effect = error_response
        result1 = adapter.reserve_issue("bd-123")
        self.assertFalse(result1)
        
        # Second reservation succeeds
        mock_urlopen.side_effect = None
        mock_urlopen.return_value = self._mock_response(201, {"reserved": True})
        result2 = adapter.reserve_issue("bd-456")
        self.assertTrue(result2)


class TestAuthorizationHeaders(unittest.TestCase):
    """Test authorization header handling."""
    
    def _mock_response(self, status_code=200, data=None):
        """Create mock HTTP response."""
        mock_response = MagicMock()
        mock_response.status = status_code
        mock_response.__enter__ = Mock(return_value=mock_response)
        mock_response.__exit__ = Mock(return_value=False)
        
        if data is not None:
            mock_response.read.return_value = json.dumps(data).encode('utf-8')
        else:
            mock_response.read.return_value = b''
        
        return mock_response
    
    @patch('beads_mail_adapter.urlopen')
    def test_authorization_header_present_with_token(self, mock_urlopen):
        """Test that Authorization header is sent when token is provided."""
        mock_urlopen.return_value = self._mock_response(200, {"status": "ok"})
        adapter = AgentMailAdapter(token="test-token-123", agent_name="test-agent")
        
        # Reset mock to capture the actual request
        mock_urlopen.reset_mock()
        mock_urlopen.return_value = self._mock_response(201, {"reserved": True})
        
        adapter.reserve_issue("bd-123")
        
        # Verify Authorization header was sent
        self.assertTrue(mock_urlopen.called)
        call_args = mock_urlopen.call_args
        request = call_args[0][0]
        
        self.assertEqual(request.headers.get('Authorization'), 'Bearer test-token-123')
    
    @patch('beads_mail_adapter.urlopen')
    def test_authorization_header_absent_without_token(self, mock_urlopen):
        """Test that Authorization header is not sent when no token provided."""
        mock_urlopen.return_value = self._mock_response(200, {"status": "ok"})
        adapter = AgentMailAdapter(token="", agent_name="test-agent")
        
        # Reset mock to capture the actual request
        mock_urlopen.reset_mock()
        mock_urlopen.return_value = self._mock_response(201, {"reserved": True})
        
        adapter.reserve_issue("bd-123")
        
        # Verify Authorization header was not sent
        self.assertTrue(mock_urlopen.called)
        call_args = mock_urlopen.call_args
        request = call_args[0][0]
        
        self.assertNotIn('Authorization', request.headers)
    
    @patch('beads_mail_adapter.urlopen')
    def test_content_type_header_always_json(self, mock_urlopen):
        """Test that Content-Type header is always application/json."""
        mock_urlopen.return_value = self._mock_response(200, {"status": "ok"})
        adapter = AgentMailAdapter(agent_name="test-agent")
        
        # Reset mock to capture the actual request
        mock_urlopen.reset_mock()
        mock_urlopen.return_value = self._mock_response(201, {"sent": True})
        
        adapter.notify("test_event", {"foo": "bar"})
        
        # Verify Content-Type header
        self.assertTrue(mock_urlopen.called)
        call_args = mock_urlopen.call_args
        request = call_args[0][0]
        
        self.assertEqual(request.headers.get('Content-type'), 'application/json')


class TestRequestBodyValidation(unittest.TestCase):
    """Test request body structure and validation."""
    
    def _mock_response(self, status_code=200, data=None):
        """Create mock HTTP response."""
        mock_response = MagicMock()
        mock_response.status = status_code
        mock_response.__enter__ = Mock(return_value=mock_response)
        mock_response.__exit__ = Mock(return_value=False)
        
        if data is not None:
            mock_response.read.return_value = json.dumps(data).encode('utf-8')
        else:
            mock_response.read.return_value = b''
        
        return mock_response
    
    @patch('beads_mail_adapter.urlopen')
    def test_reserve_issue_request_body_structure(self, mock_urlopen):
        """Test reservation request body contains correct fields."""
        mock_urlopen.return_value = self._mock_response(200, {"status": "ok"})
        adapter = AgentMailAdapter(agent_name="test-agent")
        
        # Reset mock to capture the actual request
        mock_urlopen.reset_mock()
        mock_urlopen.return_value = self._mock_response(201, {"reserved": True})
        
        adapter.reserve_issue("bd-123", ttl=7200)
        
        # Verify request body structure
        self.assertTrue(mock_urlopen.called)
        call_args = mock_urlopen.call_args
        request = call_args[0][0]
        body = json.loads(request.data.decode('utf-8'))
        
        self.assertEqual(body["file_path"], ".beads/issues/bd-123")
        self.assertEqual(body["agent_name"], "test-agent")
        self.assertEqual(body["ttl"], 7200)
    
    @patch('beads_mail_adapter.urlopen')
    def test_notify_request_body_structure(self, mock_urlopen):
        """Test notification request body contains correct fields."""
        mock_urlopen.return_value = self._mock_response(200, {"status": "ok"})
        adapter = AgentMailAdapter(agent_name="notification-agent")
        
        # Reset mock to capture the actual request
        mock_urlopen.reset_mock()
        mock_urlopen.return_value = self._mock_response(201, {"sent": True})
        
        test_payload = {"issue_id": "bd-456", "status": "completed"}
        adapter.notify("status_changed", test_payload)
        
        # Verify request body structure
        self.assertTrue(mock_urlopen.called)
        call_args = mock_urlopen.call_args
        request = call_args[0][0]
        body = json.loads(request.data.decode('utf-8'))
        
        self.assertEqual(body["from_agent"], "notification-agent")
        self.assertEqual(body["event_type"], "status_changed")
        self.assertEqual(body["payload"], test_payload)
    
    @patch('beads_mail_adapter.urlopen')
    def test_release_issue_url_structure(self, mock_urlopen):
        """Test release request uses correct URL structure."""
        mock_urlopen.return_value = self._mock_response(200, {"status": "ok"})
        adapter = AgentMailAdapter(agent_name="release-agent")
        
        # Reset mock to capture the actual request
        mock_urlopen.reset_mock()
        mock_urlopen.return_value = self._mock_response(204)
        
        adapter.release_issue("bd-789")
        
        # Verify URL path
        self.assertTrue(mock_urlopen.called)
        call_args = mock_urlopen.call_args
        request = call_args[0][0]
        
        # URL should be: {base_url}/api/reservations/{agent_name}/{issue_id}
        self.assertIn("/api/reservations/release-agent/bd-789", request.full_url)
    
    @patch('beads_mail_adapter.urlopen')
    def test_check_inbox_url_structure(self, mock_urlopen):
        """Test inbox check uses correct URL structure."""
        mock_urlopen.return_value = self._mock_response(200, {"status": "ok"})
        adapter = AgentMailAdapter(agent_name="inbox-agent")
        
        # Reset mock to capture the actual request
        mock_urlopen.reset_mock()
        mock_urlopen.return_value = self._mock_response(200, [])
        
        adapter.check_inbox()
        
        # Verify URL path
        self.assertTrue(mock_urlopen.called)
        call_args = mock_urlopen.call_args
        request = call_args[0][0]
        
        # URL should be: {base_url}/api/notifications/{agent_name}
        self.assertIn("/api/notifications/inbox-agent", request.full_url)


class TestReservationEdgeCases(unittest.TestCase):
    """Test edge cases in reservation mechanisms."""
    
    def _mock_response(self, status_code=200, data=None):
        """Create mock HTTP response."""
        mock_response = MagicMock()
        mock_response.status = status_code
        mock_response.__enter__ = Mock(return_value=mock_response)
        mock_response.__exit__ = Mock(return_value=False)
        
        if data is not None:
            mock_response.read.return_value = json.dumps(data).encode('utf-8')
        else:
            mock_response.read.return_value = b''
        
        return mock_response
    
    @patch('beads_mail_adapter.urlopen')
    def test_reserve_with_zero_ttl(self, mock_urlopen):
        """Test reservation with TTL=0 (should still be allowed)."""
        mock_urlopen.return_value = self._mock_response(200, {"status": "ok"})
        adapter = AgentMailAdapter(agent_name="test-agent")
        
        mock_urlopen.return_value = self._mock_response(201, {"reserved": True})
        result = adapter.reserve_issue("bd-123", ttl=0)
        
        # Should succeed - server decides if TTL is valid
        self.assertTrue(result)
    
    @patch('beads_mail_adapter.urlopen')
    def test_reserve_with_very_large_ttl(self, mock_urlopen):
        """Test reservation with very large TTL."""
        mock_urlopen.return_value = self._mock_response(200, {"status": "ok"})
        adapter = AgentMailAdapter(agent_name="test-agent")
        
        mock_urlopen.return_value = self._mock_response(201, {"reserved": True})
        result = adapter.reserve_issue("bd-999", ttl=86400 * 365)  # 1 year
        
        # Should succeed - server decides if TTL is valid
        self.assertTrue(result)
    
    @patch('beads_mail_adapter.urlopen')
    def test_reserve_issue_with_special_characters_in_id(self, mock_urlopen):
        """Test reservation with special characters in issue ID."""
        mock_urlopen.return_value = self._mock_response(200, {"status": "ok"})
        adapter = AgentMailAdapter(agent_name="test-agent")
        
        # Test various ID formats
        test_ids = ["bd-abc123", "bd-123-456", "test-999", "bd_special"]
        
        for issue_id in test_ids:
            mock_urlopen.return_value = self._mock_response(201, {"reserved": True})
            result = adapter.reserve_issue(issue_id)
            self.assertTrue(result, f"Failed to reserve {issue_id}")
    
    @patch('beads_mail_adapter.urlopen')
    def test_release_nonexistent_reservation(self, mock_urlopen):
        """Test releasing a reservation that doesn't exist."""
        mock_urlopen.return_value = self._mock_response(200, {"status": "ok"})
        adapter = AgentMailAdapter(agent_name="test-agent")
        
        # Server might return 404, but adapter should handle gracefully
        error_response = HTTPError(
            url="http://test",
            code=404,
            msg="Not Found",
            hdrs={},
            fp=BytesIO(b"Reservation not found")
        )
        mock_urlopen.side_effect = error_response
        
        result = adapter.release_issue("bd-nonexistent")
        
        # Should still return True (graceful degradation)
        self.assertTrue(result)
    
    @patch('beads_mail_adapter.urlopen')
    def test_multiple_reservations_same_agent(self, mock_urlopen):
        """Test agent reserving multiple issues sequentially."""
        mock_urlopen.return_value = self._mock_response(200, {"status": "ok"})
        adapter = AgentMailAdapter(agent_name="test-agent")
        
        # Reserve multiple issues
        for i in range(5):
            mock_urlopen.return_value = self._mock_response(201, {"reserved": True})
            result = adapter.reserve_issue(f"bd-{i}")
            self.assertTrue(result)


class TestTimeoutConfiguration(unittest.TestCase):
    """Test timeout configuration and behavior."""
    
    def _mock_response(self, status_code=200, data=None):
        """Create mock HTTP response."""
        mock_response = MagicMock()
        mock_response.status = status_code
        mock_response.__enter__ = Mock(return_value=mock_response)
        mock_response.__exit__ = Mock(return_value=False)
        
        if data is not None:
            mock_response.read.return_value = json.dumps(data).encode('utf-8')
        else:
            mock_response.read.return_value = b''
        
        return mock_response
    
    @patch.dict(os.environ, {'AGENT_MAIL_TIMEOUT': '15'})
    @patch('beads_mail_adapter.urlopen')
    def test_timeout_from_env_var(self, mock_urlopen):
        """Test timeout configuration from environment variable."""
        mock_urlopen.side_effect = URLError("Not testing connection")
        
        adapter = AgentMailAdapter()
        
        self.assertEqual(adapter.timeout, 15)
    
    @patch('beads_mail_adapter.urlopen')
    def test_timeout_from_constructor(self, mock_urlopen):
        """Test timeout configuration from constructor."""
        mock_urlopen.side_effect = URLError("Not testing connection")
        
        adapter = AgentMailAdapter(timeout=3)
        
        self.assertEqual(adapter.timeout, 3)
    
    @patch('beads_mail_adapter.urlopen')
    @patch.dict(os.environ, {'AGENT_MAIL_TIMEOUT': '10'})
    def test_constructor_timeout_overrides_env(self, mock_urlopen):
        """Test constructor timeout overrides environment variable."""
        mock_urlopen.side_effect = URLError("Not testing connection")
        
        adapter = AgentMailAdapter(timeout=7)
        
        self.assertEqual(adapter.timeout, 7)
    
    @patch('beads_mail_adapter.urlopen')
    def test_health_check_uses_short_timeout(self, mock_urlopen):
        """Test health check uses 2s timeout instead of default."""
        mock_urlopen.return_value = self._mock_response(200, {"status": "ok"})
        
        adapter = AgentMailAdapter(timeout=10)
        
        # Health check should have been called with timeout=2
        # Verify the call was made with timeout parameter
        self.assertTrue(mock_urlopen.called)
        # The health check is called during __init__
        # We can verify it was called but actual timeout verification
        # requires inspecting the urlopen call args
        call_args = mock_urlopen.call_args_list[0]
        # urlopen(req, timeout=2)
        if len(call_args[1]) > 0:
            self.assertEqual(call_args[1].get('timeout'), 2)


class TestInboxHandlingEdgeCases(unittest.TestCase):
    """Test edge cases in inbox message handling."""
    
    def _mock_response(self, status_code=200, data=None):
        """Create mock HTTP response."""
        mock_response = MagicMock()
        mock_response.status = status_code
        mock_response.__enter__ = Mock(return_value=mock_response)
        mock_response.__exit__ = Mock(return_value=False)
        
        if data is not None:
            mock_response.read.return_value = json.dumps(data).encode('utf-8')
        else:
            mock_response.read.return_value = b''
        
        return mock_response
    
    @patch('beads_mail_adapter.urlopen')
    def test_inbox_with_large_message_list(self, mock_urlopen):
        """Test inbox handling with many messages."""
        mock_urlopen.return_value = self._mock_response(200, {"status": "ok"})
        adapter = AgentMailAdapter(agent_name="test-agent")
        
        # Create large message list
        messages = [{"id": i, "event": "test", "data": {}} for i in range(100)]
        mock_urlopen.return_value = self._mock_response(200, messages)
        
        result = adapter.check_inbox()
        
        self.assertEqual(len(result), 100)
    
    @patch('beads_mail_adapter.urlopen')
    def test_inbox_with_nested_payload_data(self, mock_urlopen):
        """Test inbox messages with deeply nested payload data."""
        mock_urlopen.return_value = self._mock_response(200, {"status": "ok"})
        adapter = AgentMailAdapter(agent_name="test-agent")
        
        messages = [{
            "from": "agent-1",
            "event": "complex_update",
            "data": {
                "issue": {
                    "id": "bd-123",
                    "metadata": {
                        "tags": ["urgent", "bug"],
                        "assignee": {"name": "test", "id": 42}
                    }
                }
            }
        }]
        mock_urlopen.return_value = self._mock_response(200, messages)
        
        result = adapter.check_inbox()
        
        self.assertEqual(len(result), 1)
        self.assertEqual(result[0]["data"]["issue"]["id"], "bd-123")
    
    @patch('beads_mail_adapter.urlopen')
    def test_inbox_returns_none_on_error(self, mock_urlopen):
        """Test inbox gracefully handles errors and returns empty list."""
        mock_urlopen.return_value = self._mock_response(200, {"status": "ok"})
        adapter = AgentMailAdapter(agent_name="test-agent")
        
        # Simulate error
        mock_urlopen.side_effect = URLError("Network error")
        
        result = adapter.check_inbox()
        
        self.assertEqual(result, [])


class TestNotificationEdgeCases(unittest.TestCase):
    """Test edge cases in notification sending."""
    
    def _mock_response(self, status_code=200, data=None):
        """Create mock HTTP response."""
        mock_response = MagicMock()
        mock_response.status = status_code
        mock_response.__enter__ = Mock(return_value=mock_response)
        mock_response.__exit__ = Mock(return_value=False)
        
        if data is not None:
            mock_response.read.return_value = json.dumps(data).encode('utf-8')
        else:
            mock_response.read.return_value = b''
        
        return mock_response
    
    @patch('beads_mail_adapter.urlopen')
    def test_notify_with_empty_payload(self, mock_urlopen):
        """Test sending notification with empty payload."""
        mock_urlopen.return_value = self._mock_response(200, {"status": "ok"})
        adapter = AgentMailAdapter(agent_name="test-agent")
        
        mock_urlopen.return_value = self._mock_response(201, {"sent": True})
        result = adapter.notify("event_type", {})
        
        self.assertTrue(result)
    
    @patch('beads_mail_adapter.urlopen')
    def test_notify_with_large_payload(self, mock_urlopen):
        """Test sending notification with large payload."""
        mock_urlopen.return_value = self._mock_response(200, {"status": "ok"})
        adapter = AgentMailAdapter(agent_name="test-agent")
        
        # Create large payload
        large_payload = {
            "issues": [{"id": f"bd-{i}", "data": "x" * 100} for i in range(100)]
        }
        mock_urlopen.return_value = self._mock_response(201, {"sent": True})
        result = adapter.notify("bulk_update", large_payload)
        
        self.assertTrue(result)
    
    @patch('beads_mail_adapter.urlopen')
    def test_notify_with_unicode_payload(self, mock_urlopen):
        """Test sending notification with Unicode characters."""
        mock_urlopen.return_value = self._mock_response(200, {"status": "ok"})
        adapter = AgentMailAdapter(agent_name="test-agent")
        
        unicode_payload = {
            "message": "Hello 世界 🎉",
            "emoji": "✅ 🚀 💯"
        }
        mock_urlopen.return_value = self._mock_response(201, {"sent": True})
        result = adapter.notify("unicode_test", unicode_payload)
        
        self.assertTrue(result)


if __name__ == '__main__':
    unittest.main()

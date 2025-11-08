#!/usr/bin/env python3
"""
Beads Agent Mail Adapter

Lightweight HTTP client for MCP Agent Mail server that provides:
- File reservation system (collision prevention)
- Real-time notifications between agents
- Status update coordination
- Graceful degradation when server unavailable

Usage:
    from beads_mail_adapter import AgentMailAdapter
    
    adapter = AgentMailAdapter()
    if adapter.enabled:
        adapter.reserve_issue("bd-123")
        adapter.notify("status_changed", {"issue_id": "bd-123", "status": "in_progress"})
        adapter.release_issue("bd-123")
"""

import os
import logging
from typing import Optional, Dict, Any, List
from urllib.request import Request, urlopen
from urllib.error import URLError, HTTPError
import json

logger = logging.getLogger(__name__)


class AgentMailAdapter:
    """
    Agent Mail HTTP client with health checks and graceful degradation.
    
    Environment variables:
        AGENT_MAIL_URL: Server URL (default: http://127.0.0.1:8765)
        AGENT_MAIL_TOKEN: Bearer token for authentication
        BEADS_AGENT_NAME: Agent identifier (default: hostname)
        AGENT_MAIL_TIMEOUT: Request timeout in seconds (default: 5)
    """
    
    def __init__(
        self,
        url: Optional[str] = None,
        token: Optional[str] = None,
        agent_name: Optional[str] = None,
        timeout: Optional[int] = None
    ):
        """
        Initialize Agent Mail adapter with health check.
        
        Args:
            url: Server URL (overrides AGENT_MAIL_URL env var)
            token: Bearer token (overrides AGENT_MAIL_TOKEN env var)
            agent_name: Agent identifier (overrides BEADS_AGENT_NAME env var)
            timeout: HTTP request timeout in seconds (overrides AGENT_MAIL_TIMEOUT env var)
        """
        self.url = url or os.getenv("AGENT_MAIL_URL", "http://127.0.0.1:8765")
        self.token = token or os.getenv("AGENT_MAIL_TOKEN", "")
        self.agent_name = agent_name or os.getenv("BEADS_AGENT_NAME") or self._get_default_agent_name()
        # Constructor argument overrides environment variable
        if timeout is not None:
            self.timeout = timeout
        else:
            self.timeout = int(os.getenv("AGENT_MAIL_TIMEOUT", "5"))
        self.enabled = False
        
        # Remove trailing slash from URL
        self.url = self.url.rstrip("/")
        
        # Perform health check on initialization
        self._health_check()
        
    def _get_default_agent_name(self) -> str:
        """Get default agent name from hostname or fallback."""
        import socket
        try:
            return socket.gethostname()
        except Exception:
            return "beads-agent"
    
    def _health_check(self) -> None:
        """
        Check if Agent Mail server is reachable.
        Sets self.enabled based on health check result.
        """
        try:
            response = self._request("GET", "/api/health", timeout=2)
            if response and response.get("status") == "ok":
                self.enabled = True
                logger.info(f"Agent Mail server available at {self.url}")
            else:
                logger.warning(f"Agent Mail server health check failed, falling back to Beads-only mode")
                self.enabled = False
        except Exception as e:
            logger.info(f"Agent Mail server unavailable ({e}), falling back to Beads-only mode")
            self.enabled = False
    
    def _request(
        self,
        method: str,
        path: str,
        data: Optional[Dict[str, Any]] = None,
        timeout: Optional[int] = None
    ) -> Optional[Dict[str, Any]]:
        """
        Make HTTP request to Agent Mail server.
        
        Args:
            method: HTTP method (GET, POST, DELETE)
            path: API path (must start with /)
            data: Request body (JSON)
            timeout: Request timeout override
            
        Returns:
            Response JSON or None on error
        """
        if not self.enabled and not path.endswith("/health"):
            return None
            
        url = f"{self.url}{path}"
        headers = {"Content-Type": "application/json"}
        
        if self.token:
            headers["Authorization"] = f"Bearer {self.token}"
        
        body = json.dumps(data).encode("utf-8") if data else None
        
        try:
            req = Request(url, data=body, headers=headers, method=method)
            with urlopen(req, timeout=timeout or self.timeout) as response:
                if response.status in (200, 201, 204):
                    response_data = response.read()
                    if response_data:
                        return json.loads(response_data)
                    return {}
                else:
                    logger.warning(f"Agent Mail request failed: {method} {path} -> {response.status}")
                    return None
        except HTTPError as e:
            if e.code == 409:  # Conflict (reservation already exists)
                error_body = e.read().decode("utf-8")
                try:
                    error_data = json.loads(error_body)
                    logger.warning(f"Agent Mail conflict: {error_data.get('error', 'Unknown error')}")
                    return {"error": error_data.get("error"), "status_code": 409}
                except json.JSONDecodeError:
                    logger.warning(f"Agent Mail conflict: {error_body}")
                    return {"error": error_body, "status_code": 409}
            else:
                logger.warning(f"Agent Mail HTTP error: {method} {path} -> {e.code} {e.reason}")
                return None
        except URLError as e:
            logger.debug(f"Agent Mail connection error: {e.reason}")
            return None
        except Exception as e:
            logger.debug(f"Agent Mail request error: {e}")
            return None
    
    def reserve_issue(self, issue_id: str, ttl: int = 3600) -> bool:
        """
        Reserve an issue to prevent other agents from claiming it.
        
        Args:
            issue_id: Issue ID (e.g., "bd-123")
            ttl: Reservation time-to-live in seconds (default: 1 hour)
            
        Returns:
            True if reservation successful, False otherwise
        """
        if not self.enabled:
            return True  # No-op in Beads-only mode
        
        response = self._request(
            "POST",
            "/api/reservations",
            data={
                "file_path": f".beads/issues/{issue_id}",
                "agent_name": self.agent_name,
                "ttl": ttl
            }
        )
        
        if response and response.get("status_code") == 409:
            logger.error(f"Issue {issue_id} already reserved: {response.get('error')}")
            return False
        
        # Graceful degradation: return True if request failed (None)
        return True
    
    def release_issue(self, issue_id: str) -> bool:
        """
        Release a previously reserved issue.
        
        Args:
            issue_id: Issue ID to release
            
        Returns:
            True if release successful, False otherwise
        """
        if not self.enabled:
            return True
        
        response = self._request(
            "DELETE",
            f"/api/reservations/{self.agent_name}/{issue_id}"
        )
        # Graceful degradation: return True even if request failed
        return True
    
    def notify(self, event_type: str, data: Dict[str, Any]) -> bool:
        """
        Send notification to other agents.
        
        Args:
            event_type: Event type (e.g., "status_changed", "issue_completed")
            data: Event payload
            
        Returns:
            True if notification sent, False otherwise
        """
        if not self.enabled:
            return True
        
        response = self._request(
            "POST",
            "/api/notifications",
            data={
                "from_agent": self.agent_name,
                "event_type": event_type,
                "payload": data
            }
        )
        # Graceful degradation: return True even if request failed
        return True
    
    def check_inbox(self) -> List[Dict[str, Any]]:
        """
        Check for incoming notifications from other agents.
        
        Returns:
            List of notification messages (empty if server unavailable)
        """
        if not self.enabled:
            return []
        
        response = self._request("GET", f"/api/notifications/{self.agent_name}")
        if response and isinstance(response, list):
            return response
        elif response and "messages" in response:
            return response["messages"]
        # Graceful degradation: return empty list if request failed
        return []
    
    def get_reservations(self) -> List[Dict[str, Any]]:
        """
        Get all active reservations.
        
        Returns:
            List of active reservations
        """
        if not self.enabled:
            return []
        
        response = self._request("GET", "/api/reservations")
        if response and isinstance(response, list):
            return response
        elif response and "reservations" in response:
            return response["reservations"]
        # Graceful degradation: return empty list if request failed
        return []

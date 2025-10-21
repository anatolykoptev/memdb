"""
API Analyzer for Scheduler

This module provides the APIAnalyzerForScheduler class that handles API requests
for search and add operations with reusable instance variables.
"""

import http.client
import json

from typing import Any
from urllib.parse import urlparse

import requests

from memos.log import get_logger


logger = get_logger(__name__)


class APIAnalyzerForScheduler:
    """
    API Analyzer class for scheduler operations.

    This class provides methods to interact with APIs for search and add operations,
    with reusable instance variables for better performance and configuration management.
    """

    def __init__(
        self,
        base_url: str = "http://127.0.0.1:8002",
        default_headers: dict[str, str] | None = None,
        timeout: int = 30,
    ):
        """
        Initialize the APIAnalyzerForScheduler.

        Args:
            base_url: Base URL for API requests
            default_headers: Default headers to use for all requests
            timeout: Request timeout in seconds
        """
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout

        # Default headers
        self.default_headers = default_headers or {"Content-Type": "application/json"}

        # Parse URL for http.client usage
        parsed_url = urlparse(self.base_url)
        self.host = parsed_url.hostname
        self.port = parsed_url.port or 8002
        self.is_https = parsed_url.scheme == "https"

        # Reusable connection for http.client
        self._connection = None

        logger.info(f"APIAnalyzerForScheduler initialized with base_url: {self.base_url}")

    def _get_connection(self) -> http.client.HTTPConnection | http.client.HTTPSConnection:
        """
        Get or create a reusable HTTP connection.

        Returns:
            HTTP connection object
        """
        if self._connection is None:
            if self.is_https:
                self._connection = http.client.HTTPSConnection(self.host, self.port)
            else:
                self._connection = http.client.HTTPConnection(self.host, self.port)
        return self._connection

    def _close_connection(self):
        """Close the HTTP connection if it exists."""
        if self._connection:
            self._connection.close()
            self._connection = None

    def search(
        self, user_id: str, mem_cube_id: str, query: str, top: int = 50, use_requests: bool = True
    ) -> dict[str, Any]:
        """
        Search for memories using the product/search API endpoint.

        Args:
            user_id: User identifier
            mem_cube_id: Memory cube identifier
            query: Search query string
            top: Number of top results to return
            use_requests: Whether to use requests library (True) or http.client (False)

        Returns:
            Dictionary containing the API response
        """
        payload = {"user_id": user_id, "mem_cube_id": mem_cube_id, "query": query, "top": top}

        try:
            if use_requests:
                return self._search_with_requests(payload)
            else:
                return self._search_with_http_client(payload)
        except Exception as e:
            logger.error(f"Error in search operation: {e}")
            return {"error": str(e), "success": False}

    def search_ws(
        self,
        user_id: str,
        mem_cube_id: str,
        query: str,
        top_k: int = 50,
        session_id: str | None = None,
        use_requests: bool = True,
    ) -> dict[str, Any]:
        """
        Search for memories using the product/search_ws API endpoint (with scheduler).

        Args:
            user_id: User identifier
            mem_cube_id: Memory cube identifier
            query: Search query string
            top_k: Number of top results to return
            session_id: Optional session identifier
            use_requests: Whether to use requests library (True) or http.client (False)

        Returns:
            Dictionary containing the API response
        """
        payload = {"user_id": user_id, "mem_cube_id": mem_cube_id, "query": query, "top_k": top_k}
        if session_id:
            payload["session_id"] = session_id

        try:
            if use_requests:
                return self._search_ws_with_requests(payload)
            else:
                return self._search_ws_with_http_client(payload)
        except Exception as e:
            logger.error(f"Error in search_ws operation: {e}")
            return {"error": str(e), "success": False}

    def _search_with_requests(self, payload: dict[str, Any]) -> dict[str, Any]:
        """
        Perform search using requests library.

        Args:
            payload: Request payload

        Returns:
            Dictionary containing the API response
        """
        url = f"{self.base_url}/product/search"

        response = requests.post(
            url, headers=self.default_headers, data=json.dumps(payload), timeout=self.timeout
        )

        logger.info(f"Search request to {url} completed with status: {response.status_code}")

        try:
            return {
                "success": True,
                "status_code": response.status_code,
                "data": response.json() if response.content else {},
                "text": response.text,
            }
        except json.JSONDecodeError:
            return {
                "success": True,
                "status_code": response.status_code,
                "data": {},
                "text": response.text,
            }

    def _search_ws_with_requests(self, payload: dict[str, Any]) -> dict[str, Any]:
        """
        Perform search_ws using requests library.

        Args:
            payload: Request payload

        Returns:
            Dictionary containing the API response
        """
        url = f"{self.base_url}/product/search_ws"

        response = requests.post(
            url, headers=self.default_headers, data=json.dumps(payload), timeout=self.timeout
        )

        logger.info(f"Search_ws request to {url} completed with status: {response.status_code}")

        try:
            return {
                "success": True,
                "status_code": response.status_code,
                "data": response.json() if response.content else {},
                "text": response.text,
            }
        except json.JSONDecodeError:
            return {
                "success": True,
                "status_code": response.status_code,
                "data": {},
                "text": response.text,
            }

    def _search_ws_with_http_client(self, payload: dict[str, Any]) -> dict[str, Any]:
        """
        Perform search_ws using http.client.

        Args:
            payload: Request payload

        Returns:
            Dictionary containing the API response
        """
        conn = self._get_connection()

        try:
            conn.request("POST", "/product/search_ws", json.dumps(payload), self.default_headers)

            response = conn.getresponse()
            data = response.read()
            response_text = data.decode("utf-8")

            logger.info(f"Search_ws request completed with status: {response.status}")

            try:
                response_data = json.loads(response_text) if response_text else {}
            except json.JSONDecodeError:
                response_data = {}

            return {
                "success": True,
                "status_code": response.status,
                "data": response_data,
                "text": response_text,
            }
        except Exception as e:
            logger.error(f"Error in search_ws with http.client: {e}")
            return {"error": str(e), "success": False}
        finally:
            conn.close()

    def _search_with_http_client(self, payload: dict[str, Any]) -> dict[str, Any]:
        """
        Perform search using http.client.

        Args:
            payload: Request payload

        Returns:
            Dictionary containing the API response
        """
        conn = self._get_connection()

        try:
            conn.request("POST", "/product/search", json.dumps(payload), self.default_headers)

            response = conn.getresponse()
            data = response.read()
            response_text = data.decode("utf-8")

            logger.info(f"Search request completed with status: {response.status}")

            try:
                response_data = json.loads(response_text) if response_text else {}
            except json.JSONDecodeError:
                response_data = {}

            return {
                "success": True,
                "status_code": response.status,
                "data": response_data,
                "text": response_text,
            }
        except Exception as e:
            logger.error(f"Error in http.client search: {e}")
            return {"error": str(e), "success": False}

    def add(
        self, messages: list, user_id: str, mem_cube_id: str, use_requests: bool = True
    ) -> dict[str, Any]:
        """
        Add memories using the product/add API endpoint.

        Args:
            messages: List of message objects with role and content
            user_id: User identifier
            mem_cube_id: Memory cube identifier
            use_requests: Whether to use requests library (True) or http.client (False)

        Returns:
            Dictionary containing the API response
        """
        payload = {"messages": messages, "user_id": user_id, "mem_cube_id": mem_cube_id}

        try:
            if use_requests:
                return self._add_with_requests(payload)
            else:
                return self._add_with_http_client(payload)
        except Exception as e:
            logger.error(f"Error in add operation: {e}")
            return {"error": str(e), "success": False}

    def _add_with_requests(self, payload: dict[str, Any]) -> dict[str, Any]:
        """
        Perform add using requests library.

        Args:
            payload: Request payload

        Returns:
            Dictionary containing the API response
        """
        url = f"{self.base_url}/product/add"

        response = requests.post(
            url, headers=self.default_headers, data=json.dumps(payload), timeout=self.timeout
        )

        logger.info(f"Add request to {url} completed with status: {response.status_code}")

        try:
            return {
                "success": True,
                "status_code": response.status_code,
                "data": response.json() if response.content else {},
                "text": response.text,
            }
        except json.JSONDecodeError:
            return {
                "success": True,
                "status_code": response.status_code,
                "data": {},
                "text": response.text,
            }

    def _add_with_http_client(self, payload: dict[str, Any]) -> dict[str, Any]:
        """
        Perform add using http.client.

        Args:
            payload: Request payload

        Returns:
            Dictionary containing the API response
        """
        conn = self._get_connection()

        try:
            conn.request("POST", "/product/add", json.dumps(payload), self.default_headers)

            response = conn.getresponse()
            data = response.read()
            response_text = data.decode("utf-8")

            logger.info(f"Add request completed with status: {response.status}")

            try:
                response_data = json.loads(response_text) if response_text else {}
            except json.JSONDecodeError:
                response_data = {}

            return {
                "success": True,
                "status_code": response.status,
                "data": response_data,
                "text": response_text,
            }
        except Exception as e:
            logger.error(f"Error in http.client add: {e}")
            return {"error": str(e), "success": False}

    def update_base_url(self, new_base_url: str):
        """
        Update the base URL and reinitialize connection parameters.

        Args:
            new_base_url: New base URL for API requests
        """
        self._close_connection()
        self.base_url = new_base_url.rstrip("/")

        # Re-parse URL
        parsed_url = urlparse(self.base_url)
        self.host = parsed_url.hostname
        self.port = parsed_url.port or (443 if parsed_url.scheme == "https" else 80)
        self.is_https = parsed_url.scheme == "https"

        logger.info(f"Base URL updated to: {self.base_url}")

    def update_headers(self, headers: dict[str, str]):
        """
        Update default headers.

        Args:
            headers: New headers to merge with existing ones
        """
        self.default_headers.update(headers)
        logger.info("Headers updated")

    def __del__(self):
        """Cleanup method to close connection when object is destroyed."""
        self._close_connection()


# Example usage
if __name__ == "__main__":
    # Initialize the analyzer
    analyzer = APIAnalyzerForScheduler()

    # Example add operation
    messages = [
        {"role": "user", "content": "Where should I go for New Year's Eve in Shanghai?"},
        {
            "role": "assistant",
            "content": "You could head to the Bund for the countdown, attend a rooftop party, or enjoy the fireworks at Disneyland Shanghai.",
        },
    ]

    add_result = analyzer.add(
        messages=messages, user_id="test_user_id", mem_cube_id="test_mem_cube_id"
    )
    print("Add result:", add_result)

    # Example search operation
    search_result = analyzer.search(
        user_id="test_user_id",
        mem_cube_id="test_mem_cube_id",
        query="What are some good places to celebrate New Year's Eve in Shanghai?",
        top=50,
    )
    print("Search result:", search_result)

    # Example search_ws operation
    search_ws_result = analyzer.search_ws(
        user_id="test_user_id",
        mem_cube_id="test_mem_cube_id",
        query="What are some good places to celebrate New Year's Eve in Shanghai?",
        top_k=10,
        session_id="test_session_id",
    )
    print("Search_ws result:", search_ws_result)

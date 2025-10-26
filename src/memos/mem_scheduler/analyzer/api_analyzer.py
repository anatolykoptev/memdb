"""
API Analyzer for Scheduler

This module provides the APIAnalyzerForScheduler class that handles API requests
for search and add operations with reusable instance variables.
"""

import http.client
import json

from time import sleep
from typing import Any
from urllib.parse import urlparse

import requests

from memos.log import get_logger
from memos.mem_scheduler.schemas.general_schemas import SearchMode


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

        # Attributes
        self.user_id = "test_user_id"
        self.mem_cube_id = "test_mem_cube_id"

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

    def analyze_service(self):
        # Example add operation
        messages = [
            {"role": "user", "content": "Where should I go for New Year's Eve in Shanghai?"},
            {
                "role": "assistant",
                "content": "You could head to the Bund for the countdown, attend a rooftop party, or enjoy the fireworks at Disneyland Shanghai.",
            },
        ]

        add_result = self.add(
            messages=messages, user_id="test_user_id", mem_cube_id="test_mem_cube_id"
        )
        print("Add result:", add_result)

        # Example search operation
        search_result = self.search(
            user_id="test_user_id",
            mem_cube_id="test_mem_cube_id",
            query="What are some good places to celebrate New Year's Eve in Shanghai?",
            top=50,
        )
        print("Search result:", search_result)

    def analyze_features(self):
        try:
            # Test basic search functionality
            search_result = self.search(
                user_id="test_user_id",
                mem_cube_id="test_mem_cube_id",
                query="What are some good places to celebrate New Year's Eve in Shanghai?",
                top=50,
            )
            print("Search result:", search_result)
        except Exception as e:
            logger.error(f"Feature analysis failed: {e}")


class DirectSearchMemoriesAnalyzer:
    """
    Direct analyzer for testing search_memories function
    Used for debugging and analyzing search_memories function behavior without starting a full API server
    """

    def __init__(self):
        """Initialize the analyzer"""
        # Import necessary modules
        try:
            from memos.api.product_models import APIADDRequest, APISearchRequest
            from memos.api.routers.server_router import add_memories, search_memories
            from memos.types import MessageDict, UserContext

            self.APISearchRequest = APISearchRequest
            self.APIADDRequest = APIADDRequest
            self.search_memories = search_memories
            self.add_memories = add_memories
            self.UserContext = UserContext
            self.MessageDict = MessageDict

            logger.info("DirectSearchMemoriesAnalyzer initialized successfully")
        except ImportError as e:
            logger.error(f"Failed to import modules: {e}")
            raise

    def create_test_search_request(
        self,
        query="test query",
        user_id="test_user",
        mem_cube_id="test_cube",
        mode="fast",
        top_k=10,
        chat_history=None,
        session_id=None,
    ):
        """
        Create a test APISearchRequest object with the given parameters.

        Args:
            query: Search query string
            user_id: User ID for the request
            mem_cube_id: Memory cube ID for the request
            mode: Search mode ("fast" or "fine")
            top_k: Number of results to return
            chat_history: Chat history for context (optional)
            session_id: Session ID for the request (optional)

        Returns:
            APISearchRequest: A configured request object
        """
        return self.APISearchRequest(
            query=query,
            user_id=user_id,
            mem_cube_id=mem_cube_id,
            mode=mode,
            top_k=top_k,
            chat_history=chat_history,
            session_id=session_id,
        )

    def create_test_add_request(
        self,
        user_id="test_user",
        mem_cube_id="test_cube",
        messages=None,
        memory_content=None,
        session_id=None,
    ):
        """
        Create a test APIADDRequest object with the given parameters.

        Args:
            user_id: User ID for the request
            mem_cube_id: Memory cube ID for the request
            messages: List of messages to add (optional)
            memory_content: Direct memory content to add (optional)
            session_id: Session ID for the request (optional)

        Returns:
            APIADDRequest: A configured request object
        """
        if messages is None and memory_content is None:
            # Default test messages
            messages = [
                {"role": "user", "content": "What's the weather like today?"},
                {
                    "role": "assistant",
                    "content": "I don't have access to real-time weather data, but you can check a weather app or website for current conditions.",
                },
            ]

        # Ensure we have a valid session_id
        if session_id is None:
            session_id = "test_session_" + str(hash(user_id + mem_cube_id))[:8]

        return self.APIADDRequest(
            user_id=user_id,
            mem_cube_id=mem_cube_id,
            messages=messages,
            memory_content=memory_content,
            session_id=session_id,
            doc_path=None,
            source="api_analyzer_test",
            chat_history=None,
            operation=None,
        )

    def test_add_memories_basic(self, user_id="test_user_add", mem_cube_id="test_cube_add"):
        """Basic add_memories test"""
        print("=" * 60)
        print("Starting basic add_memories test")
        print("=" * 60)

        try:
            # Create test request with default messages
            add_req = self.create_test_add_request(user_id=user_id, mem_cube_id=mem_cube_id)

            print("Test request created:")
            print(f"  User ID: {add_req.user_id}")
            print(f"  Mem Cube ID: {add_req.mem_cube_id}")
            print(f"  Messages: {add_req.messages}")
            print(f"  Session ID: {add_req.session_id}")

            # Call add_memories function
            print("\nCalling add_memories function...")
            result = self.add_memories(add_req)

            print(f"Add result: {result}")
            print("Basic add_memories test completed successfully")
            return result

        except Exception as e:
            print(f"Basic add_memories test failed: {e}")
            import traceback

            traceback.print_exc()
            return None

    def test_search_memories_basic(self, query: str, mode: str, topk: int):
        """Basic search_memories test"""
        print("=" * 60)
        print("Starting basic search_memories test")
        print("=" * 60)

        try:
            # Create test request
            search_req = self.create_test_search_request(
                query=query,
                user_id="test_user_id",
                mem_cube_id="test_mem_cube_id",
                mode=mode,
                top_k=topk,
            )

            print("Test request parameters:")
            print(f"  - query: {search_req.query}")
            print(f"  - user_id: {search_req.user_id}")
            print(f"  - mem_cube_id: {search_req.mem_cube_id}")
            print(f"  - mode: {search_req.mode}")
            print(f"  - top_k: {search_req.top_k}")
            print(f"  - internet_search: {search_req.internet_search}")
            print(f"  - moscube: {search_req.moscube}")
            print()

            # Call search_memories function
            print("Calling search_memories function...")
            result = self.search_memories(search_req)

            print("✅ Function call successful!")
            print(f"Return result type: {type(result)}")
            print(f"Return result: {result}")

            # Analyze return result
            if hasattr(result, "message"):
                print(f"Message: {result.message}")
            if hasattr(result, "data"):
                print(f"Data type: {type(result.data)}")
                if result.data and isinstance(result.data, dict):
                    for key, value in result.data.items():
                        print(f"  {key}: {len(value) if isinstance(value, list) else value}")

            return result

        except Exception as e:
            print(f"❌ Test failed: {e}")
            import traceback

            print("Detailed error information:")
            traceback.print_exc()
            return None

    def test_mix_search_memories_continuous_questions(
        self, user_id="test_user_mix", mem_cube_id="test_cube_mix"
    ):
        """
        Test mix_search_memories function with continuous questions to verify its effectiveness.
        This test simulates a conversation scenario where multiple related questions are asked
        to evaluate how well the mix search handles context and memory retrieval.
        """
        print(
            f"Testing mix_search_memories with continuous questions for user: {user_id}, cube: {mem_cube_id}"
        )

        try:
            # Import mix_search_memories function
            from memos.api.routers.server_router import mix_search_memories

            # First, add some test memories to work with
            print("\n--- Step 1: Adding test memories for continuous question testing ---")

            # Add memories about travel and food preferences
            test_conversations = [
                [
                    {"role": "user", "content": "I love Italian food, especially pasta and pizza"},
                    {
                        "role": "assistant",
                        "content": "That's great! Italian cuisine has so many delicious options. Do you have a favorite type of pasta?",
                    },
                ],
                [
                    {"role": "user", "content": "I'm planning a trip to Rome next month"},
                    {
                        "role": "assistant",
                        "content": "Rome is amazing! You'll love the history, architecture, and of course the authentic Italian food there.",
                    },
                ],
                [
                    {
                        "role": "user",
                        "content": "What are the best restaurants in Rome for authentic pasta?",
                    },
                    {
                        "role": "assistant",
                        "content": "Some excellent choices include Checchino dal 1887 for traditional Roman dishes, and Da Enzo for authentic carbonara and cacio e pepe.",
                    },
                ],
                [
                    {
                        "role": "user",
                        "content": "I also enjoy Japanese cuisine, particularly sushi and ramen",
                    },
                    {
                        "role": "assistant",
                        "content": "Japanese food is wonderful! The attention to detail and fresh ingredients make it special.",
                    },
                ],
                [
                    {"role": "user", "content": "Are there any good Japanese restaurants in Rome?"},
                    {
                        "role": "assistant",
                        "content": "Yes! Try Metamorfosi for high-end Japanese-Italian fusion, or Sakana for more traditional Japanese dishes.",
                    },
                ],
            ]

            # Add all test conversations
            for i, messages in enumerate(test_conversations):
                add_request = self.create_test_add_request(
                    user_id=user_id,
                    mem_cube_id=mem_cube_id,
                    messages=messages,
                    session_id=f"continuous_test_session_{i}",
                )

                self.add_memories(add_request)

            print("\n--- Step 2: Testing continuous questions with mix_search_memories ---")

            # Define a series of related questions to test continuous conversation
            continuous_questions = [
                {
                    "query": "What food do I like?",
                    "description": "Basic preference question",
                    "chat_history": [],
                },
                {
                    "query": "Where am I planning to travel?",
                    "description": "Travel destination question",
                    "chat_history": [
                        {"role": "user", "content": "What food do I like?"},
                        {
                            "role": "assistant",
                            "content": "Based on our conversation, you enjoy Italian food, especially pasta and pizza, and also Japanese cuisine like sushi and ramen.",
                        },
                    ],
                },
                {
                    "query": "Can you recommend restaurants that serve my favorite food in my travel destination?",
                    "description": "Complex contextual question combining food preferences and travel plans",
                    "chat_history": [
                        {"role": "user", "content": "What food do I like?"},
                        {
                            "role": "assistant",
                            "content": "You enjoy Italian food, especially pasta and pizza, and also Japanese cuisine like sushi and ramen.",
                        },
                        {"role": "user", "content": "Where am I planning to travel?"},
                        {
                            "role": "assistant",
                            "content": "You're planning a trip to Rome next month.",
                        },
                    ],
                },
                {
                    "query": "What specific pasta dishes should I try in Rome?",
                    "description": "Detailed follow-up question",
                    "chat_history": [
                        {
                            "role": "user",
                            "content": "Can you recommend restaurants that serve my favorite food in my travel destination?",
                        },
                        {
                            "role": "assistant",
                            "content": "For Italian food in Rome, try Checchino dal 1887 for traditional Roman dishes, and Da Enzo for authentic carbonara. For Japanese food, consider Metamorfosi for fusion or Sakana for traditional dishes.",
                        },
                    ],
                },
            ]

            # Test each question in the continuous conversation
            for i, question_data in enumerate(continuous_questions):
                print(f"\n--- Question {i + 1}: {question_data['description']} ---")
                print(f"Query: {question_data['query']}")

                # Create search request with chat history for context
                search_request = self.create_test_search_request(
                    query=question_data["query"],
                    user_id=user_id,
                    mem_cube_id=mem_cube_id,
                    mode=SearchMode.MIXTURE,  # Use mixture mode to test mix_search_memories
                    top_k=10,
                    chat_history=question_data["chat_history"],
                    session_id="continuous_test_main_session",
                )

                # Create user context
                user_context = self.UserContext(user_id=user_id, mem_cube_id=mem_cube_id)

                # Call mix_search_memories function
                mix_search_result = mix_search_memories(search_request, user_context)

                print(f"Mix search returned {len(mix_search_result)} results")

                # Analyze the results

                print("Top 3 results:")
                for j, result in enumerate(mix_search_result[:3]):
                    if isinstance(result, dict):
                        memory_content = result.get("memory", result.get("content", str(result)))
                        print(f"  {j + 1}. {memory_content[:100]}...")
                    else:
                        print(f"  {j + 1}. {str(result)[:100]}...")

                # Check if results are relevant to the question context
                relevant_count = 0

                for result in mix_search_result:
                    if isinstance(result, dict):
                        content = result.get("memory", result.get("content", "")).lower()
                    else:
                        content = str(result).lower()

                    # Check for relevance based on key terms
                    if any(
                        term in content
                        for term in [
                            "italian",
                            "pasta",
                            "pizza",
                            "rome",
                            "japanese",
                            "sushi",
                            "restaurant",
                        ]
                    ):
                        relevant_count += 1

                relevance_ratio = (
                    relevant_count / len(mix_search_result) if mix_search_result else 0
                )
                print(
                    f"Relevance: {relevant_count}/{len(mix_search_result)} results ({relevance_ratio:.2%})"
                )
                sleep(5)

            print("\n--- Step 3: Testing memory accumulation effect ---")

            # Test how mix_search_memories handles accumulated context
            accumulated_query = "Based on everything we've discussed, what's the perfect Rome itinerary for someone with my food preferences?"

            # Build comprehensive chat history
            comprehensive_history = []
            for question_data in continuous_questions:
                comprehensive_history.append({"role": "user", "content": question_data["query"]})
                comprehensive_history.append(
                    {"role": "assistant", "content": f"Response to: {question_data['query']}"}
                )

            final_search_request = self.create_test_search_request(
                query=accumulated_query,
                user_id=user_id,
                mem_cube_id=mem_cube_id,
                mode="mixture",
                top_k=15,
                chat_history=comprehensive_history,
                session_id="continuous_test_final_session",
            )

            user_context = self.UserContext(user_id=user_id, mem_cube_id=mem_cube_id)

            try:
                final_result = mix_search_memories(final_search_request, user_context)
                print(f"Final comprehensive search returned {len(final_result)} results")

                if final_result:
                    print("Final search top results:")
                    for i, result in enumerate(final_result[:5]):
                        if isinstance(result, dict):
                            content = result.get("memory", result.get("content", str(result)))
                        else:
                            content = str(result)
                        print(f"  {i + 1}. {content[:150]}...")

            except Exception as e:
                print(f"Error in final comprehensive search: {e}")
                import traceback

                traceback.print_exc()

            print("\n=== Continuous questions test completed ===")

        except Exception as e:
            print(f"Error in continuous questions test: {e}")
            import traceback

            traceback.print_exc()

    def run_all_tests(self, mode: SearchMode):
        """Run all available tests"""
        print("🚀 Starting comprehensive test suite")
        print("=" * 80)

        # Test add_memories functions (more likely to have dependency issues)
        print("\n\n📝 Testing ADD_MEMORIES functions:")
        try:
            print("\n" + "-" * 40)
            self.test_add_memories_basic()
            print("✅ Basic add memories test completed")
        except Exception as e:
            print(f"❌ Basic add memories test failed: {e}")

        # Test search_memories functions first (less likely to fail)
        print("\n🔍 Testing SEARCH_MEMORIES functions:")
        try:
            self.test_search_memories_basic(
                query="What are some good places to celebrate New Year's Eve in Shanghai?",
                mode=mode,
                topk=3,
            )
            print("✅ Search memories test completed successfully")
        except Exception as e:
            print(f"❌ Search memories test failed: {e}")

        # Test mix_search_memories with continuous questions
        print("\n🔄 Testing MIX_SEARCH_MEMORIES with continuous questions:")
        try:
            self.test_mix_search_memories_continuous_questions()
            print("✅ Mix search memories continuous questions test completed")
        except Exception as e:
            print(f"❌ Mix search memories test failed: {e}")

        print("\n" + "=" * 80)
        print("✅ All tests completed!")


# Example usage
if __name__ == "__main__":
    import argparse

    parser = argparse.ArgumentParser(description="API Analyzer for Memory Scheduler")
    parser.add_argument(
        "--mode",
        choices=["direct", "api"],
        default="direct",
        help="Test mode: 'direct' for direct function testing, 'api' for API testing (default: direct)",
    )

    args = parser.parse_args()

    if args.mode == "direct":
        # Direct test mode for search_memories and add_memories functions
        print("Using direct test mode")
        try:
            direct_analyzer = DirectSearchMemoriesAnalyzer()
            direct_analyzer.run_all_tests(mode=SearchMode.MIXTURE)
        except Exception as e:
            print(f"Direct test mode failed: {e}")
            import traceback

            traceback.print_exc()
    else:
        # Original API test mode
        print("Using API test mode")
        analyzer = APIAnalyzerForScheduler()

        # Test add operation
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

        # Test search operation
        search_result = analyzer.search(
            user_id="test_user_id",
            mem_cube_id="test_mem_cube_id",
            query="What are some good places to celebrate New Year's Eve in Shanghai?",
            top=50,
        )
        print("Search result:", search_result)

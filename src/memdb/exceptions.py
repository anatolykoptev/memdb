"""Custom exceptions for the MemDB library.

This module defines all custom exceptions used throughout the MemDB project.
All exceptions inherit from a base MemDBError class to provide a consistent
error handling interface.
"""


class MemDBError(Exception): ...


class ConfigurationError(MemDBError): ...


class MemoryError(MemDBError): ...


class MemCubeError(MemDBError): ...


class VectorDBError(MemDBError): ...


class LLMError(MemDBError): ...


class EmbedderError(MemDBError): ...


class ParserError(MemDBError): ...

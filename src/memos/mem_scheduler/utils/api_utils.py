from typing import Any


def format_textual_memory_item(memory_data: Any) -> dict[str, Any]:
    """Format a single memory item for API response."""
    memory = memory_data.model_dump()
    memory_id = memory["id"]
    ref_id = f"[{memory_id.split('-')[0]}]"

    memory["ref_id"] = ref_id
    memory["metadata"]["embedding"] = []
    memory["metadata"]["sources"] = []
    memory["metadata"]["ref_id"] = ref_id
    memory["metadata"]["id"] = memory_id
    memory["metadata"]["memory"] = memory["memory"]

    return memory

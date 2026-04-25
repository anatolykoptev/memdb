# DEPRECATED 2026-04-26 — Python container shutdown via M9 S8 (chore/phase5-python-shutdown).
# memdb-api service commented out in compose/memdb.yml. Delete after 2-week soak.
# Base image
FROM python:3.11-slim

# Install dependencies
RUN apt-get update && apt-get install -y \
    gcc \
    g++ \
    build-essential \
    libffi-dev \
    python3-dev \
    curl \
    && rm -rf /var/lib/apt/lists/*

# Set working directory
WORKDIR /app

# Set Hugging Face mirror
ENV HF_ENDPOINT=https://hf-mirror.com

# Install Python packages
COPY docker/requirements.txt .
RUN pip install --upgrade pip && pip install --no-cache-dir -r requirements.txt

# Copy application code
COPY docker/ ./docker/
COPY src/ ./src/

# Set Python import path
ENV PYTHONPATH=/app/src

# Expose port
EXPOSE 8000

# Start the docker
CMD ["uvicorn", "memdb.api.server_api:app", "--host", "0.0.0.0", "--port", "8000", "--reload"]

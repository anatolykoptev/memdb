#!/bin/bash
# Downloads multilingual-e5-large INT8 ONNX model and tokenizer
set -euo pipefail

MODEL_DIR="${1:-./models/multilingual-e5-large}"
mkdir -p "$MODEL_DIR"

echo "Downloading tokenizer.json..."
wget -q --show-progress -O "$MODEL_DIR/tokenizer.json" \
  "https://huggingface.co/intfloat/multilingual-e5-large/resolve/main/tokenizer.json"

echo "Downloading model_quantized.onnx (INT8)..."
wget -q --show-progress -O "$MODEL_DIR/model_quantized.onnx" \
  "https://huggingface.co/Xenova/multilingual-e5-large/resolve/main/onnx/model_quantized.onnx"

echo ""
echo "Downloaded to $MODEL_DIR:"
ls -lh "$MODEL_DIR"
echo ""
echo "Total size: $(du -sh "$MODEL_DIR" | cut -f1)"

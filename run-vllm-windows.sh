#!/bin/bash

# Check if .venv exists
if [ ! -d ".venv" ]; then
  echo "ERROR: .venv directory not found."
  echo "Install instructions: https://github.com/SystemPanic/vllm-windows"
  exit 1
fi

# Activate venv if not already active
if [ -z "$VIRTUAL_ENV" ]; then
  source .venv/Scripts/activate
fi

# Run vllm serve
vllm serve \
  --model=sakamakismile/Qwen3.6-27B-Text-NVFP4-MTP \
  --served-model-name=Qwen3.6-27B-Text-NVFP4-MTP \
  --max-model-len=200000 \
  --gpu-memory-utilization=0.90 \
  --kv-cache-dtype=fp8 \
  --host=0.0.0.0 \
  --port=8000 \
  --async-scheduling \
  --enable-auto-tool-choice \
  --reasoning-parser=qwen3 \
  --tool-call-parser=qwen3_coder \
  --enable-prefix-caching \
  --enable-chunked-prefill \
  --max-num-seqs=4 \
  --max-num-batched-tokens=8192 \
  --default-chat-template-kwargs='{"preserve_thinking": true, "enable_thinking": true}' \
  --speculative-config='{"method":"mtp","num_speculative_tokens":3}' \
  --quantization=modelopt \
  --language-model-only

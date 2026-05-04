# Synthetic Data Generation Scripts

These scripts generate synthetic chat histories for evaluating and fine-tuning the query router's classification capabilities.

## Purpose

The goal is to create a dataset of multi-turn conversations where each user message has a known ground-truth label (`DIRECT` or `REASONING`). This allows automated evaluation of the router's performance and systematic prompt optimisation by replaying histories against different routing prompts.

## Scripts

### `generate_thread.py`

Generates a single synthetic chat thread of exactly 6 turns.

**Process:**
1. Randomly picks a target classification (`DIRECT` or `REASONING`) for each turn.
2. Calls the generator LLM to produce a question matching that classification. The first message uses a randomly selected domain hint (coding, DevOps, algorithms, maths, science, etc.) to encourage diversity; follow-up messages are grounded in the existing conversation history.
3. Calls the generator LLM again to produce a real assistant response to that question, which becomes the context for the next turn.
4. Repeats for all 6 turns, then saves the thread to a JSON file.

All LLM calls use `enable_thinking: false` for speed.

**Usage:**
```bash
python3 generate_thread.py \
  --generator-url <LLM_ENDPOINT> \
  --output-dir <OUTPUT_DIR>
```

**Arguments:**

| Argument | Default | Description |
|---|---|---|
| `--generator-url` | `http://localhost:8000/v1/chat/completions` | vLLM endpoint used to generate questions and responses |
| `--router-url` | `http://localhost:8001/v1/chat/completions` | Router endpoint (reserved for future evaluation use) |
| `--output-dir` | `data/synthetic` | Directory to save output JSON files |

---

### `run_collection.py`

Runs `generate_thread.py` N times, optionally in parallel.

**Usage:**
```bash
python3 run_collection.py <N> \
  --generator-url <LLM_ENDPOINT> \
  --output-dir <OUTPUT_DIR> \
  --workers <WORKERS>
```

**Arguments:**

| Argument | Default | Description |
|---|---|---|
| `n` | *(required)* | Number of threads to generate |
| `--generator-url` | *(required)* | vLLM endpoint used to generate questions and responses |
| `--output-dir` | `data/synthetic` | Directory to save output JSON files |
| `--workers` | `1` | Number of threads to run in parallel |

Workers are capped at `n`. Output from parallel workers is prefixed with `[i/n]` so progress from concurrent runs stays readable. A final summary line reports how many threads succeeded and failed; the script exits non-zero if any failed.

**Example — generate 50 threads with 4 parallel workers:**
```bash
python3 run_collection.py 50 \
  --generator-url http://localhost:8000/v1/chat/completions \
  --output-dir data/synthetic \
  --workers 4
```

---

## Data Format

Each generated JSON file follows this structure:

```json
{
  "thread_id": "<uuid>",
  "history": [
    { "role": "user", "content": "..." },
    { "role": "assistant", "content": "..." }
  ],
  "labels": ["DIRECT", "REASONING", "REASONING", "DIRECT", "REASONING", "DIRECT"]
}
```

- `history` contains 12 messages: 6 user turns alternating with 6 real assistant responses.
- `labels` contains 6 entries, one per user turn, in order.
- Because all 6 labels are stored, evaluation can truncate a thread to any prefix length (1–6 turns) without re-generating data.

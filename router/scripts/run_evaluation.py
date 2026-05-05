import json
import os
import time
import uuid
import argparse
import requests
import concurrent.futures
from datetime import datetime
from typing import List, Tuple, Optional

from evaluator import (
    EvaluationResult,
    EvaluationRun,
    compare_labels,
    analyze_error,
    calculate_weighted_score,
    generate_optimizer_report
)

# Configuration
GENERATOR_URL = "http://localhost:8000/v1/chat/completions"
SYNTHETIC_DATA_DIR = "data/synthetic"
OUTPUT_DIR = "data/eval_results"
MODEL_NAME = "google/gemma-4-26B-A4B-it"
PROMPT_VERSION = "v1-baseline"

# The prompt used by the actual router in classify.go
ROUTER_SYSTEM_PROMPT = (
    "You are a query router. Classify the user's latest message as DIRECT or REASONING based on the cognitive depth required to provide an accurate response.\n\n"
    "DIRECT: Use for shallow-processing tasks. This includes simple factual retrieval, greetings, or trivial transformations where the answer is immediate and requires no internal logical steps, synthesis of information, or complex reasoning.\n\n"
    "REASONING: Use for deep-processing tasks. This includes queries that require:\n"
    "- Multi-step logical deduction or sequential processing.\n"
    "- Synthesis or abstraction (e.g., summarizing, identifying themes, or distilling information).\n"
    "- Analytical reasoning (e.g., comparisons, critiques, or explaining 'why').\n"
    "- Complex constraint satisfaction (e.g., following intricate formatting or stylistic rules).\n"
    "- Processing high-density or structurally complex input.\n\n"
    "Classify the message based on the required depth of processing."
)

def evaluate_thread(filename: str) -> List[EvaluationResult]:
    """
    Evaluates a single thread and returns a list of results for its turns.
    """
    filepath = os.path.join(SYNTHETIC_DATA_DIR, filename)
    with open(filepath, 'r') as f:
        thread_data = json.load(f)

    history = thread_data['history']
    ground_truth_labels = thread_data['labels']
    thread_results = []

    print(f"Evaluating thread: {filename}")

    for i in range(len(ground_truth_labels)):
        user_idx = i * 2
        user_query = history[user_idx]['content']
        expected_label = ground_truth_labels[i]

        # Prepare messages for the router: [System Prompt, ...last 3 messages]
        context_window = history[max(0, user_idx-2) : user_idx+1]
        router_messages = [{"role": "system", "content": ROUTER_SYSTEM_PROMPT}] + context_window

        payload = {
            "model": MODEL_NAME,
            "messages": router_messages,
            "max_tokens": 16,
            "stream": False,
            "structured_outputs": {"choice": ["DIRECT", "REASONING"]},
            "chat_template_kwargs": {"enable_thinking": False}
        }

        start_time = time.time()
        try:
            resp = requests.post(GENERATOR_URL, json=payload)
            resp.raise_for_status()
            router_output = resp.json()["choices"][0]["message"]["content"].strip().upper()
            latency = (time.time() - start_time) * 1000
            tokens = resp.json().get("usage", {}).get("completion_tokens", 0)
        except Exception as e:
            print(f"  [error] routing error in {filename} turn {i}: {e}")
            continue

        match = compare_labels(router_output, expected_label)
        result = EvaluationResult(
            query=user_query,
            ground_truth_label=expected_label,
            router_label=router_output,
            latency_ms=latency,
            token_usage=tokens
        )

        if not match:
            error_info = analyze_error(GENERATOR_URL, user_query, expected_label, router_output)
            result.error_category = error_info.get("category")
            result.error_reason = error_info.get("reason")

        thread_results.append(result)

    return thread_results

def run_evaluation(num_files: int = 0, workers: int = 1) -> Tuple[Optional[str], Optional[str]]:
    """
    Runs evaluation and returns a tuple of (results_file_path, report_file_path).
    """
    if not os.path.exists(OUTPUT_DIR):
        os.makedirs(OUTPUT_DIR)

    files = [f for f in os.listdir(SYNTHETIC_DATA_DIR) if f.endswith('.json')]
    if not files:
        print(f"No synthetic data found in {SYNTHETIC_DATA_DIR}")
        return None, None

    if num_files > 0:
        files = files[:num_files]

    print(f"Found {len(files)} threads. Starting evaluation with {workers} workers...")

    all_run_results = []

    with concurrent.futures.ThreadPoolExecutor(max_workers=workers) as executor:
        future_to_file = {executor.submit(evaluate_thread, f): f for f in files}
        for future in concurrent.futures.as_completed(future_to_file):
            filename = future_to_file[future]
            try:
                thread_results = future.result()
                all_run_results.extend(thread_results)
            except Exception as e:
                print(f"Thread {filename} generated an exception: {e}")

    evaluation_run = EvaluationRun(
        timestamp=datetime.now().isoformat(),
        prompt_version=PROMPT_VERSION,
        model_name=MODEL_NAME,
        results=all_run_results
    )

    run_id = str(uuid.uuid4())[:8]
    results_file = os.path.join(OUTPUT_DIR, f"results_{run_id}.json")
    with open(results_file, 'w') as f:
        json.dump(evaluation_run.to_dict(), f, indent=2)

    report = generate_optimizer_report(all_run_results)
    report_file = os.path.join(OUTPUT_DIR, f"report_{run_id}.md")
    with open(report_file, 'w') as f:
        f.write(report)

    print("\n" + "="*30)
    print("Evaluation Complete")
    print(f"Results: {results_file}")
    print(f"Report:  {report_file}")
    print("="*30)

    return results_file, report_file

if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Run router evaluation on synthetic data.")
    parser.add_argument("--n", type=int, default=0, help="Number of files to evaluate (0 for all)")
    parser.add_argument("--workers", type=int, default=1, help="Number of parallel workers")
    args = parser.parse_args()

    res_path, rep_path = run_evaluation(args.n, args.workers)
    if res_path:
        print(res_path)
        print(rep_path)



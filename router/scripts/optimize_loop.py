import argparse
import os
import subprocess
import time
from datetime import datetime
import json
import re

from evaluator import EvaluationRun
from run_evaluation import run_evaluation
from optimizer import optimize_prompt

# Configuration
GENERATOR_URL = "http://localhost:8000/v1/chat/completions"
SYNTHETIC_DATA_DIR = "data/synthetic"
OUTPUT_DIR = "data/eval_results"
PROMPTS_DIR = "data/prompts"
MODEL_NAME = "google/gemma-4-26B-A4B-it"

# Initial baseline prompt
INITIAL_PROMPT = (
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

def optimize_loop(target_score: float, max_iterations: int, workers: int):
    current_prompt = INITIAL_PROMPT
    iteration = 0

    # Save initial prompt
    if not os.path.exists(PROMPTS_DIR):
        os.makedirs(PROMPTS_DIR)

    current_prompt_path = os.path.join(PROMPTS_DIR, "v0_baseline.txt")
    with open(current_prompt_path, "w") as f:
        f.write(current_prompt)

    print(f"Starting optimization loop. Target Score: {target_score}")

    while iteration < max_iterations:
        iteration += 1
        print(f"\n--- Iteration {iteration} ---")

        # 1. Run Evaluation
        print("Step 1: Running evaluation...")
        results_file, report_file = run_evaluation(num_files=0, workers=workers, system_prompt=current_prompt)

        if not results_file or not report_file:
            print("Error: Evaluation failed to produce files.")
            break

        # 2. Parse Results
        with open(results_file, 'r') as f:
            run_data = json.load(f)

        # Extract score from report using regex
        with open(report_file, 'r') as f:
            report_content = f.read()

        score_match = re.search(r"\*\*Weighted Score:\*\* ([\-\d.]+)", report_content)
        if not score_match:
            print("Error: Could not find weighted score in report.")
            break
        current_score = float(score_match.group(1))

        print(f"Current Weighted Score: {current_score}")

        # 3. Check if target met
        if current_score >= target_score:
            print(f"Target score reached! ({current_score} >= {target_score})")
            break

        # 4. Run Optimization
        print("Step 2: Optimizing prompt...")
        new_prompt = optimize_prompt(GENERATOR_URL, report_file, current_prompt)

        if not new_prompt:
            print("Optimizer failed to return a new prompt.")
            break

        # Save new prompt
        current_prompt = new_prompt
        iteration_prompt_path = os.path.join(PROMPTS_DIR, f"v{iteration}_optimized.txt")
        with open(iteration_prompt_path, "w") as f:
            f.write(current_prompt)

        print(f"Iteration {iteration} complete. New prompt saved to {iteration_prompt_path}")

    if iteration >= max_iterations:
        print(f"\nReached maximum iterations ({max_iterations}). Stopping.")

    print(f"\nOptimization Finished. Final iteration: {iteration}")

if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Automated Prompt Optimization Loop.")
    parser.add_argument("--target-score", type=float, default=5.0, help="Target weighted score to reach")
    parser.add_argument("--max-iters", type=int, default=5, help="Maximum number of optimization iterations")
    parser.add_argument("--workers", type=int, default=4, help="Parallel workers for evaluation")
    args = parser.parse_args()

    optimize_loop(args.target_score, args.max_iters, args.workers)



#!/usr/bin/env python3
import subprocess
import argparse
import sys
from concurrent.futures import ThreadPoolExecutor, as_completed

def run_one(i, n, script_path, generator_url, output_dir):
    try:
        result = subprocess.run(
            ["python3", script_path, "--generator-url", generator_url, "--output-dir", output_dir],
            check=True,
            capture_output=True,
            text=True,
        )
        # Forward the script's stdout (progress lines) prefixed with worker index
        for line in result.stdout.splitlines():
            print(f"[{i+1}/{n}] {line}", flush=True)
        return True
    except subprocess.CalledProcessError as e:
        for line in (e.stdout or "").splitlines():
            print(f"[{i+1}/{n}] {line}", flush=True)
        for line in (e.stderr or "").splitlines():
            print(f"[{i+1}/{n}] [stderr] {line}", file=sys.stderr, flush=True)
        print(f"[{i+1}/{n}] failed", file=sys.stderr, flush=True)
        return False

def main():
    parser = argparse.ArgumentParser(description="Run thread generation N times, optionally in parallel.")
    parser.add_argument("n", type=int, help="Number of threads to generate")
    parser.add_argument("--generator-url", type=str, required=True, help="Direct LLM endpoint to generate questions")
    parser.add_argument("--output-dir", type=str, default="data/synthetic", help="Directory to save JSON files")
    parser.add_argument("--workers", type=int, default=1, help="Number of parallel workers (default: 1)")

    args = parser.parse_args()

    script_path = "scripts/generate_thread.py"
    workers = min(args.workers, args.n)

    print(f"Generating {args.n} thread(s) with {workers} worker(s)...")

    succeeded = 0
    failed = 0

    with ThreadPoolExecutor(max_workers=workers) as executor:
        futures = {executor.submit(run_one, i, args.n, script_path, args.generator_url, args.output_dir): i for i in range(args.n)}
        for future in as_completed(futures):
            if future.result():
                succeeded += 1
            else:
                failed += 1

    print(f"\nDone: {succeeded} succeeded, {failed} failed.")
    if failed:
        sys.exit(1)

if __name__ == "__main__":
    main()

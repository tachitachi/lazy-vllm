#!/usr/bin/env python3
import subprocess
import argparse
import sys

def main():
    parser = argparse.ArgumentParser(description="Run thread generation N times.")
    parser.add_argument("n", type=int, help="Number of threads to generate")
    parser.add_argument("--generator-url", type=str, required=True, help="Direct LLM endpoint to generate questions")
    parser.add_argument("--output-dir", type=str, default="data/synthetic", help="Directory to save JSON files")
    
    args = parser.parse_args()
    
    script_path = "scripts/generate_thread.py"
    
    for i in range(args.n):
        print(f"Generating thread {i+1}/{args.n}...")
        try:
            subprocess.run(
                ["python3", script_path, "--generator-url", args.generator_url, "--output-dir", args.output_dir],
                check=True
            )
        except subprocess.CalledProcessError as e:
            print(f"Error generating thread {i+1}: {e}")
            continue

if __name__ == "__main__":
    main()

#!/usr/bin/env python3
import json
import random
import requests
import sys
import os
import argparse
import uuid

# Configuration
DEFAULT_ROUTER_URL = "http://localhost:8001/v1/chat/completions"
DEFAULT_GENERATOR_URL = "http://localhost:8000/v1/chat/completions"
MODEL = "Qwen3.6-35B-A3B-NVFP4"

def generate_response(llm_url, history):
    """
    Generates a real assistant response given the current conversation history.
    """
    payload = {
        "model": MODEL,
        "messages": history,
        "temperature": 0.7,
        "chat_template_kwargs": {"enable_thinking": False},
    }

    try:
        response = requests.post(llm_url, json=payload)
        response.raise_for_status()
        return response.json()["choices"][0]["message"]["content"].strip()
    except Exception as e:
        print(f"  [error] generating response: {e}", file=sys.stderr)
        return None


def generate_question(llm_url, target_type, context=None):
    """
    Asks an LLM to generate a question that should be classified as target_type.
    """
    domains = (
        "coding (debugging, algorithms, system design, APIs, performance), "
        "software and DevOps (Docker, CI/CD, databases, networking, Linux), "
        "mathematics and logic, science, history, language and writing, "
        "everyday practical tasks, or casual conversation"
    )

    if target_type == "DIRECT":
        type_description = (
            "direct and factual — a concise question that can be answered with a single fact, "
            "a command, a definition, or a short instruction without requiring multi-step "
            "reasoning (e.g., 'How do I check open ports in Linux?', 'What is the syntax for a "
            "Python decorator?', 'What does the git command git stash do?'). Avoid overly trivial "
            "trivia like 'What is the capital of France?' or 'What is 2+2?'."
        )
    else:
        type_description = (
            "cognitively demanding — it requires multi-step reasoning, synthesis, analysis, "
            "debugging, design trade-offs, or explanation of complex concepts "
            "(e.g. 'Why does this Python snippet cause a RecursionError and how would you fix it?', "
            "'Design a rate-limiter for a distributed API gateway', "
            "'Explain the CAP theorem and when you would sacrifice consistency for availability')"
        )

    if context:
        prompt = (
            f"You are a curious user continuing a conversation. "
            f"Previous conversation:\n{context}\n\n"
            f"Generate a single natural follow-up question from the user. "
            f"The question must be {type_description}. "
            f"Vary the subject matter freely across domains such as {domains}. "
            "Ensure the question is unique, avoids common clichés, and is highly relevant to the specified domain and logically follows the previous answer. "
            "Do not repeat common easy questions. "
            "Output ONLY the question text with no preamble, quotes, or explanation."
        )
    else:
        domain_hint = random.choice([
            "software engineering or coding",
            "DevOps, infrastructure, or Linux",
            "algorithms or data structures",
            "mathematics or logic puzzles",
            "science or technology concepts",
            "history, language, or culture",
            "everyday practical tasks",
            "casual conversation or small talk",
        ])
        prompt = (
            f"Generate a single user question for a chat assistant. "
            f"The question must be {type_description}. "
            f"Pick a topic from the domain: {domain_hint}. "
            "Ensure the question is unique, avoids common clichés, and is highly relevant to the specified domain and logically follows the previous answer. "
            "Do not repeat common easy questions. "
            "Output ONLY the question text with no preamble, quotes, or explanation."
        )

    payload = {
        "model": MODEL,
        "messages": [{"role": "user", "content": prompt}],
        "chat_template_kwargs": {"enable_thinking": False},
    }

    try:
        response = requests.post(llm_url, json=payload)
        response.raise_for_status()
        content = response.json()["choices"][0]["message"]["content"].strip()
        content = content.strip('"').strip("'")
        return content
    except Exception as e:
        print(f"  [error] generating question: {e}", file=sys.stderr)
        return None

def run_thread(router_url, generator_url, output_dir):
    thread_id = str(uuid.uuid4())
    history = []
    labels = []

    num_messages = 6
    print(f"[thread {thread_id[:8]}] generating {num_messages} message(s)")
    current_context = None

    for i in range(num_messages):
        target = random.choice(["DIRECT", "REASONING"])
        labels.append(target)

        print(f"  [{i+1}/{num_messages}] generating {target} question...", end=" ", flush=True)
        question = generate_question(generator_url, target, current_context)
        if not question:
            print("failed")
            print(f"[thread {thread_id[:8]}] aborted at step {i+1}", file=sys.stderr)
            return None
        print(f"ok  →  {question[:80]}{'...' if len(question) > 80 else ''}")

        history.append({"role": "user", "content": question})

        print(f"  [{i+1}/{num_messages}] generating assistant response...", end=" ", flush=True)
        answer = generate_response(generator_url, history)
        if not answer:
            print("failed")
            print(f"[thread {thread_id[:8]}] aborted at step {i+1}", file=sys.stderr)
            return None
        print(f"ok  →  {answer[:80]}{'...' if len(answer) > 80 else ''}")

        history.append({"role": "assistant", "content": answer})
        current_context = json.dumps(history)

    data = {
        "thread_id": thread_id,
        "history": history,
        "labels": labels
    }

    filename = os.path.join(output_dir, f"thread_{thread_id}.json")
    with open(filename, "w") as f:
        json.dump(data, f, indent=2)

    print(f"[thread {thread_id[:8]}] saved → {filename}")
    return filename

if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Generate synthetic chat threads for router evaluation.")
    parser.add_argument("--router-url", type=str, default=DEFAULT_ROUTER_URL, help="Router endpoint")
    parser.add_argument("--generator-url", type=str, default=DEFAULT_GENERATOR_URL, help="Direct LLM endpoint to generate questions")
    parser.add_argument("--output-dir", type=str, default="data/synthetic", help="Directory to save JSON files")

    args = parser.parse_args()

    if not os.path.exists(args.output_dir):
        os.makedirs(args.output_dir)

    result = run_thread(args.router_url, args.generator_url, args.output_dir)
    if result:
        print(result)
    else:
        sys.exit(1)

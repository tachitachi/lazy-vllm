#!/usr/bin/env python3
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

def generate_question(llm_url, target_type, context=None):
    """
    Asks an LLM to generate a question that should be classified as target_type.
    """
    if context:
        prompt = (
            f"You are a user in a chat. Based on the previous conversation: {context}, "
            f"generate a follow-up question that is clearly {target_type} in nature. "
            f"A {target_type} question is one that is {'trivial and requires no reasoning' if target_type == 'DIRECT' else 'complex and requires deep reasoning'}. "
            "Respond ONLY with the question text and nothing else."
        )
    else:
        prompt = (
            f"Generate a user question that should be classified as {target_type} by a router. "
            f"A {target_type} question is one that is {'trivial and requires no reasoning' if target_type == 'DIRECT' else 'complex and requires deep reasoning'}. "
            "Respond ONLY with the question text and nothing else."
        )

    payload = {
        "model": "google/gemma-4-26B-A4B-it",
        "messages": [{"role": "user", "content": prompt}],
        "temperature": 0.8,
    }

    try:
        response = requests.post(llm_url, json=payload)
        response.raise_for_status()
        content = response.json()["choices"][0]["message"]["content"].strip()
        content = content.strip('"').strip("'")
        return content
    except Exception as e:
        print(f"Error generating question: {e}", file=sys.stderr)
        return None

def run_thread(router_url, generator_url, output_dir):
    thread_id = str(uuid.uuid4())
    history = []
    labels = []
    
    num_messages = random.randint(1, 6)
    current_context = None
    
    for i in range(num_messages):
        target = random.choice(["DIRECT", "REASONING"])
        labels.append(target)
        
        question = generate_question(generator_url, target, current_context)
        if not question:
            print(f"Failed to generate question for thread {thread_id} at step {i}")
            return None
        
        history.append({"role": "user", "content": question})
        current_context = str(history)
        history.append({"role": "assistant", "content": "I understand. How can I help further?"})

    data = {
        "thread_id": thread_id,
        "history": history,
        "labels": labels
    }
    
    filename = os.path.join(output_dir, f"thread_{thread_id}.json")
    with open(filename, "w") as f:
        json.dump(data, f, indent=2)
    
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


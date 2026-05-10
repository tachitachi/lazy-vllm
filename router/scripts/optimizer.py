import json
import argparse
import requests
import sys

MODEL = "Qwen3.6-35B-A3B-NVFP4"

def optimize_prompt(llm_url: str, report_path: str, current_prompt: str) -> str:
    """
    Sends the report and current prompt to an LLM to generate an improved prompt.
    """
    try:
        with open(report_path, 'r') as f:
            report_content = f.read()
    except Exception as e:
        print(f"Error reading report: {e}")
        sys.exit(1)

    prompt = (
        "You are an expert Prompt Engineer. Your task is to optimize a Router System Prompt "
        "to improve its classification accuracy and reduce critical errors (False Negatives).\n\n"
        "### Current System Prompt:\n"
        f"\"\"\"\n{current_prompt}\n\"\"\"\n\n"
        "### Evaluation Report:\n"
        f"{report_content}\n\n"
        "### Instructions:\n"
        "1. Analyze the 'Misses' table in the report to identify patterns in misclassification.\n"
        "2. If there are many False Negatives, the current prompt is too lenient on 'DIRECT' classification. "
        "Make the 'REASONING' criteria more robust and explicit.\n"
        "3. If there are many False Positives, the prompt is too aggressive in 'REASONING'. "
        "Refine the 'DIRECT' criteria to be clearer.\n"
        "4. Aim for a 'giant leap' improvement in a single version.\n"
        "5. Output the new prompt as a JSON object with the key 'new_prompt'.\n"
        "6. Do not include any preamble, explanation, or markdown code blocks in your response. "
        "Return ONLY the JSON object.\n\n"
        "Example Output:\n"
        "{\"new_prompt\": \"Your new optimized prompt text here...\"}"
    )

    payload = {
        "model": MODEL,
        "messages": [{"role": "user", "content": prompt}],
        "chat_template_kwargs": {"enable_thinking": True},
    }

    try:
        response = requests.post(llm_url, json=payload)
        response.raise_for_status()
        content = response.json()["choices"][0]["message"]["content"].strip()

        # Clean up markdown if present
        if content.startswith("```json"):
            content = content.split("```json")[1].split("```")[0].strip()
        elif content.startswith("```"):
            content = content.split("```")[1].split("```")[0].strip()

        data = json.loads(content)
        return data["new_prompt"]
    except Exception as e:
        print(f"Error during optimization: {e}")
        sys.exit(1)

if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Optimize a prompt based on an evaluation report.")
    parser.add_argument("--report", type=str, required=True, help="Path to the evaluation report (Markdown)")
    parser.add_argument("--current-prompt", type=str, required=True, help="The current system prompt")
    parser.add_argument("--generator-url", type=str, required=True, help="LLM endpoint for optimization")

    args = parser.parse_args()

    new_prompt = optimize_prompt(args.generator_url, args.report, args.current_prompt)
    print(new_prompt)

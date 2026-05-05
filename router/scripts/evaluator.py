import json
import dataclasses
import time
import random
import requests
from typing import List, Optional, Literal, Dict, Any

@dataclasses.dataclass
class EvaluationResult:
    query: str
    ground_truth_label: Literal["DIRECT", "REASONING"]
    router_label: Literal["DIRECT", "REASONING"]
    latency_ms: float
    token_usage: int
    error_category: Optional[Literal["Complexity Error", "Ambiguity Error", "Domain Error"]] = None
    error_reason: Optional[str] = None

@dataclasses.dataclass
class EvaluationRun:
    timestamp: str
    prompt_version: str
    model_name: str
    results: List[EvaluationResult]

    def to_dict(self) -> Dict[str, Any]:
        return dataclasses.asdict(self)

def compare_labels(router_label: str, ground_truth_label: str) -> bool:
    """
    Returns True if labels match, False otherwise.
    """
    return router_label == ground_truth_label

def identify_error_type(router_label: str, ground_truth_label: str) -> Literal["FN", "FP", "NONE"]:
    """
    Identifies if the mismatch is a False Negative or False Positive.
    """
    if router_label == ground_truth_label:
        return "NONE"
    
    if ground_truth_label == "REASONING" and router_label == "DIRECT":
        return "FN"
    
    if ground_truth_label == "DIRECT" and router_label == "REASONING":
        return "FP"
    
    return "NONE"

def analyze_error(
    llm_url: str, 
    query: str, 
    ground_truth: str, 
    router_output: str
) -> Dict[str, str]:
    """
    Asynchronously (simulated via requests) calls an LLM to categorize the error.
    """
    prompt = (
        f"You are an expert error analyst for an LLM router. "
        f"A user query was misclassified.\n\n"
        f"User Query: '{query}'\n"
        f"Expected Label: {ground_truth}\n"
        f"Actual Router Label: {router_output}\n\n"
        f"Categorize the error into exactly one of the following categories:\n"
        f"- Complexity Error: The router failed to recognize the depth of the reasoning required.\n"
        f"- Ambiguity Error: The query is too vague or could be interpreted both ways.\n"
        f"- Domain Error: The router struggles with this specific subject matter.\n\n"
        f"Provide a concise, one-sentence explanation of why the failure occurred. "
        f"Format your response as a JSON object with keys 'category' and 'reason'. "
        f"Example: {{\"category\": \"Complexity Error\", \"reason\": \"The query required multi-step logic that the router dismissed as a simple fact.\"}}\n\n"
        f"Return ONLY the JSON object."
    )

    payload = {
        "model": "google/gemma-4-26B-A4B-it",
        "messages": [{"role": "user", "content": prompt}],
        "temperature": 0.0,
        "chat_template_kwargs": {"enable_thinking": False},
    }

    try:
        response = requests.post(llm_url, json=payload)
        response.raise_for_status()
        analysis = response.json()["choices"][0]["message"]["content"].strip()
        # Clean up potential markdown code blocks
        if analysis.startswith("```json"):
            analysis = analysis.split("```json")[1].split("```")[0].strip()
        elif analysis.startswith("```"):
            analysis = analysis.split("```")[1].split("```")[0].strip()
        
        return json.loads(analysis)
    except Exception as e:
        print(f"  [error] analyzing error: {e}")
        return {"category": "Complexity Error", "reason": "Failed to analyze error via LLM."}

def calculate_weighted_score(results: List[EvaluationResult]) -> float:
    """
    Computes the performance metric using a Cost-Weighted Penalty Function.
    """
    score = 0.0
    for res in results:
        if res.router_label == res.ground_truth_label:
            score += 1.0
        else:
            # Mismatch
            if res.ground_truth_label == "REASONING" and res.router_label == "DIRECT":
                # False Negative (FN)
                score -= 5.0
            elif res.ground_truth_label == "DIRECT" and res.router_label == "REASONING":
                # False Positive (FP)
                score -= 0.5
    return score

def generate_optimizer_report(results: List[EvaluationResult]) -> str:
    """
    Compiles results into a structured Markdown report.
    """
    total = len(results)
    if total == 0:
        return "No results to report."

    correct = sum(1 for r in results if r.router_label == r.ground_truth_label)
    accuracy = (correct / total) * 100
    weighted_score = calculate_weighted_score(results)
    
    fps = sum(1 for r in results if r.ground_truth_label == "DIRECT" and r.router_label == "REASONING")
    fns = sum(1 for r in results if r.ground_truth_label == "REASONING" and r.router_label == "DIRECT")

    report = [
        "# Router Evaluation Report",
        "",
        "## Summary Stats",
        f"- **Total Queries:** {total}",
        f"- **Accuracy:** {accuracy:.2f}%",
        f"- **Weighted Score:** {weighted_score:.2f}",
        f"- **False Positives (Wasteful):** {fps}",
        f"- **False Negatives (Dangerous):** {fns}",
        "",
        "## The \"Misses\" Table",
        "| Query | Expected | Actual | Category | Reason |",
        "|---|---|---|---|---|",
    ]

    mismatches = [r for r in results if r.router_label != r.ground_truth_label]
    for m in mismatches:
        report.append(f"| {m.query[:50]}... | {m.ground_truth_label} | {m.router_label} | {m.error_category} | {m.error_reason} |")

    report.extend([
        "",
        "## The \"Hits\" Sample",
        "| Query | Expected | Actual |",
        "|---|---|---|",
    ])

    hits = random.sample([r for r in results if r.router_label == r.ground_truth_label], min(len(results), 3))
    for h in hits:
        report.append(f"| {h.query[:50]}... | {h.ground_truth_label} | {h.router_label} |")

    return "\n".join(report)

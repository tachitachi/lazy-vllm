import json
import dataclasses
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

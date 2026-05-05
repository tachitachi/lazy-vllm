# Implementation Plan: Routing Evaluator Module

**Objective:** Build a system that compares the outputs of a "Router Agent" against "Ground Truth" labels, performs error analysis on mismatches using an LLM, and calculates a cost-weighted performance score.

### 1. Data Schema Definition
Define a standardized JSON schema for the evaluation results. Every evaluation run must produce a `results.json` containing:
*   **Input:** `query`, `ground_truth_label` (`DIRECT` or `REASONING`).
*   **Output:** `router_label` (`DIRECT` or `REASONING`), `latency_ms`, `token_usage`.
*   **Analysis (only if mismatch):** `error_category`, `error_reason`.
*   **Metadata:** `timestamp`, `prompt_version`, `model_name`.

### 2. Comparison & Logic Engine (Python)
Implement a core function `compare_labels(router_label, ground_truth_label)` that:
*   Returns `True` if labels match.
*   Returns `False` if labels mismatch.
*   Identifies the **Error Type**:
    *   **False Negative (FN):** Ground Truth is `REASONING`, but Router is `DIRECT`.
    *   **False Positive (FP):** Ground Truth is `DIRECT`, but Router is `REASONING`.

### 3. Error Analyst Module (LLM-based)
Implement an asynchronous function `analyze_error(query, ground_truth, router_output)` that triggers **only when a mismatch occurs**.
*   **Task:** Compare the query against the labels and categorize the failure.
*   **Required Categories:** `Complexity Error`, `Ambiguity Error`, `Domain Error`.
*   **Prompt Requirement:** The analyst must output a concise, one-sentence explanation of *why* the router failed.

### 4. Weighted Scoring Engine (Mathematical)
Implement a function `calculate_weighted_score(results)` to compute the performance metric. Do not use simple accuracy. Use a **Cost-Weighted Penalty Function**:
*   **Correct DIRECT (TP):** +1.0 points.
*   **Correct REASONING (TP):** +1.0 points.
*   **False Positive (FP - Wasteful):** -0.5 points (penalty for unnecessary reasoning/latency).
*   **False Negative (FN - Dangerous):** -5.0 points (heavy penalty for giving a direct answer to a complex question).
*   **Formula:** $Score = \sum(Correct) - \sum(FP \times 0.5) - \sum(FN \times 5.0)$

### 5. Report Aggregator (Optimizer Input)
Implement a function `generate_optimizer_report(results)` that compiles the data into a structured Markdown report for the Optimizer LLM. The report must include:
1.  **Summary Stats:** Total queries, Accuracy, Weighted Score, and the breakdown of FP vs FN.
2.  **The "Misses" Table:** A Markdown table containing all mismatched queries, their expected vs. actual labels, the error category, and the Analyst's reason.
3.  **The "Hits" Sample:** A small, randomized sample of successful queries to provide context of what is working.

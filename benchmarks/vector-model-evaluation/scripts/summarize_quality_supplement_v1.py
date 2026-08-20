from __future__ import annotations

import json
from pathlib import Path


ROOT = Path(r"E:\Dev\ownward\.tmp\vector-model-evaluation")
RESULTS = ROOT / "results" / "quality-supplement-v1"
MODELS = ("bge_m3", "embeddinggemma_300m", "qwen3_embedding_0_6b")


def aggregate(items: list[dict[str, object]]) -> dict[str, float | int]:
    ranks = [int(item["rank"]) if item["rank"] is not None else None for item in items]
    return {
        "count": len(items),
        "top1_accuracy": sum(rank == 1 for rank in ranks) / len(ranks),
        "mrr_at_10": sum(1.0 / rank if rank is not None else 0.0 for rank in ranks) / len(ranks),
        "recall_at_5": sum(rank is not None and rank <= 5 for rank in ranks) / len(ranks),
        "recall_at_10": sum(rank is not None for rank in ranks) / len(ranks),
    }


def main() -> None:
    combined: dict[str, object] = {"models": {}}
    for model in MODELS:
        path = RESULTS / model / "per-query.jsonl"
        items = [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines() if line]
        categories = sorted({str(item["category"]) for item in items})
        combined["models"][model] = {
            "overall": aggregate(items),
            "by_category": {
                category: aggregate([item for item in items if item["category"] == category])
                for category in categories
            },
            "per_query": {str(item["query_id"]): item["rank"] for item in items},
        }

    model_metrics = combined["models"]
    ranking = sorted(
        MODELS,
        key=lambda model: (
            -float(model_metrics[model]["overall"]["top1_accuracy"]),
            -float(model_metrics[model]["overall"]["mrr_at_10"]),
            -float(model_metrics[model]["overall"]["recall_at_5"]),
            model,
        ),
    )
    paired: dict[str, object] = {}
    for left_index, left in enumerate(MODELS):
        for right in MODELS[left_index + 1 :]:
            left_ranks = model_metrics[left]["per_query"]
            right_ranks = model_metrics[right]["per_query"]
            wins = losses = ties = 0
            for query_id in sorted(left_ranks):
                left_rank = int(left_ranks[query_id]) if left_ranks[query_id] is not None else 11
                right_rank = int(right_ranks[query_id]) if right_ranks[query_id] is not None else 11
                if left_rank < right_rank:
                    wins += 1
                elif left_rank > right_rank:
                    losses += 1
                else:
                    ties += 1
            paired[f"{left}__vs__{right}"] = {
                "left_wins": wins,
                "right_wins": losses,
                "ties": ties,
            }
    combined["ranking_by_frozen_rule"] = list(ranking)
    combined["paired_rank_comparison"] = paired
    output = RESULTS / "comparison.json"
    output.write_text(
        json.dumps(combined, ensure_ascii=False, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
        newline="\n",
    )
    print(json.dumps(combined, ensure_ascii=False, indent=2, sort_keys=True))


if __name__ == "__main__":
    main()

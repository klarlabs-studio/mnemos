"""CLI entry for the benchmark harness."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
from datetime import datetime

from .providers.mnemos import MnemosProvider
from .providers.mem0 import Mem0Provider
from .providers.zep import ZepProvider
from .providers.letta import LettaProvider
from .suites import contradiction_detection, locomo, longmemeval, real_trace_recall

PROVIDERS = {
    "mnemos": MnemosProvider,
    "mem0": Mem0Provider,
    "zep": ZepProvider,
    "letta": LettaProvider,
}

SUITES = {
    "contradiction_detection": contradiction_detection.run,
    "longmemeval": longmemeval.run,
    "locomo": locomo.run,
    "real_trace_recall": real_trace_recall.run,
    # replay_completeness, evidence_traceability follow.
}


def main() -> int:
    p = argparse.ArgumentParser(prog="bench", description="Mnemos cross-provider benchmark")
    p.add_argument("--provider", choices=[*PROVIDERS, "all"], required=True)
    p.add_argument("--suite", choices=[*SUITES, "all"], required=True)
    p.add_argument("--output", default="benchmarks/results", help="Directory for JSON results")
    p.add_argument(
        "--split",
        default="holdout",
        choices=["train", "validation", "holdout", "all"],
        help="Dataset split for suites that support split-aware evaluation",
    )
    args = p.parse_args()

    providers = list(PROVIDERS) if args.provider == "all" else [args.provider]
    suites = list(SUITES) if args.suite == "all" else [args.suite]

    out_dir = Path(args.output)
    out_dir.mkdir(parents=True, exist_ok=True)
    ts = datetime.utcnow().strftime("%Y%m%dT%H%M%SZ")

    for prov_name in providers:
        provider = PROVIDERS[prov_name]()
        for suite_name in suites:
            suite_fn = SUITES[suite_name]
            print(f"→ {prov_name} :: {suite_name}")
            if suite_name == "real_trace_recall":
                result = suite_fn(provider, split=args.split)
            else:
                result = suite_fn(provider)
            agg = result.aggregate()
            payload = {
                "provider": prov_name,
                "suite": suite_name,
                "timestamp": ts,
                "summary": agg,
                "cases": [c.__dict__ for c in result.cases],
            }
            outfile = out_dir / f"{prov_name}-{suite_name}-{ts}.json"
            outfile.write_text(json.dumps(payload, indent=2))
            print(f"   {agg}")
            print(f"   wrote {outfile}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

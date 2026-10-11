"""Publish only the demo clock configuration beside the static GUI."""

import json
import os
from pathlib import Path


def write_runtime(destination, fixed_now):
    """Write configured clock metadata; the Go processes validate the timestamp."""
    metadata = {"clock_mode": "fixed" if fixed_now else "real"}
    if fixed_now:
        metadata["fixed_now"] = fixed_now
    Path(destination).write_text(json.dumps(metadata) + "\n", encoding="utf-8")


if __name__ == "__main__":
    write_runtime("/tmp/e2b-demo-runtime/runtime.json", os.environ.get("E2B_DEMO_NOW", ""))

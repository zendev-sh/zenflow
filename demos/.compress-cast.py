#!/usr/bin/env python3
"""
Compress idle gaps in an asciinema v3 .cast file beyond what
--idle-time-limit caps at recording time.

asciinema v3 stores each frame as [delta_seconds, event_type, data]
where delta_seconds is the gap from the PREVIOUS frame (NOT
cumulative).

Strategy:
  For each frame, look at its delta. If the delta is large AND the
  content is "boring" (spinner-only redraw, ANSI cursor moves with
  no new visible chars, pure whitespace, dot/idle markers), collapse
  to MIN_GAP. Otherwise clamp to MAX_GAP.

Usage:
  python3 .compress-cast.py input.cast output.cast [--max-gap S] [--min-gap S]

Defaults:
  --max-gap 0.6    cap visible-content gaps to this
  --min-gap 0.10   collapse boring/idle gaps to this
"""

import json
import re
import sys
from pathlib import Path

ANSI_RE = re.compile(r"\x1b\[[0-9;?]*[a-zA-Z]|\x1b\][^\x07]*\x07|\r")
SPINNER_CHARS = set("⠁⠂⠄⡀⢀⠠⠐⠈ ●○◐◑◒◓⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏◎·≋")


def classify(data: str) -> str:
    stripped = ANSI_RE.sub("", data)
    if not stripped.strip():
        return "boring"
    chars = [c for c in stripped if not c.isspace()]
    if chars and all(c in SPINNER_CHARS for c in chars):
        return "boring"
    return "visible"


def compress(in_path: Path, out_path: Path, max_gap: float, min_gap: float) -> dict:
    lines = in_path.read_text().splitlines()
    if not lines:
        raise SystemExit("empty cast")

    header = json.loads(lines[0])
    out_lines = [json.dumps(header, separators=(",", ":"))]

    in_total = 0.0
    out_total = 0.0
    boring_count = 0
    visible_clamped = 0
    n_frames = 0

    for raw in lines[1:]:
        if not raw.strip():
            continue
        n_frames += 1
        delta, ev, data = json.loads(raw)
        in_total += delta

        new_delta = delta
        if delta > max_gap:
            kind = classify(data) if ev == "o" else "visible"
            if kind == "boring":
                new_delta = min_gap
                boring_count += 1
            else:
                new_delta = max_gap
                visible_clamped += 1

        out_total += new_delta
        out_lines.append(json.dumps([round(new_delta, 6), ev, data], separators=(",", ":")))

    out_path.write_text("\n".join(out_lines) + "\n")
    return {
        "input_duration_s": round(in_total, 2),
        "output_duration_s": round(out_total, 2),
        "input_min": round(in_total / 60, 2),
        "output_min": round(out_total / 60, 2),
        "boring_gaps_compressed": boring_count,
        "visible_gaps_clamped": visible_clamped,
        "frames": n_frames,
    }


def main():
    args = sys.argv[1:]
    if len(args) < 2:
        print(__doc__)
        sys.exit(2)
    in_path = Path(args[0])
    out_path = Path(args[1])
    max_gap = 0.6
    min_gap = 0.10
    i = 2
    while i < len(args):
        if args[i] == "--max-gap":
            max_gap = float(args[i + 1]); i += 2
        elif args[i] == "--min-gap":
            min_gap = float(args[i + 1]); i += 2
        else:
            print(f"unknown arg {args[i]!r}"); sys.exit(2)

    stats = compress(in_path, out_path, max_gap, min_gap)
    for k, v in stats.items():
        print(f"{k}: {v}")


if __name__ == "__main__":
    main()

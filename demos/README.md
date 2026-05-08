# demos

Asciinema casts and derived GIFs that drive the docsite landing page
and the GitHub README header. Casts are the source of truth; GIFs
are regenerated from them deterministically.

## Files

| File | Source | Purpose |
| --- | --- | --- |
| [`full-featured.cast`](full-featured.cast) | Real run of `zenflow flow spec/v1/examples/full-featured.yaml --model google/gemini-3-flash-preview --workdir /tmp/full-feature-gemini --yolo --plan` on 2026-05-07. Recorded with `asciinema 3.x --idle-time-limit 1.0 --window-size 110x36`. ~282 s playback. | Local source-of-truth cast (uncompressed). |
| [`full-featured-c1.cast`](full-featured-c1.cast) | Derived from `full-featured.cast` via `.compress-cast.py --max-gap 0.6 --min-gap 0.1`. ~56 s playback. | Compressed cast uploaded to asciinema.org as [`T6ghM70jlJEth4Ez`](https://asciinema.org/a/T6ghM70jlJEth4Ez); embedded on the docsite landing page, README header, and coordinator concept page. |
| [`full-featured-c1.gif`](full-featured-c1.gif) | Derived from `full-featured-c1.cast` via `agg` + `gifsicle`. | GitHub README inline (no JS available there). |
| [`debate-until.cast`](debate-until.cast) | Real run of `zenflow flow spec/v1/examples/debate-until.yaml` against `google/gemini-2.0-flash` on 2026-05-04. Recorded with `asciinema 3.2 --idle-time-limit 2 --window-size 110x32`. Uploaded to asciinema.org as [`nMwrF116eEnn17bh`](https://asciinema.org/a/nMwrF116eEnn17bh). | Embedded specifically in the `### debate-until` section of `docs/examples.md`. |
| [`debate-until.gif`](debate-until.gif) | Derived from the cast via `agg` + `gifsicle`. | Companion GIF for the debate-until example block. |
| [`Makefile`](Makefile) | `make demo-gif` regenerates the GIF. | Use after changing `agg` / `gifsicle` flags. |

## Re-record policy

Each cast freezes a single LLM run. **Do not re-record on a whim.**
Each re-recording hits a real model and produces non-deterministic
output (different agent prose, different total step count, different
tool-call ordering). Re-record only when:

- the CLI surface visibly changes (new sink glyphs, renamed
  command flag, different progress format)
- the messaging contract renames (`forward_to_agent`,
  `send_message`, lifecycle events)
- the demo workflow file moves or its agents change shape

Stylistic prose differences from the LLM are not a reason to
re-record.

## Re-record procedure (rare)

### Full-featured (landing-page hero)

```bash
# From the zenflow/ directory
go build -o zenflow ./cmd/zenflow/

# .env should export GEMINI_API_KEY in the parent shell;
# the wrapper sources it on its own behalf too.
asciinema rec \
  --command "./demos/.record-full-featured.sh" \
  --idle-time-limit 1.0 \
  --title "zenflow flow full-featured demo" \
  --window-size 110x36 \
  --overwrite \
  demos/full-featured.cast

# Compress idle gaps (workflow runs ~5 minutes wall-clock; this
# brings playback under one minute without losing visible output).
python3 demos/.compress-cast.py \
  demos/full-featured.cast \
  demos/full-featured-c1.cast \
  --max-gap 0.6 --min-gap 0.1

# Render the GIF from the compressed cast.
agg --theme monokai --font-size 14 \
  demos/full-featured-c1.cast demos/full-featured-c1.tmp.gif
gifsicle -O3 --lossy=80 \
  -o demos/full-featured-c1.gif demos/full-featured-c1.tmp.gif
rm demos/full-featured-c1.tmp.gif

# Re-upload to asciinema.org (replaces the existing T6ghM70jlJEth4Ez
# entry; requires the asciinema CLI to be authenticated against the
# account that owns the cast).
asciinema upload demos/full-featured-c1.cast
```

### Debate-until (legacy example block)

```bash
asciinema rec \
  --command "./demos/.record-debate-until.sh" \
  --idle-time-limit 2.0 \
  --title "zenflow flow debate-until demo" \
  --window-size 110x32 \
  --overwrite \
  demos/debate-until.cast

cd demos && make demo-gif
```

Inspect each cast first with `asciinema play demos/<name>.cast`
before regenerating the GIF.

## What the full-featured demo shows

A four-agent feature delivery on a Go service:

- `planner` reads the architecture + API spec, surveys the codebase
  with `glob` / `grep`, and produces an implementation plan.
- `coder` writes Go (`internal/storage`, `internal/service`,
  `internal/api`), runs `go build`, scaffolds tests.
- `reviewer` audits the diff and the test suite.
- `deployer` (in the `deploy_staging` sub-workflow loaded via
  `includes:`) builds and runs the binary, then verifies the
  staging deployment.

This is the workflow surface chosen for the landing-page demo because:

- It exercises the coordinator narration (`narrate` tool fires on
  every step lifecycle event, including sub-workflow boundaries).
- It exercises every YAML field the spec defines (agents, tools,
  conditions, contextFiles, includes, options).
- It exercises the `--plan` flag (DAG is printed before execution).
- It exercises the `--yolo` permission mode (auto-approve every
  tool call) and `--workdir` (sandboxed scratch directory).
- It runs in ~5 minutes wall-clock against `gemini-3-flash-preview`,
  compressed to ~56 s of playback for the docsite.

## What the debate-until demo shows

A four-agent debate on "Remote work should be the default for all
knowledge workers." After the moderator sets the topic, a
`repeat-until` loop alternates `pro-argue` and `con-argue` rounds
with the coordinator narrating each side's contribution. The judge
agent (`untilAgent`) terminates the loop when the debate is
repetitive or one side is clearly winning. A `summarizer` agent
renders the verdict.

Kept as a focused demo for the `### debate-until` example because
it exercises the loop primitive plus the `untilAgent` termination
contract more cleanly than the larger full-featured workflow.

## Sizing budgets

- Cast file: **target under 30 KB** (`full-featured-c1.cast` is
  ~26 KB; `debate-until.cast` is ~15 KB).
- GIF file: **hard cap 2 MB** for embedding in the GitHub README.
  Tune `LOSSY` in the Makefile if `gifsicle` exceeds this. The
  full-featured GIF is ~5 MB at `--lossy=80` because the workflow
  outputs significantly more text than the debate; consider
  bumping `--lossy=120` or dropping the GIF in favour of the
  asciinema embed if the size becomes a problem.

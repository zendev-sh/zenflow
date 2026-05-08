# zenflow brand palette

Tokens are shared with the parent zendev brand palette so the
OSS docsite stays visually consistent with the marketing site.
Same HSL navy spine, same Outfit + JetBrains Mono pair, same
terminal-syntax accent set. The ensō calligraphy that ships in
the icon assets uses the same primary navy ink.

## Typography

| Use | Family | Weights |
| --- | --- | --- |
| Display + body + chrome | Outfit | 300, 400, 500, 600, 700, 800 |
| Code + machine labels | JetBrains Mono | 400, 500, 600 |

Both load from Google Fonts `display=swap`. No third family.

## Light mode

| Token | Value | Use |
| --- | --- | --- |
| `--zf-bg` | `hsl(220, 30%, 98%)` | Page background |
| `--zf-bg-alt` | `hsl(220, 30%, 95%)` | Section bg alternation |
| `--zf-card` | `hsl(220, 30%, 99%)` | Card surface |
| `--zf-fg` | `hsl(222, 30%, 15%)` | Primary text |
| `--zf-muted-fg` | `hsl(222, 20%, 50%)` | Secondary text |
| `--zf-primary` | `hsl(222, 47%, 20%)` | Filled buttons, brand surfaces |
| `--zf-secondary` | `hsl(220, 25%, 93%)` | Accent fill, code chip bg |
| `--zf-border` | `hsl(220, 20%, 88%)` | Frame strokes |

## Dark mode

| Token | Value | Use |
| --- | --- | --- |
| `--zf-bg` | `hsl(222, 47%, 11%)` | Page background |
| `--zf-bg-alt` | `hsl(222, 47%, 9%)` | Section bg alternation |
| `--zf-card` | `hsl(222, 47%, 14%)` | Card surface |
| `--zf-fg` | `hsl(0, 0%, 98%)` | Primary text |
| `--zf-muted-fg` | `hsl(0, 0%, 64%)` | Secondary text |
| `--zf-primary` | `hsl(0, 0%, 98%)` | Brand surface (inverted) |
| `--zf-secondary` | `hsl(222, 47%, 20%)` | Accent fill |
| `--zf-border` | `hsl(222, 47%, 25%)` | Frame strokes |

## Terminal-syntax accents

Used contextually for code highlight + the link/hover accent.

| Token | Light | Dark |
| --- | --- | --- |
| `--zf-t-prompt` | `hsl(222, 60%, 45%)` | `hsl(210, 60%, 60%)` |
| `--zf-t-cmd` | `hsl(222, 30%, 15%)` | `hsl(0, 0%, 98%)` |
| `--zf-t-flag` (link + hover accent) | `hsl(190, 65%, 38%)` | `hsl(190, 75%, 65%)` |
| `--zf-t-string` | `hsl(190, 50%, 35%)` | `hsl(190, 60%, 70%)` |
| `--zf-t-ok` | `hsl(150, 50%, 35%)` | `hsl(150, 60%, 55%)` |
| `--zf-t-url` | `hsl(210, 60%, 45%)` | `hsl(210, 70%, 65%)` |

`--zf-t-flag` doubles as the link color, button hover, card
corner-tick fill, animated dot fill in the diagrams, and pill
accent on `LLM` badges. **2026-05** migrated this
token from a warm orange/gold (light `hsl(30, 70%, 40%)` ≈
`#ad661f`, dark `hsl(40, 80%, 60%)`) to brand-cyan, synced with
the CLI's `\033[36m` Cyan ANSI code that dominates the runtime
experience (`▸ Starting workflow`, `↻ wake`, step headers). The
site accent and the product accent now match.

## VitePress mapping

| Source token | VitePress token |
| --- | --- |
| `--zf-primary` | `--vp-c-brand-1` |
| `--zf-bg` | `--vp-c-bg` |
| `--zf-bg-alt` | `--vp-c-bg-alt` |
| `--zf-card` | `--vp-c-bg-soft`, `--vp-c-bg-elv` |
| `--zf-fg` | `--vp-c-text-1` |
| `--zf-muted-fg` | `--vp-c-text-2` |
| `--zf-border` | `--vp-c-divider`, `--vp-c-gutter` |
| `--zf-t-flag` | `--vp-c-tip-1` |

Edits to the palette belong here first; propagate to
`style.css` second.

## Logo asset

The dark-variant ensō PNG is regenerated from the light variant
by replacing every opaque RGB pixel with `hsl(0, 0%, 98%)` while
preserving the alpha channel. Light variant keeps its native
navy ink (`#151D49`). This matches zendev's logo treatment.

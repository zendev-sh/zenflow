# demos/Makefile - regenerate the demo GIF from the asciinema cast.
#
# The cast is recorded once (frozen LLM output, see README.md). The GIF
# is derived deterministically. If the GIF needs a refresh after changing
# `agg` / `gifsicle` flags, run:
#
#   cd demos && make demo-gif
#
# Re-recording the cast (rare; only when the CLI surface changes) lives
# in the README.md procedure - this Makefile does not invoke a real LLM.

CAST  := debate-until.cast
GIF   := debate-until.gif

# agg renders cast -> raw GIF; gifsicle compresses with palette-quantization.
THEME      ?= monokai
FONT_SIZE  ?= 14
LOSSY      ?= 80

.PHONY: demo-gif
demo-gif: $(GIF)

$(GIF): $(CAST)
	agg --theme $(THEME) --font-size $(FONT_SIZE) $(CAST) $(GIF).tmp.gif
	gifsicle -O3 --lossy=$(LOSSY) -o $(GIF) $(GIF).tmp.gif
	rm $(GIF).tmp.gif
	@printf 'wrote %s (%s bytes)\n' "$(GIF)" "$$(wc -c < $(GIF) | tr -d ' ')"

.PHONY: clean
clean:
	rm -f $(GIF) $(GIF).tmp.gif

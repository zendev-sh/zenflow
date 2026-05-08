# homebrew/

GoReleaser generates the Homebrew cask automatically from the
`homebrew_casks:` block in `.goreleaser.yaml`; no template needed
here.

The rendered cask lands at `zendev-sh/homebrew-tap/Casks/zenflow.rb`
on every tagged release. Edit `.goreleaser.yaml` if the cask needs
changing - not files in this directory.

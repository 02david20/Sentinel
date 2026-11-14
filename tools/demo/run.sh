#!/usr/bin/env bash
# Re-records docs/demo.mp4 and docs/demo.gif by driving the real TUI in a pseudo-terminal.
# Needs: Go, Python 3, ffmpeg, and macOS fonts (Menlo, Apple Symbols, Arial Unicode); edit the
# font paths in record.py on other systems.
set -euo pipefail
D=$(cd "$(dirname "$0")" && pwd); ROOT=$(cd "$D/../.." && pwd)
cd "$D"
go build -o sentinel "$ROOT/cmd/sentinel"
[ -d venv ] || { python3 -m venv venv && ./venv/bin/pip -q install pyte pillow; }
mkdir -p demo/workflows
bash setup.sh
./venv/bin/python record.py
./venv/bin/python record_plugin.py
ffmpeg -loglevel error -y -framerate 12 -i frames-plugin/f%05d.png -c:v libx264 -pix_fmt yuv420p -crf 20 -movflags +faststart "$ROOT/docs/demo-plugins.mp4"
ffmpeg -loglevel error -y -i "$ROOT/docs/demo-plugins.mp4" -vf "fps=6,scale=820:-1:flags=lanczos,split[a][b];[a]palettegen=max_colors=48[p];[b][p]paletteuse=dither=none" "$ROOT/docs/demo-plugins.gif"
ffmpeg -loglevel error -y -framerate 12 -i frames/f%05d.png -c:v libx264 -pix_fmt yuv420p -crf 20 -movflags +faststart "$ROOT/docs/demo.mp4"
ffmpeg -loglevel error -y -i "$ROOT/docs/demo.mp4" -vf "fps=10,scale=900:-1:flags=lanczos,split[a][b];[a]palettegen=max_colors=64[p];[b][p]paletteuse=dither=none" "$ROOT/docs/demo.gif"

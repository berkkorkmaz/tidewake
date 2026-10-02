#!/usr/bin/env python3
"""Generate the README charts (light and dark SVGs) in docs/img/.

Data: one `tidewake scan --json` and `tidewake sessions` run on the author's
Mac on 2026-10-02 (v0.1.0-rc4). Numbers are copied here as literals so the
images can be regenerated and checked against the source run.

Colors follow the validated reference palette: blue for "can be freed",
orange for "kept on purpose"; text never wears the series color.
"""

from pathlib import Path

OUT = Path(__file__).resolve().parent.parent / "docs" / "img"

FREED_GB = [  # (label, GB) sorted largest first; exact bytes / 1e9 from the scan
    ("Unused Docker images", 14.220),
    ("Scratch files of ended Claude sessions", 9.254),
    ("Old Codex releases", 3.295),
    ("Docker build cache", 2.296),
    ("Finished git worktrees", 1.458),
]
KEPT_WORKTREES = [  # (reason, count) from the same scan: 24 kept, 5.8 GB
    ("Commits that exist only in that worktree", 13),
    ("Ignored files like .env or local data", 9),
    ("Unpushed commits", 1),
    ("Used in the last 48 hours", 1),
]
TILES = [  # (value, label); first three are user reports in public GitHub issues
    ("129 GB", "RAM, one Claude process"),  # anthropics/claude-code#11315
    ("1,300+", "zombie Codex processes"),  # openai/codex#12491
    ("154", "orphaned processes"),  # anthropics/claude-code#17391 (comment)
    ("30.5 GB", "found in one scan"),  # the scan above
]

THEMES = {
    "light": {
        "primary": "#0b0b0b",
        "secondary": "#52514e",
        "muted": "#8a8984",
        "grid": "#e4e3df",
        "blue": "#2a78d6",
        "orange": "#eb6834",
        "tile": "#f3f2ef",
    },
    "dark": {
        "primary": "#f0f6fc",
        "secondary": "#c3c2b7",
        "muted": "#8b8a83",
        "grid": "#30363d",
        "blue": "#3987e5",
        "orange": "#d95926",
        "tile": "#161b22",
    },
}
FONT = "-apple-system, BlinkMacSystemFont, 'Segoe UI', Helvetica, Arial, sans-serif"

WIDTH = 760
LABEL_COL = 300  # x where bars start
BAR_MAX = 360  # longest bar
BAR_H = 22  # <= 24px thick
ROW_H = 38
TOP = 74


def bar_path(x, y, w, h, r=4):
    """Bar with a 4px rounded data-end and a square baseline end."""
    r = min(r, w / 2)
    return (
        f"M{x},{y} h{w - r} a{r},{r} 0 0 1 {r},{r} v{h - 2 * r} "
        f"a{r},{r} 0 0 1 {-r},{r} h{-(w - r)} z"
    )


def esc(s):
    return s.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")


def bar_chart(theme, title, subtitle, rows, color_key, fmt):
    t = THEMES[theme]
    height = TOP + ROW_H * len(rows) + 16
    top = max(v for _, v in rows)
    out = [
        (
            f'<svg xmlns="http://www.w3.org/2000/svg" width="{WIDTH}" height="{height}" '
            f'viewBox="0 0 {WIDTH} {height}" font-family="{FONT}" role="img" aria-label="{esc(title)}">'
        ),
        f"<title>{esc(title)}</title>",
        f'<text x="0" y="24" font-size="17" font-weight="600" fill="{t["primary"]}">{esc(title)}</text>',
        f'<text x="0" y="46" font-size="13" fill="{t["muted"]}">{esc(subtitle)}</text>',
        f'<line x1="{LABEL_COL}" y1="{TOP - 8}" x2="{LABEL_COL}" y2="{height - 12}" stroke="{t["grid"]}" stroke-width="1"/>',
    ]
    for i, (label, value) in enumerate(rows):
        y = TOP + i * ROW_H
        w = max(2, BAR_MAX * value / top)
        out.append(
            f'<text x="{LABEL_COL - 12}" y="{y + BAR_H / 2 + 5}" font-size="13.5" text-anchor="end" '
            f'fill="{t["secondary"]}">{esc(label)}</text>'
        )
        out.append(
            f'<path d="{bar_path(LABEL_COL, y, w, BAR_H)}" fill="{t[color_key]}"/>'
        )
        out.append(
            f'<text x="{LABEL_COL + w + 8}" y="{y + BAR_H / 2 + 5}" font-size="13.5" font-weight="600" '
            f'fill="{t["primary"]}">{esc(fmt(value))}</text>'
        )
    out.append("</svg>")
    return "\n".join(out)


def tiles(theme):
    t = THEMES[theme]
    gap, h = 12, 92
    w = (WIDTH - gap * (len(TILES) - 1)) / len(TILES)
    out = [
        (
            f'<svg xmlns="http://www.w3.org/2000/svg" width="{WIDTH}" height="{h}" viewBox="0 0 {WIDTH} {h}" '
            f'font-family="{FONT}" role="img" aria-label="What agents leave behind">'
        ),
        "<title>What coding agents leave behind</title>",
    ]
    for i, (value, label) in enumerate(TILES):
        x = i * (w + gap)
        out.append(
            f'<rect x="{x}" y="0" width="{w}" height="{h}" rx="10" fill="{t["tile"]}"/>'
        )
        out.append(
            f'<text x="{x + 18}" y="44" font-size="28" font-weight="650" fill="{t["primary"]}">{esc(value)}</text>'
        )
        out.append(
            f'<text x="{x + 18}" y="72" font-size="13" fill="{t["secondary"]}">{esc(label)}</text>'
        )
    out.append("</svg>")
    return "\n".join(out)


def main():
    OUT.mkdir(parents=True, exist_ok=True)
    for theme in THEMES:
        (OUT / f"summary-{theme}.svg").write_text(tiles(theme))
        (OUT / f"freed-{theme}.svg").write_text(
            bar_chart(
                theme,
                "What can be freed: 30.5 GB",
                "One tidewake scan on a developer Mac, Oct 2026. Each row comes with a command to review.",
                FREED_GB,
                "blue",
                lambda v: f"{v:.1f} GB",
            )
        )
        (OUT / f"kept-{theme}.svg").write_text(
            bar_chart(
                theme,
                "What it refused to remove: 24 worktrees, 5.8 GB",
                "Same scan. 16 of the 24 looked clean to git status; removing them would still have lost work.",
                KEPT_WORKTREES,
                "orange",
                lambda v: f"{int(v)}",
            )
        )
    print("wrote", sorted(p.name for p in OUT.glob("*.svg")))


if __name__ == "__main__":
    main()

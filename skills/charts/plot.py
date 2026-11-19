"""Draws a chart widget, the JSON that den send --widget takes, as a PNG in
den's style, so Telegram shows the picture and the app the same data.

    uv run --with matplotlib python plot.py chart.json chart.png
"""

import json
import sys
from pathlib import Path

import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt

STYLE = Path(__file__).with_name("den.mplstyle")
MUTED = "#3a3f4b"


def short(value, unit=""):
    """A value as the chart shows it: 1.2k for 1200, 3.5 for 3.5."""
    size = abs(value)
    for limit, suffix in ((1e9, "B"), (1e6, "M"), (1e3, "k")):
        if size >= limit:
            text = f"{value / limit:.1f}".rstrip("0").rstrip(".") + suffix
            break
    else:
        text = f"{value:.2f}".rstrip("0").rstrip(".") if value % 1 else f"{value:.0f}"
    return text + unit


def gaps(values):
    """The values with NaN for null, which matplotlib leaves as a gap."""
    return [float("nan") if v is None else v for v in values]


def alone(values):
    """The indexes of the values with no number on either side."""
    def missing(i):
        return i < 0 or i >= len(values) or values[i] is None

    return [i for i, v in enumerate(values) if v is not None and missing(i - 1) and missing(i + 1)]


def last(values):
    """The index and the value of the last number in values."""
    return next((i, v) for i, v in reversed(list(enumerate(values))) if v is not None)


def colors(series):
    cycle = plt.rcParams["axes.prop_cycle"].by_key()["color"]
    return [cycle[i % len(cycle)] for i in range(len(series))]


def line(ax, chart):
    labels, series = chart["labels"], chart["series"]
    x = range(len(labels))
    for s, color in zip(series, colors(series)):
        ax.plot(x, gaps(s["values"]), color=color)
        # A value between two gaps has no line to be on, so it is a dot.
        lone = alone(s["values"])
        ax.plot(lone, [s["values"][i] for i in lone], "o", color=color, markersize=4)
        # Name each line at its right end, with its last value.
        at, value = last(s["values"])
        name = f'{s.get("name", "")}  {short(value, chart.get("unit", ""))}'.strip()
        ax.annotate(
            name,
            (at, value),
            xytext=(8, 0),
            textcoords="offset points",
            va="center",
            color=color,
            fontsize=13,
        )
    ax.set_xticks(list(x), labels)
    # Every label fits on a phone up to about eight; then every n-th.
    step = max(1, len(labels) // 8)
    for i, tick in enumerate(ax.get_xticklabels()):
        tick.set_visible(i % step == 0 or i == len(labels) - 1)
    ax.margins(x=0.02)


def bars(ax, chart, horizontal):
    labels, series = chart["labels"], chart["series"]
    width = 0.8 / len(series)
    unit = chart.get("unit", "")
    palette = colors(series)
    for i, s in enumerate(series):
        offset = (i - (len(series) - 1) / 2) * width
        at = [p + offset for p in range(len(labels))]
        color = palette[i] if len(series) > 1 else palette[0]
        # A label with no data has no bar and no value.
        sizes = [0 if v is None else v for v in s["values"]]
        drawn = (ax.barh if horizontal else ax.bar)(at, sizes, width * 0.9, color=color, label=s.get("name"))
        texts = ["" if v is None else short(v, unit) for v in s["values"]]
        ax.bar_label(drawn, texts, padding=4, fontsize=12, color="#e6e6e6")
    # Each bar has its value, so the value axis and its grid would only
    # repeat them.
    ax.grid(False)
    if horizontal:
        ax.set_yticks(range(len(labels)), labels)
        ax.invert_yaxis()
        ax.xaxis.set_visible(False)
        ax.spines["bottom"].set_visible(False)
    else:
        ax.set_xticks(range(len(labels)), labels)
        ax.yaxis.set_visible(False)
    if len(series) > 1:
        ax.legend()


def main(spec, out):
    chart = json.loads(Path(spec).read_text())
    plt.style.use(STYLE)

    # A line or a few bars take less height than many bars.
    fig, ax = plt.subplots(figsize=(6, 4.5) if chart["kind"] == "line" or len(chart["labels"]) <= 8 else (6, 6))
    if chart["kind"] == "line":
        line(ax, chart)
        ax.yaxis.set_major_formatter(lambda v, _: short(v))
    else:
        bars(ax, chart, horizontal=chart["kind"] == "hbar")
    if chart.get("title"):
        ax.set_title(chart["title"])
    fig.savefig(out)


if __name__ == "__main__":
    if len(sys.argv) != 3:
        sys.exit("usage: plot.py chart.json chart.png")
    main(sys.argv[1], sys.argv[2])

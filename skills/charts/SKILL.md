---
name: charts
description: Draw a chart the user reads in Telegram on a phone - dark, minimal, one point per chart. Use whenever you plot data to send, together with the python skill.
---

# charts

The user reads charts on a phone, where Telegram fits a photo to the screen's width. den's style makes matplotlib draw them dark, square, and large enough to read there. Run Python as the `python` skill says.

```python
import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt

plt.style.use("/usr/share/den/skills/charts/den.mplstyle")

fig, ax = plt.subplots()
ax.plot(hours, speed)
ax.set_title("deepseek writes faster after 18:00")
fig.savefig("/tmp/chart.png")
```

```bash
uv run --with matplotlib python chart.py
den send --caption "Writing speed by hour, last 7 days" /tmp/chart.png
```

The style sets the size (6 × 6 inches at 200 dpi, 1200 px wide), the colors, the fonts, and the grid. Leave them be: a chart that looks like the others reads faster.

## Make it minimal

- One point per chart. Two points make two charts, sent together.
- The title says what the chart shows, in the user's language: "Expenses fell in March", not "Expenses by month".
- At most four or five series. Name lines at their right ends with `ax.annotate` in the line's color rather than in a legend; when two ends are close, move one label up or down a little with `xytext`, and use a legend only when many ends crowd together.
- Keep labels short: round numbers, `1.2k` for 1200, `14:00` or `Mar` on dates, and no axis label the title already says.
- The first color is the main one. When one series matters, draw it in the first color and the rest in `#3a3f4b`.
- Bars sort by value unless their order means something, such as time. With long category names, use horizontal bars (`ax.barh`) so the names stay readable.
- Put a few key numbers on the chart itself, such as the last value or the maximum, and leave the rest to the axis.
- Keep it square. A long time series can be 6 × 4.5 (`plt.subplots(figsize=(6, 4.5))`); never wider.

## Local findings

Findings about this skill on this machine are in `notes/skills/charts.md`. Read it before you start, and add what you learn there. This skill itself is read-only.

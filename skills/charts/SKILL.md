---
name: charts
description: Draw a chart the user reads in Telegram and the den app - dark, minimal, one point per chart. Use whenever you plot data to send, together with the python skill.
---

# charts

The user reads charts in Telegram on a phone, where a photo fits the screen's width, and in the den app, which draws a chart itself from its data. So describe a chart as data, draw the picture from it, and send both: Telegram shows the picture, the app the data. Run Python as the `python` skill says.

## Line and bar charts

Write the chart as JSON:

```json
{
  "type": "chart",
  "kind": "line",
  "title": "deepseek writes faster after 18:00",
  "unit": " t/s",
  "labels": ["00", "03", "06", "09", "12", "15", "18", "21"],
  "series": [
    {"name": "deepseek", "values": [41, 43, 38, 30, 28, 31, 52, 55]},
    {"name": "glm", "values": [35, 36, 33, 29, 27, 29, 33, 34]}
  ]
}
```

`kind` is `line`, `bar`, or `hbar` (horizontal bars, for long category names). `unit` is optional and goes after each value. Every series has a value for each label: at most 6 series and 60 labels.

```bash
uv run --with matplotlib python /usr/share/den/skills/charts/plot.py /tmp/chart.json /tmp/chart.png
den send --caption "Writing speed by hour, last 7 days" --widget /tmp/chart.json /tmp/chart.png
```

`plot.py` draws in den's style: lines named at their right ends with the last value, bars with their values on them.

## Other charts

When a chart does not fit those kinds, such as a scatter plot, draw it with matplotlib and den's style, and send it without `--widget`:

```python
import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt

plt.style.use("/usr/share/den/skills/charts/den.mplstyle")

fig, ax = plt.subplots()
ax.scatter(price, rating)
ax.set_title("Pricier cafés are not rated higher")
fig.savefig("/tmp/chart.png")
```

The style sets the size (6 × 6 inches at 200 dpi, 1200 px wide), the colors, the fonts, and the grid. Leave them be: a chart that looks like the others reads faster. Keep it square, or 6 × 4.5 for a long series; never wider.

## Make it minimal

- One point per chart. Two points make two charts, sent together.
- The title says what the chart shows, in the user's language: "Expenses fell in March", not "Expenses by month".
- At most four or five series. The first is the main one.
- At most 24 points per series. Group dense data, such as hourly values over days, into 6-hour or daily medians first: the trend is what the user reads, not every sample.
- Keep labels short: round numbers, `14:00` or `Mar` on dates.
- Bars sort by value unless their order means something, such as time.

## Local findings

Findings about this skill on this machine are in `notes/skills/charts.md`. Read it before you start, and add what you learn there. This skill itself is read-only.

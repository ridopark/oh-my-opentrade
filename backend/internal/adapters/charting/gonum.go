package charting

import (
	"bytes"
	"context"
	"fmt"
	"image/color"
	"time"

	_ "codeberg.org/go-fonts/liberation/liberationsansregular"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/pplcc/plotext/custplotter"
	"gonum.org/v1/plot"
	"gonum.org/v1/plot/plotter"
	"gonum.org/v1/plot/vg"
	"gonum.org/v1/plot/vg/draw"
	"gonum.org/v1/plot/vg/vgimg"
)

// etTimeTicks is a custom plot.Ticker that places ticks at regular hourly
// intervals and formats them in America/New_York time.
type etTimeTicks struct {
	loc      *time.Location
	interval time.Duration
	format   string
}

func newETTimeTicks(format string) etTimeTicks {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		// Fallback to UTC if tz data unavailable (shouldn't happen in production).
		loc = time.UTC
	}
	return etTimeTicks{loc: loc, interval: 30 * time.Minute, format: format}
}

func (t etTimeTicks) Ticks(min, max float64) []plot.Tick {
	start := time.Unix(int64(min), 0).In(t.loc)
	end := time.Unix(int64(max), 0).In(t.loc)

	intervalSec := t.interval.Seconds()
	alignedStart := time.Unix(int64(start.Unix()/int64(intervalSec)+1)*int64(intervalSec), 0).In(t.loc)

	var ticks []plot.Tick
	for ts := alignedStart; !ts.After(end); ts = ts.Add(t.interval) {
		ticks = append(ticks, plot.Tick{
			Value: float64(ts.Unix()),
			Label: ts.Format(t.format),
		})
	}
	return ticks
}

var _ ports.ChartGeneratorPort = (*GonumChartGenerator)(nil)

type GonumChartGenerator struct{}

func NewGonumChartGenerator() *GonumChartGenerator {
	return &GonumChartGenerator{}
}

type barData []domain.MarketBar

func (d barData) Len() int { return len(d) }

func (d barData) TOHLCV(i int) (float64, float64, float64, float64, float64, float64) {
	b := d[i]
	return float64(b.Time.Unix()), b.Open, b.High, b.Low, b.Close, b.Volume
}

var levelColors = map[string]color.RGBA{
	"green":  {R: 76, G: 175, B: 80, A: 220},
	"red":    {R: 239, G: 83, B: 80, A: 220},
	"blue":   {R: 52, G: 152, B: 219, A: 220},
	"silver": {R: 180, G: 180, B: 180, A: 220},
}

func (g *GonumChartGenerator) GenerateCandlestickChart(_ context.Context, bars []domain.MarketBar, title string, opts ports.ChartOptions) ([]byte, error) {
	if len(bars) < 2 {
		return nil, fmt.Errorf("need at least 2 bars, got %d", len(bars))
	}

	bgColor := color.RGBA{R: 30, G: 30, B: 35, A: 255}
	textColor := color.RGBA{R: 200, G: 200, B: 200, A: 255}
	greenUp := color.RGBA{R: 38, G: 166, B: 154, A: 255}
	redDown := color.RGBA{R: 239, G: 83, B: 80, A: 255}

	candles, err := custplotter.NewCandlesticks(barData(bars))
	if err != nil {
		return nil, fmt.Errorf("creating candlesticks: %w", err)
	}
	candles.ColorUp = greenUp
	candles.ColorDown = redDown
	candles.CandleWidth = vg.Points(4)

	chartTitle := title
	if opts.PnL != nil {
		pnl := opts.PnL
		sign := "+"
		if pnl.PnLPct < 0 {
			sign = ""
		}
		chartTitle = fmt.Sprintf("%s  ·  %s%.1f%%  $%.0f  %s", title, sign, pnl.PnLPct, pnl.PnLUSD, pnl.HoldDuration)
	}

	p := plot.New()
	p.Title.Text = chartTitle
	p.BackgroundColor = bgColor

	if opts.PnL != nil {
		if opts.PnL.PnLPct >= 0 {
			p.Title.TextStyle.Color = color.RGBA{R: 38, G: 166, B: 154, A: 255}
		} else {
			p.Title.TextStyle.Color = color.RGBA{R: 239, G: 83, B: 80, A: 255}
		}
	} else {
		p.Title.TextStyle.Color = textColor
	}

	p.X.Color = textColor
	p.X.Label.TextStyle.Color = textColor
	p.X.Tick.Label.Color = textColor
	p.X.Tick.Color = textColor
	p.X.Tick.Marker = newETTimeTicks("15:04")

	p.Y.Color = textColor
	p.Y.Label.TextStyle.Color = textColor
	p.Y.Tick.Label.Color = textColor
	p.Y.Tick.Color = textColor
	p.Y.Label.Text = "$"

	p.Add(candles)

	xMin := float64(bars[0].Time.Unix())
	xMax := float64(bars[len(bars)-1].Time.Unix())

	yMin, yMax := bars[0].Low, bars[0].High
	for _, b := range bars[1:] {
		if b.Low < yMin {
			yMin = b.Low
		}
		if b.High > yMax {
			yMax = b.High
		}
	}
	for _, lvl := range opts.Levels {
		if lvl.Price <= 0 {
			continue
		}
		if lvl.Price < yMin {
			yMin = lvl.Price
		}
		if lvl.Price > yMax {
			yMax = lvl.Price
		}
	}
	yPad := (yMax - yMin) * 0.03
	p.Y.Min = yMin - yPad
	p.Y.Max = yMax + yPad

	for _, lvl := range opts.Levels {
		if lvl.Price <= 0 {
			continue
		}

		lineXMin := xMin
		lineXMax := xMax
		if !lvl.StartTime.IsZero() {
			clamped := float64(lvl.StartTime.Unix())
			if clamped > lineXMin {
				lineXMin = clamped
			}
		}
		if !lvl.EndTime.IsZero() {
			clamped := float64(lvl.EndTime.Unix())
			if clamped < lineXMax {
				lineXMax = clamped
			}
		}
		if lineXMin > lineXMax {
			lineXMin = lineXMax
		}

		pts := make(plotter.XYs, 2)
		pts[0] = plotter.XY{X: lineXMin, Y: lvl.Price}
		pts[1] = plotter.XY{X: lineXMax, Y: lvl.Price}

		line, err := plotter.NewLine(pts)
		if err != nil {
			continue
		}

		c := levelColors["blue"]
		if mapped, ok := levelColors[lvl.Color]; ok {
			c = mapped
		}
		line.Color = c
		line.Width = vg.Points(1.5)
		line.Dashes = []vg.Length{vg.Points(6), vg.Points(3)}

		p.Add(line)
		p.Legend.Add(lvl.Label, line)
	}

	for _, mk := range opts.Markers {
		if mk.Time.IsZero() {
			continue
		}
		x := float64(mk.Time.Unix())
		if x < xMin {
			x = xMin
		}
		if x > xMax {
			x = xMax
		}

		pts := make(plotter.XYs, 2)
		pts[0] = plotter.XY{X: x, Y: p.Y.Min}
		pts[1] = plotter.XY{X: x, Y: p.Y.Max}

		vline, err := plotter.NewLine(pts)
		if err != nil {
			continue
		}

		c := levelColors["green"]
		if mapped, ok := levelColors[mk.Color]; ok {
			c = mapped
		}
		c.A = 100
		vline.Color = c
		vline.Width = vg.Points(1)
		vline.Dashes = []vg.Length{vg.Points(4), vg.Points(4)}

		p.Add(vline)
	}

	if len(opts.Levels) > 0 {
		p.Legend.TextStyle.Color = textColor
		p.Legend.Top = true
		p.Legend.Left = true
	}

	canvas := vgimg.New(vg.Points(800), vg.Points(400))
	dc := draw.New(canvas)
	p.Draw(dc)

	jpg := vgimg.JpegCanvas{Canvas: canvas}
	var buf bytes.Buffer
	if _, err := jpg.WriteTo(&buf); err != nil {
		return nil, fmt.Errorf("encoding jpeg: %w", err)
	}
	return buf.Bytes(), nil
}

package charting

import (
	"bytes"
	"context"
	"fmt"
	"image/color"

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
	"green": {R: 38, G: 166, B: 154, A: 200},
	"red":   {R: 239, G: 83, B: 80, A: 200},
	"blue":  {R: 52, G: 152, B: 219, A: 200},
}

func (g *GonumChartGenerator) GenerateCandlestickChart(_ context.Context, bars []domain.MarketBar, title string, levels []domain.PriceLevel) ([]byte, error) {
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

	p := plot.New()
	p.Title.Text = title
	p.Title.TextStyle.Color = textColor
	p.BackgroundColor = bgColor

	p.X.Color = textColor
	p.X.Label.TextStyle.Color = textColor
	p.X.Tick.Label.Color = textColor
	p.X.Tick.Color = textColor
	p.X.Tick.Marker = plot.TimeTicks{Format: "15:04"}

	p.Y.Color = textColor
	p.Y.Label.TextStyle.Color = textColor
	p.Y.Tick.Label.Color = textColor
	p.Y.Tick.Color = textColor
	p.Y.Label.Text = "$"

	p.Add(candles)

	xMin := float64(bars[0].Time.Unix())
	xMax := float64(bars[len(bars)-1].Time.Unix())

	for _, lvl := range levels {
		if lvl.Price <= 0 {
			continue
		}

		pts := make(plotter.XYs, 2)
		pts[0] = plotter.XY{X: xMin, Y: lvl.Price}
		pts[1] = plotter.XY{X: xMax, Y: lvl.Price}

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

	if len(levels) > 0 {
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

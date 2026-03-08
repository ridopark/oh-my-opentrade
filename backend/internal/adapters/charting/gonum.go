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
	"gonum.org/v1/plot/vg"
	"gonum.org/v1/plot/vg/draw"
	"gonum.org/v1/plot/vg/vgimg"
)

var _ ports.ChartGeneratorPort = (*GonumChartGenerator)(nil)

// GonumChartGenerator renders candlestick chart PNGs using gonum/plot and plotext.
type GonumChartGenerator struct{}

// NewGonumChartGenerator returns a ready-to-use chart generator.
func NewGonumChartGenerator() *GonumChartGenerator {
	return &GonumChartGenerator{}
}

type barData []domain.MarketBar

func (d barData) Len() int { return len(d) }

func (d barData) TOHLCV(i int) (float64, float64, float64, float64, float64, float64) {
	b := d[i]
	return float64(b.Time.Unix()), b.Open, b.High, b.Low, b.Close, b.Volume
}

func (g *GonumChartGenerator) GenerateCandlestickChart(_ context.Context, bars []domain.MarketBar, title string) ([]byte, error) {
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

	p.X.LineStyle.Color = textColor
	p.X.Label.TextStyle.Color = textColor
	p.X.Tick.Label.Color = textColor
	p.X.Tick.LineStyle.Color = textColor
	p.X.Tick.Marker = plot.TimeTicks{Format: "15:04"}

	p.Y.LineStyle.Color = textColor
	p.Y.Label.TextStyle.Color = textColor
	p.Y.Tick.Label.Color = textColor
	p.Y.Tick.LineStyle.Color = textColor
	p.Y.Label.Text = "$"

	p.Add(candles)

	canvas := vgimg.New(vg.Points(1200), vg.Points(600))
	dc := draw.New(canvas)
	p.Draw(dc)

	png := vgimg.PngCanvas{Canvas: canvas}
	var buf bytes.Buffer
	if _, err := png.WriteTo(&buf); err != nil {
		return nil, fmt.Errorf("encoding png: %w", err)
	}
	return buf.Bytes(), nil
}

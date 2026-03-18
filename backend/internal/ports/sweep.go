package ports

import "context"

type SweepEvent struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

type SweepPort interface {
	Start(ctx context.Context, configJSON []byte) (sweepID string, err error)
	Events(ctx context.Context, sweepID string) (<-chan SweepEvent, error)
	Cancel(sweepID string) error
	GetResultJSON(sweepID string) ([]byte, error)
	ApplyBest(ctx context.Context, sweepID string, runIndex int) error
}

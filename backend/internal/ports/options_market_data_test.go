package ports_test

import (
	"context"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

// mockOptionsMarketData is a compile-check mock implementing OptionsMarketDataPort.
type mockOptionsMarketData struct{}

func (m *mockOptionsMarketData) GetOptionChain(
	ctx context.Context,
	underlying domain.Symbol,
	expiry time.Time,
	right domain.OptionRight,
) ([]domain.OptionContractSnapshot, error) {
	return nil, nil
}

// Compile-time assertion that mockOptionsMarketData satisfies the interface.
var _ ports.OptionsMarketDataPort = (*mockOptionsMarketData)(nil)

func TestOptionsMarketDataPort_InterfaceCompiles(t *testing.T) {
	var p ports.OptionsMarketDataPort = &mockOptionsMarketData{}
	if p == nil {
		t.Fatal("interface should not be nil")
	}
}

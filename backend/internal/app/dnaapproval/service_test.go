package dnaapproval_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/dnaapproval"
	"github.com/oh-my-opentrade/backend/internal/domain"
	dnadomain "github.com/oh-my-opentrade/backend/internal/domain/dnaapproval"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type mockRepo struct {
	mu sync.Mutex

	saveVersionFunc      func(ctx context.Context, v dnadomain.DNAVersion) error
	getVersionFunc       func(ctx context.Context, id string) (*dnadomain.DNAVersion, error)
	getVersionByHashFunc func(ctx context.Context, strategyKey, contentHash string) (*dnadomain.DNAVersion, error)
	saveApprovalFunc     func(ctx context.Context, a dnadomain.DNAApproval) error
	updateApprovalFunc   func(ctx context.Context, id string, status dnadomain.DNAStatus, decidedBy string, comment string) error
	getApprovalFunc      func(ctx context.Context, id string) (*dnadomain.DNAApproval, error)
	listPendingFunc      func(ctx context.Context) ([]dnadomain.DNAApproval, error)
	getActiveVersionFunc func(ctx context.Context, strategyKey string) (*dnadomain.DNAVersion, error)
}

func (m *mockRepo) SaveDNAVersion(ctx context.Context, v dnadomain.DNAVersion) error {
	return m.saveVersionFunc(ctx, v)
}
func (m *mockRepo) GetDNAVersion(ctx context.Context, id string) (*dnadomain.DNAVersion, error) {
	return m.getVersionFunc(ctx, id)
}
func (m *mockRepo) GetDNAVersionByHash(ctx context.Context, strategyKey, contentHash string) (*dnadomain.DNAVersion, error) {
	return m.getVersionByHashFunc(ctx, strategyKey, contentHash)
}
func (m *mockRepo) SaveDNAApproval(ctx context.Context, a dnadomain.DNAApproval) error {
	return m.saveApprovalFunc(ctx, a)
}
func (m *mockRepo) UpdateDNAApproval(ctx context.Context, id string, status dnadomain.DNAStatus, decidedBy string, comment string) error {
	return m.updateApprovalFunc(ctx, id, status, decidedBy, comment)
}
func (m *mockRepo) GetDNAApproval(ctx context.Context, id string) (*dnadomain.DNAApproval, error) {
	return m.getApprovalFunc(ctx, id)
}
func (m *mockRepo) ListPendingApprovals(ctx context.Context) ([]dnadomain.DNAApproval, error) {
	return m.listPendingFunc(ctx)
}
func (m *mockRepo) GetActiveDNAVersion(ctx context.Context, strategyKey string) (*dnadomain.DNAVersion, error) {
	return m.getActiveVersionFunc(ctx, strategyKey)
}

type fakeBus struct {
	mu        sync.Mutex
	handlers  map[domain.EventType][]ports.EventHandler
	published []domain.Event
}

func newFakeBus() *fakeBus {
	return &fakeBus{handlers: make(map[domain.EventType][]ports.EventHandler)}
}

func (b *fakeBus) Publish(ctx context.Context, ev domain.Event) error {
	b.mu.Lock()
	b.published = append(b.published, ev)
	h := append([]ports.EventHandler(nil), b.handlers[ev.Type]...)
	b.mu.Unlock()
	for _, fn := range h {
		_ = fn(ctx, ev)
	}
	return nil
}

func (b *fakeBus) Subscribe(_ context.Context, t domain.EventType, h ports.EventHandler) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers[t] = append(b.handlers[t], h)
	return nil
}

func (b *fakeBus) Unsubscribe(_ context.Context, _ domain.EventType, _ ports.EventHandler) error {
	return nil
}

func (b *fakeBus) count(t domain.EventType) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, ev := range b.published {
		if ev.Type == t {
			n++
		}
	}
	return n
}

func TestService_Start_Subscribes(t *testing.T) {
	repo := &mockRepo{}
	bus := newFakeBus()
	svc := dnaapproval.NewService(repo, bus, zerolog.Nop())
	require.NoError(t, svc.Start(context.Background()))
	bus.mu.Lock()
	_, ok := bus.handlers[domain.EventDNAVersionDetected]
	bus.mu.Unlock()
	require.True(t, ok)
}

func TestService_HandleDNAVersionDetected_CreatesVersionAndPendingApproval(t *testing.T) {
	calledSaveV := false
	calledSaveA := false

	repo := &mockRepo{
		getVersionByHashFunc: func(ctx context.Context, strategyKey, contentHash string) (*dnadomain.DNAVersion, error) {
			return nil, nil
		},
		saveVersionFunc: func(ctx context.Context, v dnadomain.DNAVersion) error {
			calledSaveV = true
			return nil
		},
		saveApprovalFunc: func(ctx context.Context, a dnadomain.DNAApproval) error {
			calledSaveA = true
			require.Equal(t, dnadomain.DNAStatusPending, a.Status)
			return nil
		},
		getVersionFunc: func(ctx context.Context, id string) (*dnadomain.DNAVersion, error) { return nil, nil },
		updateApprovalFunc: func(ctx context.Context, id string, status dnadomain.DNAStatus, decidedBy string, comment string) error {
			return nil
		},
		getApprovalFunc:      func(ctx context.Context, id string) (*dnadomain.DNAApproval, error) { return nil, nil },
		listPendingFunc:      func(ctx context.Context) ([]dnadomain.DNAApproval, error) { return nil, nil },
		getActiveVersionFunc: func(ctx context.Context, strategyKey string) (*dnadomain.DNAVersion, error) { return nil, nil },
	}

	bus := newFakeBus()
	svc := dnaapproval.NewService(repo, bus, zerolog.Nop())

	ev, err := domain.NewEvent(domain.EventDNAVersionDetected, "default", domain.EnvModePaper, "k1", dnaapproval.VersionDetectedPayload{
		StrategyKey: "orb",
		ContentTOML: "[strategy]\nid='orb'\n",
		ContentHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		DetectedAt:  time.Now().UTC(),
	})
	require.NoError(t, err)
	require.NoError(t, svc.HandleDNAVersionDetected(context.Background(), *ev))
	require.True(t, calledSaveV)
	require.True(t, calledSaveA)
	require.Equal(t, 1, bus.count(domain.EventDNAApprovalRequested))
}

func TestService_HandleDNAVersionDetected_SkipsExistingHash(t *testing.T) {
	existing := &dnadomain.DNAVersion{ID: "v1"}
	repo := &mockRepo{
		getVersionByHashFunc: func(ctx context.Context, strategyKey, contentHash string) (*dnadomain.DNAVersion, error) {
			return existing, nil
		},
		saveVersionFunc:  func(ctx context.Context, v dnadomain.DNAVersion) error { return errors.New("should not save") },
		saveApprovalFunc: func(ctx context.Context, a dnadomain.DNAApproval) error { return errors.New("should not save") },
		getVersionFunc:   func(ctx context.Context, id string) (*dnadomain.DNAVersion, error) { return nil, nil },
		updateApprovalFunc: func(ctx context.Context, id string, status dnadomain.DNAStatus, decidedBy string, comment string) error {
			return nil
		},
		getApprovalFunc:      func(ctx context.Context, id string) (*dnadomain.DNAApproval, error) { return nil, nil },
		listPendingFunc:      func(ctx context.Context) ([]dnadomain.DNAApproval, error) { return nil, nil },
		getActiveVersionFunc: func(ctx context.Context, strategyKey string) (*dnadomain.DNAVersion, error) { return nil, nil },
	}
	bus := newFakeBus()
	svc := dnaapproval.NewService(repo, bus, zerolog.Nop())

	ev, _ := domain.NewEvent(domain.EventDNAVersionDetected, "default", domain.EnvModePaper, "k1", dnaapproval.VersionDetectedPayload{StrategyKey: "orb", ContentTOML: "x", ContentHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", DetectedAt: time.Now()})
	require.NoError(t, svc.HandleDNAVersionDetected(context.Background(), *ev))
	require.Equal(t, 0, bus.count(domain.EventDNAApprovalRequested))
}

func TestService_Approve_PublishesEvents(t *testing.T) {
	a := &dnadomain.DNAApproval{ID: "a1", VersionID: "v1", Status: dnadomain.DNAStatusPending}
	v := &dnadomain.DNAVersion{ID: "v1", StrategyKey: "orb", ContentHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}

	repo := &mockRepo{
		getApprovalFunc: func(ctx context.Context, id string) (*dnadomain.DNAApproval, error) { return a, nil },
		updateApprovalFunc: func(ctx context.Context, id string, status dnadomain.DNAStatus, decidedBy string, comment string) error {
			require.Equal(t, dnadomain.DNAStatusApproved, status)
			require.Equal(t, "bob", decidedBy)
			return nil
		},
		getVersionFunc: func(ctx context.Context, id string) (*dnadomain.DNAVersion, error) { return v, nil },
		getVersionByHashFunc: func(ctx context.Context, strategyKey, contentHash string) (*dnadomain.DNAVersion, error) {
			return nil, nil
		},
		saveVersionFunc:      func(ctx context.Context, v dnadomain.DNAVersion) error { return nil },
		saveApprovalFunc:     func(ctx context.Context, a dnadomain.DNAApproval) error { return nil },
		listPendingFunc:      func(ctx context.Context) ([]dnadomain.DNAApproval, error) { return nil, nil },
		getActiveVersionFunc: func(ctx context.Context, strategyKey string) (*dnadomain.DNAVersion, error) { return nil, nil },
	}
	bus := newFakeBus()
	svc := dnaapproval.NewService(repo, bus, zerolog.Nop())

	require.NoError(t, svc.Approve(context.Background(), "a1", "bob", "ok"))
	require.Equal(t, 1, bus.count(domain.EventDNAApproved))
	require.Equal(t, 1, bus.count(domain.EventActiveDNAChanged))
}

func TestService_Reject_PublishesEvent(t *testing.T) {
	a := &dnadomain.DNAApproval{ID: "a1", VersionID: "v1", Status: dnadomain.DNAStatusPending}
	repo := &mockRepo{
		getApprovalFunc: func(ctx context.Context, id string) (*dnadomain.DNAApproval, error) { return a, nil },
		updateApprovalFunc: func(ctx context.Context, id string, status dnadomain.DNAStatus, decidedBy string, comment string) error {
			require.Equal(t, dnadomain.DNAStatusRejected, status)
			return nil
		},
		getVersionFunc: func(ctx context.Context, id string) (*dnadomain.DNAVersion, error) {
			return &dnadomain.DNAVersion{ID: "v1"}, nil
		},
		getVersionByHashFunc: func(ctx context.Context, strategyKey, contentHash string) (*dnadomain.DNAVersion, error) {
			return nil, nil
		},
		saveVersionFunc:      func(ctx context.Context, v dnadomain.DNAVersion) error { return nil },
		saveApprovalFunc:     func(ctx context.Context, a dnadomain.DNAApproval) error { return nil },
		listPendingFunc:      func(ctx context.Context) ([]dnadomain.DNAApproval, error) { return nil, nil },
		getActiveVersionFunc: func(ctx context.Context, strategyKey string) (*dnadomain.DNAVersion, error) { return nil, nil },
	}
	bus := newFakeBus()
	svc := dnaapproval.NewService(repo, bus, zerolog.Nop())

	require.NoError(t, svc.Reject(context.Background(), "a1", "bob", "no"))
	require.Equal(t, 1, bus.count(domain.EventDNARejected))
}

func TestService_GetPendingApprovals_JoinsVersions(t *testing.T) {
	repo := &mockRepo{
		listPendingFunc: func(ctx context.Context) ([]dnadomain.DNAApproval, error) {
			return []dnadomain.DNAApproval{{ID: "a1", VersionID: "v1", Status: dnadomain.DNAStatusPending}}, nil
		},
		getVersionFunc: func(ctx context.Context, id string) (*dnadomain.DNAVersion, error) {
			return &dnadomain.DNAVersion{ID: "v1", StrategyKey: "orb"}, nil
		},
		getApprovalFunc: func(ctx context.Context, id string) (*dnadomain.DNAApproval, error) { return nil, nil },
		updateApprovalFunc: func(ctx context.Context, id string, status dnadomain.DNAStatus, decidedBy string, comment string) error {
			return nil
		},
		getVersionByHashFunc: func(ctx context.Context, strategyKey, contentHash string) (*dnadomain.DNAVersion, error) {
			return nil, nil
		},
		saveVersionFunc:      func(ctx context.Context, v dnadomain.DNAVersion) error { return nil },
		saveApprovalFunc:     func(ctx context.Context, a dnadomain.DNAApproval) error { return nil },
		getActiveVersionFunc: func(ctx context.Context, strategyKey string) (*dnadomain.DNAVersion, error) { return nil, nil },
	}
	svc := dnaapproval.NewService(repo, newFakeBus(), zerolog.Nop())

	items, err := svc.GetPendingApprovals(context.Background())
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, "orb", items[0].Version.StrategyKey)
}

func TestService_GetApproval_ReturnsNilWhenMissing(t *testing.T) {
	repo := &mockRepo{
		getApprovalFunc: func(ctx context.Context, id string) (*dnadomain.DNAApproval, error) { return nil, nil },
		getVersionFunc:  func(ctx context.Context, id string) (*dnadomain.DNAVersion, error) { return nil, nil },
		listPendingFunc: func(ctx context.Context) ([]dnadomain.DNAApproval, error) { return nil, nil },
		updateApprovalFunc: func(ctx context.Context, id string, status dnadomain.DNAStatus, decidedBy string, comment string) error {
			return nil
		},
		getVersionByHashFunc: func(ctx context.Context, strategyKey, contentHash string) (*dnadomain.DNAVersion, error) {
			return nil, nil
		},
		saveVersionFunc:      func(ctx context.Context, v dnadomain.DNAVersion) error { return nil },
		saveApprovalFunc:     func(ctx context.Context, a dnadomain.DNAApproval) error { return nil },
		getActiveVersionFunc: func(ctx context.Context, strategyKey string) (*dnadomain.DNAVersion, error) { return nil, nil },
	}
	svc := dnaapproval.NewService(repo, newFakeBus(), zerolog.Nop())
	got, err := svc.GetApproval(context.Background(), "a1")
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestService_IsVersionApproved_UsesActiveVersion(t *testing.T) {
	repo := &mockRepo{
		getActiveVersionFunc: func(ctx context.Context, strategyKey string) (*dnadomain.DNAVersion, error) {
			return &dnadomain.DNAVersion{ID: "v1", StrategyKey: strategyKey, ContentHash: "h1"}, nil
		},
		getApprovalFunc: func(ctx context.Context, id string) (*dnadomain.DNAApproval, error) { return nil, nil },
		getVersionFunc:  func(ctx context.Context, id string) (*dnadomain.DNAVersion, error) { return nil, nil },
		listPendingFunc: func(ctx context.Context) ([]dnadomain.DNAApproval, error) { return nil, nil },
		updateApprovalFunc: func(ctx context.Context, id string, status dnadomain.DNAStatus, decidedBy string, comment string) error {
			return nil
		},
		getVersionByHashFunc: func(ctx context.Context, strategyKey, contentHash string) (*dnadomain.DNAVersion, error) {
			return nil, nil
		},
		saveVersionFunc:  func(ctx context.Context, v dnadomain.DNAVersion) error { return nil },
		saveApprovalFunc: func(ctx context.Context, a dnadomain.DNAApproval) error { return nil },
	}
	svc := dnaapproval.NewService(repo, newFakeBus(), zerolog.Nop())
	ok, err := svc.IsVersionApproved(context.Background(), "orb", "h1")
	require.NoError(t, err)
	require.True(t, ok)
}

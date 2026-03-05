package dnaapproval

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	dnadomain "github.com/oh-my-opentrade/backend/internal/domain/dnaapproval"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

type Service struct {
	repo     ports.DNAApprovalRepoPort
	eventBus ports.EventBusPort
	log      zerolog.Logger
}

type VersionDetectedPayload struct {
	StrategyKey string    `json:"strategyKey"`
	ContentTOML string    `json:"contentToml"`
	ContentHash string    `json:"contentHash"`
	DetectedAt  time.Time `json:"detectedAt"`
}

type ApprovalWithVersion struct {
	Approval dnadomain.DNAApproval
	Version  dnadomain.DNAVersion
}

func NewService(repo ports.DNAApprovalRepoPort, eventBus ports.EventBusPort, log zerolog.Logger) *Service {
	return &Service{repo: repo, eventBus: eventBus, log: log}
}

func (s *Service) Start(ctx context.Context) error {
	if err := s.eventBus.Subscribe(ctx, domain.EventDNAVersionDetected, s.HandleDNAVersionDetected); err != nil {
		return fmt.Errorf("dnaapproval: failed to subscribe: %w", err)
	}
	s.log.Info().Msg("subscribed to DNAVersionDetected events")
	return nil
}

func (s *Service) HandleDNAVersionDetected(ctx context.Context, event domain.Event) error {
	p, ok := event.Payload.(VersionDetectedPayload)
	if !ok {
		return fmt.Errorf("dnaapproval: payload is not VersionDetectedPayload, got %T", event.Payload)
	}
	if strings.TrimSpace(p.StrategyKey) == "" || strings.TrimSpace(p.ContentTOML) == "" || strings.TrimSpace(p.ContentHash) == "" {
		return errors.New("dnaapproval: invalid payload")
	}
	if p.DetectedAt.IsZero() {
		p.DetectedAt = time.Now().UTC()
	}

	existing, err := s.repo.GetDNAVersionByHash(ctx, p.StrategyKey, p.ContentHash)
	if err != nil {
		return fmt.Errorf("dnaapproval: check version by hash: %w", err)
	}
	if existing != nil {
		return nil
	}

	v, err := dnadomain.NewDNAVersion(p.StrategyKey, p.ContentTOML, p.ContentHash, p.DetectedAt)
	if err != nil {
		return fmt.Errorf("dnaapproval: new dna version: %w", err)
	}
	if err := s.repo.SaveDNAVersion(ctx, v); err != nil {
		return fmt.Errorf("dnaapproval: save dna version: %w", err)
	}

	a, err := dnadomain.NewDNAApproval(v.ID, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("dnaapproval: new dna approval: %w", err)
	}
	if err := s.repo.SaveDNAApproval(ctx, a); err != nil {
		return fmt.Errorf("dnaapproval: save dna approval: %w", err)
	}

	reqEv, err := domain.NewEvent(domain.EventDNAApprovalRequested, event.TenantID, event.EnvMode, event.IdempotencyKey+"-dna-approval-requested", ApprovalWithVersion{Approval: a, Version: v})
	if err != nil {
		return fmt.Errorf("dnaapproval: create approval requested event: %w", err)
	}
	if err := s.eventBus.Publish(ctx, *reqEv); err != nil {
		return fmt.Errorf("dnaapproval: publish approval requested: %w", err)
	}
	return nil
}

func (s *Service) Approve(ctx context.Context, approvalID, decidedBy, comment string) error {
	approvalID = strings.TrimSpace(approvalID)
	decidedBy = strings.TrimSpace(decidedBy)
	if approvalID == "" {
		return errors.New("approval id is required")
	}
	if decidedBy == "" {
		return errors.New("decidedBy is required")
	}

	a, err := s.repo.GetDNAApproval(ctx, approvalID)
	if err != nil {
		return fmt.Errorf("dnaapproval: get approval: %w", err)
	}
	if a == nil {
		return errors.New("approval not found")
	}
	if a.Status != dnadomain.DNAStatusPending {
		return fmt.Errorf("approval is not pending: %s", a.Status)
	}

	if err := s.repo.UpdateDNAApproval(ctx, approvalID, dnadomain.DNAStatusApproved, decidedBy, comment); err != nil {
		return fmt.Errorf("dnaapproval: update approval: %w", err)
	}

	v, err := s.repo.GetDNAVersion(ctx, a.VersionID)
	if err != nil {
		return fmt.Errorf("dnaapproval: get version: %w", err)
	}
	if v == nil {
		return errors.New("version not found")
	}

	approvedEv, err := domain.NewEvent(domain.EventDNAApproved, "default", domain.EnvModePaper, approvalID+"-dna-approved", map[string]any{"approvalId": approvalID, "versionId": a.VersionID})
	if err != nil {
		return fmt.Errorf("dnaapproval: create approved event: %w", err)
	}
	if err := s.eventBus.Publish(ctx, *approvedEv); err != nil {
		return fmt.Errorf("dnaapproval: publish approved event: %w", err)
	}

	activeEv, err := domain.NewEvent(domain.EventActiveDNAChanged, "default", domain.EnvModePaper, approvalID+"-active-dna-changed", map[string]any{"strategyKey": v.StrategyKey, "contentHash": v.ContentHash, "versionId": v.ID})
	if err != nil {
		return fmt.Errorf("dnaapproval: create active dna changed event: %w", err)
	}
	if err := s.eventBus.Publish(ctx, *activeEv); err != nil {
		return fmt.Errorf("dnaapproval: publish active dna changed: %w", err)
	}
	return nil
}

func (s *Service) Reject(ctx context.Context, approvalID, decidedBy, comment string) error {
	approvalID = strings.TrimSpace(approvalID)
	decidedBy = strings.TrimSpace(decidedBy)
	if approvalID == "" {
		return errors.New("approval id is required")
	}
	if decidedBy == "" {
		return errors.New("decidedBy is required")
	}

	a, err := s.repo.GetDNAApproval(ctx, approvalID)
	if err != nil {
		return fmt.Errorf("dnaapproval: get approval: %w", err)
	}
	if a == nil {
		return errors.New("approval not found")
	}
	if a.Status != dnadomain.DNAStatusPending {
		return fmt.Errorf("approval is not pending: %s", a.Status)
	}

	if err := s.repo.UpdateDNAApproval(ctx, approvalID, dnadomain.DNAStatusRejected, decidedBy, comment); err != nil {
		return fmt.Errorf("dnaapproval: update approval: %w", err)
	}

	rejectedEv, err := domain.NewEvent(domain.EventDNARejected, "default", domain.EnvModePaper, approvalID+"-dna-rejected", map[string]any{"approvalId": approvalID, "versionId": a.VersionID})
	if err != nil {
		return fmt.Errorf("dnaapproval: create rejected event: %w", err)
	}
	if err := s.eventBus.Publish(ctx, *rejectedEv); err != nil {
		return fmt.Errorf("dnaapproval: publish rejected event: %w", err)
	}
	return nil
}

func (s *Service) GetPendingApprovals(ctx context.Context) ([]ApprovalWithVersion, error) {
	items, err := s.repo.ListPendingApprovals(ctx)
	if err != nil {
		return nil, fmt.Errorf("dnaapproval: list pending: %w", err)
	}
	out := make([]ApprovalWithVersion, 0, len(items))
	for _, a := range items {
		v, err := s.repo.GetDNAVersion(ctx, a.VersionID)
		if err != nil {
			return nil, fmt.Errorf("dnaapproval: get version: %w", err)
		}
		if v == nil {
			continue
		}
		out = append(out, ApprovalWithVersion{Approval: a, Version: *v})
	}
	return out, nil
}

func (s *Service) GetApproval(ctx context.Context, id string) (*ApprovalWithVersion, error) {
	a, err := s.repo.GetDNAApproval(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("dnaapproval: get approval: %w", err)
	}
	if a == nil {
		return nil, nil
	}
	v, err := s.repo.GetDNAVersion(ctx, a.VersionID)
	if err != nil {
		return nil, fmt.Errorf("dnaapproval: get version: %w", err)
	}
	if v == nil {
		return nil, nil
	}
	out := &ApprovalWithVersion{Approval: *a, Version: *v}
	return out, nil
}

func (s *Service) GetActiveDNA(ctx context.Context, strategyKey string) (*dnadomain.DNAVersion, error) {
	v, err := s.repo.GetActiveDNAVersion(ctx, strategyKey)
	if err != nil {
		return nil, fmt.Errorf("dnaapproval: get active: %w", err)
	}
	return v, nil
}

func (s *Service) IsVersionApproved(ctx context.Context, strategyKey, contentHash string) (bool, error) {
	active, err := s.repo.GetActiveDNAVersion(ctx, strategyKey)
	if err != nil {
		return false, fmt.Errorf("dnaapproval: get active: %w", err)
	}
	if active == nil {
		return false, nil
	}
	return active.ContentHash == contentHash, nil
}

// IsDNAApproved returns true when an active (approved) DNA version exists for
// the given strategy. It satisfies monitor.DNAGateChecker.
func (s *Service) IsDNAApproved(ctx context.Context, strategyKey string) (bool, error) {
	v, err := s.repo.GetActiveDNAVersion(ctx, strategyKey)
	if err != nil {
		return false, fmt.Errorf("dnaapproval: check approved: %w", err)
	}
	return v != nil, nil
}

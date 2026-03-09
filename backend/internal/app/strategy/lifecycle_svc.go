package strategy

import (
	"log/slog"
	"sort"

	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

type InstanceInfo struct {
	ID                 string   `json:"id"`
	StrategyName       string   `json:"strategyName"`
	Lifecycle          string   `json:"lifecycle"`
	Symbols            []string `json:"symbols"`
	IsActive           bool     `json:"isActive"`
	AllowedTransitions []string `json:"allowedTransitions"`
}

type LifecycleService struct {
	router *Router
	logger *slog.Logger
}

func NewLifecycleService(router *Router, logger *slog.Logger) *LifecycleService {
	if logger == nil {
		logger = slog.Default()
	}
	return &LifecycleService{router: router, logger: logger}
}

func (s *LifecycleService) Promote(instanceID start.InstanceID, target start.LifecycleState) error {
	inst, ok := s.router.Instance(instanceID)
	if !ok {
		return start.ErrInstanceNotFound
	}

	current := inst.Lifecycle()
	if err := start.ValidateTransition(current, target); err != nil {
		return err
	}

	inst.SetLifecycle(target)
	s.logger.Info(
		"strategy instance lifecycle transitioned",
		"instance_id", instanceID.String(),
		"from", current.String(),
		"to", target.String(),
	)
	return nil
}

func (s *LifecycleService) Deactivate(instanceID start.InstanceID) error {
	return s.Promote(instanceID, start.LifecycleDeactivated)
}

func (s *LifecycleService) Archive(instanceID start.InstanceID) error {
	return s.Promote(instanceID, start.LifecycleArchived)
}

func (s *LifecycleService) ListInstances() []InstanceInfo {
	insts := s.router.AllInstances()
	result := make([]InstanceInfo, 0, len(insts))
	for _, inst := range insts {
		if inst == nil {
			continue
		}
		lc := inst.Lifecycle()
		allowed := lc.AllowedTransitions()
		allowedStr := make([]string, 0, len(allowed))
		for _, a := range allowed {
			allowedStr = append(allowedStr, a.String())
		}
		sort.Strings(allowedStr)

		assign := inst.Assignment()
		syms := append([]string(nil), assign.Symbols...)
		sort.Strings(syms)

		name := ""
		if st := inst.Strategy(); st != nil {
			name = st.Meta().Name
		}

		result = append(result, InstanceInfo{
			ID:                 inst.ID().String(),
			StrategyName:       name,
			Lifecycle:          lc.String(),
			Symbols:            syms,
			IsActive:           inst.IsActive(),
			AllowedTransitions: allowedStr,
		})
	}

	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

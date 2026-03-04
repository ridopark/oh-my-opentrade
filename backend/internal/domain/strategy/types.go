package strategy

import (
	"errors"
	"fmt"
	"regexp"
)

// StrategyID uniquely identifies a strategy definition (e.g. "orb_break_retest").
type StrategyID string

func (id StrategyID) String() string { return string(id) }

// validStrategyID matches lowercase alphanumeric with underscores, 1-64 chars.
var validStrategyID = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// NewStrategyID creates a validated StrategyID.
func NewStrategyID(s string) (StrategyID, error) {
	if !validStrategyID.MatchString(s) {
		return "", fmt.Errorf("invalid strategy id %q: must match [a-z][a-z0-9_]{0,63}", s)
	}
	return StrategyID(s), nil
}

// Version represents a semantic version string for a strategy spec (e.g. "1.2.0").
type Version string

func (v Version) String() string { return string(v) }

// validVersion matches basic semver: major.minor.patch with optional pre-release.
var validVersion = regexp.MustCompile(`^\d+\.\d+\.\d+(-[a-zA-Z0-9.]+)?$`)

// NewVersion creates a validated Version.
func NewVersion(s string) (Version, error) {
	if !validVersion.MatchString(s) {
		return "", fmt.Errorf("invalid version %q: must match semver (e.g. 1.2.0)", s)
	}
	return Version(s), nil
}

// SignalType classifies the intent of a strategy signal.
type SignalType string

const (
	SignalEntry  SignalType = "entry"
	SignalExit   SignalType = "exit"
	SignalAdjust SignalType = "adjust"
	SignalFlat   SignalType = "flat"
)

func (t SignalType) String() string { return string(t) }

// NewSignalType creates a validated SignalType.
func NewSignalType(s string) (SignalType, error) {
	switch SignalType(s) {
	case SignalEntry, SignalExit, SignalAdjust, SignalFlat:
		return SignalType(s), nil
	default:
		return "", fmt.Errorf("invalid signal type: %q", s)
	}
}

// IsActionable returns true if the signal type represents a tradeable intent.
func (t SignalType) IsActionable() bool {
	return t == SignalEntry || t == SignalExit || t == SignalAdjust
}

// Side represents the direction of a signal.
type Side string

const (
	SideBuy  Side = "buy"
	SideSell Side = "sell"
)

func (s Side) String() string { return string(s) }

// NewSide creates a validated Side.
func NewSide(s string) (Side, error) {
	switch Side(s) {
	case SideBuy, SideSell:
		return Side(s), nil
	default:
		return "", fmt.Errorf("invalid side: %q", s)
	}
}

// ConflictPolicy determines how conflicting signals from multiple strategies are resolved.
type ConflictPolicy string

const (
	ConflictPriorityWins ConflictPolicy = "priority_wins"
	ConflictMerge        ConflictPolicy = "merge"
	ConflictVote         ConflictPolicy = "vote"
)

func (p ConflictPolicy) String() string { return string(p) }

// NewConflictPolicy creates a validated ConflictPolicy.
func NewConflictPolicy(s string) (ConflictPolicy, error) {
	switch ConflictPolicy(s) {
	case ConflictPriorityWins, ConflictMerge, ConflictVote:
		return ConflictPolicy(s), nil
	default:
		return "", fmt.Errorf("invalid conflict policy: %q", s)
	}
}

// HookEngine identifies how a hook function is implemented.
type HookEngine string

const (
	HookEngineBuiltin HookEngine = "builtin"
	HookEngineYaegi   HookEngine = "yaegi"
)

func (e HookEngine) String() string { return string(e) }

// NewHookEngine creates a validated HookEngine.
func NewHookEngine(s string) (HookEngine, error) {
	switch HookEngine(s) {
	case HookEngineBuiltin, HookEngineYaegi:
		return HookEngine(s), nil
	default:
		return "", fmt.Errorf("invalid hook engine: %q", s)
	}
}

// InstanceID uniquely identifies a running strategy instance.
// Format: "{strategy_id}:{version}:{symbol}" or generated UUID.
type InstanceID string

func (id InstanceID) String() string { return string(id) }

// NewInstanceID creates a validated InstanceID.
func NewInstanceID(s string) (InstanceID, error) {
	if s == "" {
		return "", errors.New("instance id must not be empty")
	}
	return InstanceID(s), nil
}

package strategy

import "errors"

// Sentinel errors for the strategy domain.
var (
	// Signal validation errors.
	ErrInvalidStrength = errors.New("signal strength must be between 0 and 1")
	ErrEmptySymbol     = errors.New("symbol must not be empty")

	// Lifecycle transition errors.
	ErrInvalidTransition = errors.New("invalid lifecycle state transition")
	ErrAlreadyInState    = errors.New("strategy is already in the target state")

	// State errors.
	ErrNilState       = errors.New("state must not be nil")
	ErrStateCorrupted = errors.New("state data is corrupted or incompatible")

	// Strategy registration errors.
	ErrStrategyNotFound = errors.New("strategy not found")
	ErrStrategyExists   = errors.New("strategy already registered")
	ErrInstanceNotFound = errors.New("strategy instance not found")
)

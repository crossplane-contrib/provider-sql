package errors

import (
	"fmt"
)

const (
	errGetProviderConfig         = "cannot get ProviderConfig: %s"
	errGetClusterProviderConfig  = "cannot get ClusterProviderConfig: %s"
	errGetSecret                 = "cannot get credentials Secret: %s"
	errInvalidProviderConfigKind = "invalid ProviderConfig kind: %s"
	errNoSecretRef               = "providerConfig does not reference a credentials Secret"
)

func GetProviderConfigError(err error) error { return ErrGetProviderConfig{err} }

type ErrGetProviderConfig struct{ error }

func (e ErrGetProviderConfig) Error() string {
	return fmt.Sprintf(errGetProviderConfig, e.error)
}

func (e ErrGetProviderConfig) Unwrap() error { return e.error }

func GetClusterProviderConfigError(err error) error { return ErrGetClusterProviderConfig{err} }

type ErrGetClusterProviderConfig struct{ error }

func (e ErrGetClusterProviderConfig) Error() string {
	return fmt.Sprintf(errGetClusterProviderConfig, e.error)
}

func (e ErrGetClusterProviderConfig) Unwrap() error { return e.error }

func GetSecretError(err error) error { return ErrGetSecret{err} }

type ErrGetSecret struct{ error }

func (e ErrGetSecret) Error() string {
	return fmt.Sprintf(errGetSecret, e.error)
}

func (e ErrGetSecret) Unwrap() error { return e.error }

func InvalidProviderConfigKindError(kind string) error { return ErrInvalidProviderConfigKind{kind} }

type ErrInvalidProviderConfigKind struct{ kind string }

func (e ErrInvalidProviderConfigKind) Error() string {
	return fmt.Sprintf(errInvalidProviderConfigKind, e.kind)
}

func MissingSecretRefError() error { return ErrNoSecretRef{} }

type ErrNoSecretRef struct{}

func (e ErrNoSecretRef) Error() string { return errNoSecretRef }

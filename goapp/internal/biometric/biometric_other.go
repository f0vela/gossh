//go:build !darwin

package biometric

import "errors"

var ErrCancelled = errors.New("autenticación cancelada")

func Biometrics() bool { return false }
func Available() bool  { return false }

func Authenticate(string) error {
	return errors.New("autenticación del sistema solo disponible en macOS")
}

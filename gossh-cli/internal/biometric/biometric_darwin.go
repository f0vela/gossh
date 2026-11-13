// Package biometric asks macOS to authenticate the current user with the
// LocalAuthentication framework: Touch ID when the Mac has it, falling
// back to the Apple Watch or the macOS account password. It talks to
// Objective-C through purego, so the app stays cgo-free.
package biometric

import (
	"errors"
	"fmt"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// LAPolicy values.
const (
	policyBiometrics  = 1 // LAPolicyDeviceOwnerAuthenticationWithBiometrics
	policyDeviceOwner = 2 // LAPolicyDeviceOwnerAuthentication: biometrics or password
)

// LAError codes the app distinguishes.
const (
	codeUserCancel   = -2
	codeSystemCancel = -4
	codeAppCancel    = -9
)

// ErrCancelled is returned when the user dismissed the prompt.
var ErrCancelled = errors.New("autenticación cancelada")

var (
	loadErr error

	selAlloc    = objc.RegisterName("alloc")
	selInit     = objc.RegisterName("init")
	selRelease  = objc.RegisterName("release")
	selCan      = objc.RegisterName("canEvaluatePolicy:error:")
	selEvaluate = objc.RegisterName("evaluatePolicy:localizedReason:reply:")
	selInval    = objc.RegisterName("invalidate")
	selCode     = objc.RegisterName("code")
	selDesc     = objc.RegisterName("localizedDescription")
	selUTF8     = objc.RegisterName("UTF8String")
	selNSString = objc.RegisterName("stringWithUTF8String:")
)

func init() {
	_, loadErr = purego.Dlopen("/System/Library/Frameworks/LocalAuthentication.framework/LocalAuthentication",
		purego.RTLD_GLOBAL|purego.RTLD_NOW)
}

func newContext() (objc.ID, error) {
	if loadErr != nil {
		return 0, loadErr
	}
	cls := objc.GetClass("LAContext")
	if cls == 0 {
		return 0, errors.New("LocalAuthentication no disponible")
	}
	return objc.ID(cls).Send(selAlloc).Send(selInit), nil
}

// bool reads an Objective-C BOOL: only the low byte of the register counts.
func isYes(v uintptr) bool { return v&0xff != 0 }

func can(policy int) bool {
	ctx, err := newContext()
	if err != nil {
		return false
	}
	defer ctx.Send(selRelease)
	return isYes(objc.Send[uintptr](ctx, selCan, policy, objc.ID(0)))
}

// Biometrics reports whether the Mac has enrolled biometrics (Touch ID).
func Biometrics() bool { return can(policyBiometrics) }

// Available reports whether the user can authenticate at all (biometrics,
// Apple Watch or an account password). False on a Mac with no password.
func Available() bool { return can(policyDeviceOwner) }

// Authenticate shows the system prompt and blocks until the user
// authenticates, cancels or fails. It must not run on the UI thread.
func Authenticate(reason string) error {
	ctx, err := newContext()
	if err != nil {
		return err
	}
	defer ctx.Send(selRelease)
	return evaluate(ctx, reason, nil)
}

// evaluate runs the prompt on ctx; started, if set, is called once the
// request is in flight (tests invalidate the context from it).
func evaluate(ctx objc.ID, reason string, started func()) error {
	type result struct {
		ok  bool
		err error
	}
	done := make(chan result, 1)
	block := objc.NewBlock(func(_ objc.Block, success uint8, nsErr objc.ID) {
		if success&1 != 0 {
			done <- result{ok: true}
			return
		}
		done <- result{err: describe(nsErr)}
	})
	defer block.Release()

	reasonStr := objc.ID(objc.GetClass("NSString")).Send(selNSString, reason)
	ctx.Send(selEvaluate, policyDeviceOwner, reasonStr, block)
	if started != nil {
		started()
	}
	r := <-done
	return r.err
}

func describe(nsErr objc.ID) error {
	if nsErr == 0 {
		return errors.New("autenticación fallida")
	}
	switch code := objc.Send[int](nsErr, selCode); code {
	case codeUserCancel, codeSystemCancel, codeAppCancel:
		return ErrCancelled
	default:
		msg := objc.Send[string](objc.Send[objc.ID](nsErr, selDesc), selUTF8)
		return fmt.Errorf("autenticación fallida (%d): %s", code, msg)
	}
}

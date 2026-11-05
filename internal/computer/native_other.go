//go:build !darwin || !cgo

package computer

import (
	"context"
	"errors"
	"runtime"
)

type nativeDriver struct{}

var errUnsupported = errors.New("native desktop control requires a macOS build with CGO enabled")

func (nativeDriver) Permissions() Permissions { return Permissions{Platform: runtime.GOOS} }
func (nativeDriver) Displays() []Bounds       { return nil }
func (nativeDriver) Capture(context.Context, int) ([]byte, Bounds, error) {
	return nil, Bounds{}, errUnsupported
}
func (nativeDriver) CaptureWindow(context.Context, int) ([]byte, error) { return nil, errUnsupported }
func (nativeDriver) Input(context.Context, Action) error                { return errUnsupported }
func (nativeDriver) Clipboard(context.Context, *string) (string, error) {
	return "", errUnsupported
}
func (nativeDriver) Windows(context.Context) ([]Window, error) { return nil, errUnsupported }
func (nativeDriver) UITree(context.Context, string, int, int) (UITree, error) {
	return UITree{}, errUnsupported
}
func (nativeDriver) OpenApp(context.Context, string) error { return errUnsupported }

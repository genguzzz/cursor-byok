package codec

import "fmt"

// errorf builds a wrapped error without importing fmt at every call site.
func errorf(format string, args ...interface{}) error {
	return fmt.Errorf(format, args...)
}

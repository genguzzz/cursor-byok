package server

import (
	"log"
)

// printLog writes one request line to the standard logger.
func printLog(format string, args ...interface{}) {
	log.Printf(format, args...)
}

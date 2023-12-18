//go:build !cgo || nowatchdog

package connmgr

import "github.com/sirupsen/logrus"

func registerWatchdog(func()) (unregister func()) {
	return nil
}

// WithEmergencyTrim is an option to enable trimming connections on memory emergency.
func WithEmergencyTrim(enable bool) Option {
	return func(cfg *config) error {
		logrus.Warn("platform doesn't support go-watchdog")
		return nil
	}
}

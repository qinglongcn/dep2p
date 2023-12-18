package config

import (
	"strings"
	"sync"

	"github.com/sirupsen/logrus"
	"go.uber.org/fx/fxevent"
)

var (
	fxLogger    fxevent.Logger
	logInitOnce sync.Once
)

type fxLogWriter struct{}

func (l *fxLogWriter) Write(b []byte) (int, error) {
	logrus.Debug(strings.TrimSuffix(string(b), "\n"))
	return len(b), nil
}

func getFXLogger() fxevent.Logger {
	logInitOnce.Do(func() { fxLogger = &fxevent.ConsoleLogger{W: &fxLogWriter{}} })
	return fxLogger
}

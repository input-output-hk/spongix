package logger

import (
	"github.com/pkg/errors"
	"go.uber.org/zap"
)

func SetupLogger(logMode, logLevel string) (*zap.Logger, error) {
	lvl := zap.NewAtomicLevel()
	if err := lvl.UnmarshalText([]byte(logLevel)); err != nil {
		return nil, errors.WithMessage(err, "parsing loglevel")
	}
	development := logMode == "development"
	encoding := "json"
	encoderConfig := zap.NewProductionEncoderConfig()
	if development {
		encoding = "console"
		encoderConfig = zap.NewDevelopmentEncoderConfig()
	}

	l := zap.Config{
		Level:             lvl,
		Development:       development,
		DisableCaller:     false,
		DisableStacktrace: false,
		Sampling:          &zap.SamplingConfig{Initial: 1, Thereafter: 2},
		Encoding:          encoding,
		EncoderConfig:     encoderConfig,
		OutputPaths:       []string{"stderr"},
		ErrorOutputPaths:  []string{"stderr"},
	}

	if log, err := l.Build(); err != nil {
		return nil, errors.WithMessage(err, "building logger")
	} else {
		return log, nil
	}
}

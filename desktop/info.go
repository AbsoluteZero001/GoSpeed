package main

import (
	"runtime"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/speedtest"
)

func runtimeVersion() string {
	return runtime.Version()
}

func platformName() string {
	return runtime.GOOS + "/" + runtime.GOARCH
}

func maxConnections() int {
	return speedtest.MaxConnections
}

func defaultConnections() int {
	return speedtest.DefaultConnections
}

func defaultDurationMs() int {
	return int(speedtest.DefaultDuration / time.Millisecond)
}

func defaultSampleIntervalMs() int {
	return int(speedtest.DefaultSampleInterval / time.Millisecond)
}

func defaultLatencySamples() int {
	return speedtest.DefaultLatencySamples
}

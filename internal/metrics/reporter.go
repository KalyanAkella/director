package metrics

import (
	"gopkg.in/alexcesaro/statsd.v2"
)

type TimingContext struct {
	Context interface{}
}

type Reporter interface {
	Increment(tag string)
	Gauge(tag string, value interface{})
	Count(tag string, value interface{})
	StartTiming() *TimingContext
	EndTiming(tc *TimingContext, tag string)
}

type noopReporter struct{}

func (r *noopReporter) StartTiming() *TimingContext             { return nil }
func (r *noopReporter) Increment(tag string)                    {}
func (r *noopReporter) Gauge(tag string, value interface{})     {}
func (r *noopReporter) Count(tag string, value interface{})     {}
func (r *noopReporter) Time(tag string)                         {}
func (r *noopReporter) EndTiming(tc *TimingContext, tag string) {}

func NewNoopReporter() *noopReporter {
	return &noopReporter{}
}

type statsDReporter struct {
	client     *statsd.Client
	errHandler StatsDErrorHandler
}

type StatsDErrorHandler func(string)

func NewStatsDReporter(metricPrefix, statsdAddr string, errorHandler StatsDErrorHandler) (*statsDReporter, error) {
	opts := []statsd.Option{statsd.Prefix(metricPrefix), statsd.Address(statsdAddr)}
	if client, err := statsd.New(opts...); err == nil && client != nil {
		return &statsDReporter{client, errorHandler}, nil
	} else {
		return nil, err
	}
}

func (r *statsDReporter) Close() {
	defer r.errHandler("Close")
	r.client.Close()
}

func (r *statsDReporter) Increment(tag string) {
	defer r.errHandler("Increment")
	r.client.Increment(tag)
}

func (r *statsDReporter) Gauge(tag string, value interface{}) {
	defer r.errHandler("Gauge")
	r.client.Gauge(tag, value)
}

func (r *statsDReporter) Count(tag string, value interface{}) {
	defer r.errHandler("Count")
	r.client.Count(tag, value)
}

func (r *statsDReporter) StartTiming() *TimingContext {
	defer r.errHandler("StartTiming")
	return &TimingContext{Context: r.client.NewTiming()}
}

func (r *statsDReporter) EndTiming(tc *TimingContext, tag string) {
	defer r.errHandler("EndTiming")
	if tc.Context != nil {
		tc.Context.(statsd.Timing).Send(tag)
	}
}

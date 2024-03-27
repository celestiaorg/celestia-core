package node

import (
	"github.com/cometbft/cometbft/config"
	"github.com/grafana/pyroscope-go"

	otelpyroscope "github.com/grafana/otel-profiling-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// setupPyroscope sets up pyroscope profiler and optionally tracing.
func setupPyroscope(instCfg *config.InstrumentationConfig, nodeID string) (*pyroscope.Profiler, *sdktrace.TracerProvider, error) {
	tp, err := tracerProviderDebug()
	if err != nil {
		return nil, nil, err
	}

	labels := map[string]string{"node_id": nodeID}

	if instCfg.PyroscopeTrace {
		if _, err = setupTracing(instCfg.PyroscopeURL, labels); err != nil {
			return nil, nil, err
		}
	} else {
		tp = nil
	}

	pflr, err := pyroscope.Start(pyroscope.Config{
		ApplicationName: "celestia",
		ServerAddress:   instCfg.PyroscopeURL,
		Logger:          nil, // use the noop logger by passing nil
		Tags:            labels,
		ProfileTypes:    toPyroscopeProfiles(instCfg.PyroscopeProfileTypes),
	})

	return pflr, tp, err
}

func setupTracing(addr string, labels map[string]string) (tp *sdktrace.TracerProvider, err error) {
	tp, err = tracerProviderDebug()
	if err != nil {
		return nil, err
	}

	// Set the Tracer Provider and the W3C Trace Context propagator as globals.
	// We wrap the tracer provider to also annotate goroutines with Span ID so
	// that pprof would add corresponding labels to profiling samples.
	otel.SetTracerProvider(otelpyroscope.NewTracerProvider(tp,
		otelpyroscope.WithAppName("celestia"),
		otelpyroscope.WithRootSpanOnly(true),
		otelpyroscope.WithAddSpanName(true),
		otelpyroscope.WithPyroscopeURL(addr),
		otelpyroscope.WithProfileBaselineLabels(labels),
		otelpyroscope.WithProfileBaselineURL(true),
		otelpyroscope.WithProfileURL(true),
	))

	// Register the trace context and baggage propagators so data is propagated across services/processes.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return tp, err
}

func tracerProviderDebug() (*sdktrace.TracerProvider, error) {
	exp, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
	if err != nil {
		return nil, err
	}
	return sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sdktrace.NewBatchSpanProcessor(exp))), nil
}

func toPyroscopeProfiles(profiles string) []pyroscope.ProfileType {
	parsedProfiles := splitAndTrimEmpty(profiles, ",", " ")
	pts := make([]pyroscope.ProfileType, 0, len(parsedProfiles))
	for _, p := range profiles {
		pts = append(pts, pyroscope.ProfileType(p))
	}
	return pts
}

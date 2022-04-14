package exporters

import (
	"context"
	"fmt"
	"testing"

	"github.com/grafana/agent/pkg/integrations/v2/app_o11y_receiver/models"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/stretchr/testify/require"
)

type metricAssertion struct {
	name  string
	value float64
}

func testcase(t *testing.T, payload models.Payload, assertions []metricAssertion) {
	ctx := context.Background()

	reg := prometheus.NewRegistry()

	exporter := NewReceiverMetricsExporter(ReceiverMetricsExporterConfig{Reg: reg})

	err := exporter.Export(ctx, payload)
	require.NoError(t, err)

	metrics, err := reg.Gather()
	require.NoError(t, err)

	for _, assertion := range assertions {
		found := false
		for _, metric := range metrics {
			if *metric.Name == assertion.name {
				found = true
				require.Len(t, metric.Metric, 1)
				val := metric.Metric[0].Counter.Value
				require.Equal(t, assertion.value, *val)
				break
			}
		}
		if !found {
			require.Fail(t, fmt.Sprintf("metric [%s] not found", assertion.name))
		}
	}
}

func TestReceiverMetricsExport(t *testing.T) {
	var payload models.Payload
	payload.Logs = make([]models.Log, 2)
	payload.Measurements = make([]models.Measurement, 3)
	payload.Exceptions = make([]models.Exception, 4)
	testcase(t, payload, []metricAssertion{
		{
			name:  "app_agent_receiver_logs_total",
			value: 2,
		},
		{
			name:  "app_agent_receiver_measurements_total",
			value: 3,
		},
		{
			name:  "app_agent_receiver_exceptions_total",
			value: 4,
		},
	})
}

func TestReceiverMetricsExportLogsOnly(t *testing.T) {
	var payload models.Payload
	payload.Logs = []models.Log{
		{},
		{},
	}
	testcase(t, payload, []metricAssertion{
		{
			name:  "app_agent_receiver_logs_total",
			value: 2,
		},
		{
			name:  "app_agent_receiver_measurements_total",
			value: 0,
		},
		{
			name:  "app_agent_receiver_exceptions_total",
			value: 0,
		},
	})
}

func TestReceiverMetricsExportExceptionsOnly(t *testing.T) {
	var payload models.Payload
	payload.Exceptions = []models.Exception{
		{},
		{},
		{},
		{},
	}
	testcase(t, payload, []metricAssertion{
		{
			name:  "app_agent_receiver_logs_total",
			value: 0,
		},
		{
			name:  "app_agent_receiver_measurements_total",
			value: 0,
		},
		{
			name:  "app_agent_receiver_exceptions_total",
			value: 4,
		},
	})
}

func TestReceiverMetricsExportMeasurementsOnly(t *testing.T) {
	var payload models.Payload
	payload.Measurements = []models.Measurement{
		{},
		{},
		{},
	}
	testcase(t, payload, []metricAssertion{
		{
			name:  "app_agent_receiver_logs_total",
			value: 0,
		},
		{
			name:  "app_agent_receiver_measurements_total",
			value: 3,
		},
		{
			name:  "app_agent_receiver_exceptions_total",
			value: 0,
		},
	})
}

package health

import (
	"fmt"

	"github.com/GoogleCloudPlatform/cloud-run-release-operator/pkg/config"
)

// StringReport returns a human-readable report of the diagnosis.
func StringReport(healthCriteria []config.HealthCriterion, diagnosis Diagnosis) string {
	report := fmt.Sprintf("status: %s\n", diagnosis.OverallResult.String())
	report += "metrics:"
	for i, result := range diagnosis.CheckResults {
		criteria := healthCriteria[i]

		// Include percentile value for latency criteria.
		if criteria.Metric == config.LatencyMetricsCheck {
			report += fmt.Sprintf("\n- %s[p%.0f]: %.2f (needs %.2f)", criteria.Metric, criteria.Percentile, result.ActualValue, criteria.Threshold)
			continue
		}

		format := "\n- %s: %.2f (needs %.2f)"
		if criteria.Metric == config.RequestCountMetricsCheck {
			// No decimals for request count.
			format = "\n- %s: %.0f (needs %.0f)"
		}
		report += fmt.Sprintf(format, criteria.Metric, result.ActualValue, criteria.Threshold)
	}

	return report
}

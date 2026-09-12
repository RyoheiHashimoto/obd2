package obd2

import (
	"math"
	"strconv"
)

// formatValue rounds v to two decimal places for display.
func formatValue(v float64) string {
	return strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64)
}

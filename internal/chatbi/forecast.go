package chatbi

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"
)

var ErrForecastUnavailable = errors.New("forecast requires an ordered numeric or date series with at least three rows")

type ForecastPoint struct {
	Label string  `json:"label"`
	Value float64 `json:"value"`
}

type Forecast struct {
	RecordID    ID              `json:"record_id"`
	LabelField  string          `json:"label_field"`
	ValueField  string          `json:"value_field"`
	InputRows   int             `json:"input_rows"`
	Points      []ForecastPoint `json:"points"`
	Method      string          `json:"method"`
	AuditID     int64           `json:"audit_id"`
	GeneratedAt time.Time       `json:"generated_at"`
	digest      string
}

func orderedLabels(labels []string) ([]float64, func(float64) string, error) {
	if len(labels) < 3 {
		return nil, nil, ErrForecastUnavailable
	}
	all := func(parse func(string) (float64, error)) ([]float64, bool) {
		values := make([]float64, len(labels))
		for i, label := range labels {
			value, err := parse(label)
			if err != nil || !finite(value) || i > 0 && value <= values[i-1] {
				return nil, false
			}
			values[i] = value
		}
		return values, true
	}
	if values, ok := all(func(label string) (float64, error) {
		value, err := time.Parse("2006-01-02", label)
		return float64(value.Unix() / 86400), err
	}); ok {
		return values, func(x float64) string { return time.Unix(int64(math.Round(x))*86400, 0).UTC().Format("2006-01-02") }, nil
	}
	if values, ok := all(func(label string) (float64, error) {
		value, err := time.Parse("2006-01", label)
		return float64(value.Year()*12 + int(value.Month()) - 1), err
	}); ok {
		return values, func(x float64) string {
			month := int(math.Round(x))
			return time.Date(month/12, time.Month(month%12+1), 1, 0, 0, 0, 0, time.UTC).Format("2006-01")
		}, nil
	}
	if values, ok := all(func(label string) (float64, error) {
		value, err := time.Parse("2006", label)
		return float64(value.Year()), err
	}); ok {
		return values, func(x float64) string { return strconv.FormatInt(int64(math.Round(x)), 10) }, nil
	}
	if values, ok := all(func(label string) (float64, error) { return strconv.ParseFloat(label, 64) }); ok {
		return values, func(x float64) string { return strconv.FormatFloat(x, 'f', -1, 64) }, nil
	}
	return nil, nil, ErrForecastUnavailable
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func forecastData(id ID, data QueryData) (Forecast, error) {
	if len(data.Data) < 3 || len(data.Data) > 100 || data.Truncated || data.AuditID <= 0 || len(data.Fields) < 2 {
		return Forecast{}, ErrForecastUnavailable
	}
	labelField := data.Fields[0]
	for _, valueField := range data.Fields[1:] {
		labels := make([]string, len(data.Data))
		y := make([]float64, len(data.Data))
		valid := true
		for i, row := range data.Data {
			if row[labelField] == nil || row[valueField] == nil {
				valid = false
				break
			}
			labels[i] = fmt.Sprint(row[labelField])
			var err error
			y[i], err = strconv.ParseFloat(fmt.Sprint(row[valueField]), 64)
			if err != nil || !finite(y[i]) {
				valid = false
				break
			}
		}
		if !valid {
			continue
		}
		x, format, err := orderedLabels(labels)
		if err != nil {
			continue
		}
		xMean, yMean := 0.0, 0.0
		for i := range x {
			xMean += x[i]
			yMean += y[i]
		}
		xMean /= float64(len(x))
		yMean /= float64(len(y))
		variance, covariance := 0.0, 0.0
		for i := range x {
			dx := x[i] - xMean
			variance += dx * dx
			covariance += dx * (y[i] - yMean)
		}
		if variance <= 0 || !finite(variance) {
			continue
		}
		slope := covariance / variance
		steps := make([]float64, 0, len(x)-1)
		for i := 1; i < len(x); i++ {
			steps = append(steps, x[i]-x[i-1])
		}
		sort.Float64s(steps)
		step := steps[len(steps)/2]
		if !finite(slope) || !finite(step) || step <= 0 {
			continue
		}
		points := make([]ForecastPoint, 0, 3)
		for i := 1; i <= 3; i++ {
			futureX := x[len(x)-1] + float64(i)*step
			futureY := yMean + slope*(futureX-xMean)
			if !finite(futureY) || math.Abs(futureY) > 1e15 {
				valid = false
				break
			}
			points = append(points, ForecastPoint{Label: format(futureX), Value: math.Round(futureY*100) / 100})
		}
		if !valid {
			continue
		}
		return Forecast{RecordID: id, LabelField: labelField, ValueField: valueField, InputRows: len(data.Data), Points: points, Method: "illustrative_least_squares_linear_trend", AuditID: data.AuditID, GeneratedAt: time.Now().UTC(), digest: resultDigest(data)}, nil
	}
	return Forecast{}, ErrForecastUnavailable
}

func (service *Service) ForecastRecord(ctx context.Context, p Principal, id ID) (Forecast, error) {
	base, err := service.Record(ctx, p, id, false)
	if err != nil {
		return Forecast{}, err
	}
	if base.Action != "" || !base.Finish || base.Error != "" {
		return Forecast{}, ErrForecastUnavailable
	}
	record, err := service.Record(ctx, p, id, true)
	if err != nil {
		return Forecast{}, err
	}
	if record.Data == nil {
		return Forecast{}, ErrGateway
	}
	forecast, err := forecastData(id, *record.Data)
	if err != nil {
		return Forecast{}, err
	}
	if err := service.live(ctx, p); err != nil {
		return Forecast{}, err
	}
	return forecast, nil
}

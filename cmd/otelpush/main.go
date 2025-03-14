package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type Data struct {
	ResourceMetrics []ResourceMetric
}

type ResourceMetric struct {
	ScopeMetrics []ScopeMetric
}

type ScopeMetric struct {
	Metrics []Metric
}

type Metric struct {
	Name        string
	Unit        string
	Description string
	Gauge       Gauge
}

type Gauge struct {
	DataPoints []DataPoint
}

type DataPoint struct {
	AsInt        int     `json:",omitzero"`
	AsDouble     float64 `json:",omitezero"`
	TimeUnixNano int64
	Attributes   []Attribute
}

type Attribute struct {
	Key   string
	Value map[string]string
}

func main() {
	if err := execute(); err != nil {
		log.Fatal(err)
	}
}

func execute() error {
	resp, err := http.Get("https://exporter.web-apps.tech/metrics")
	if err != nil {
		return fmt.Errorf("failed to get metrics: %w", err)
	}
	defer func() {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	scanner := bufio.NewScanner(resp.Body)

	data := Data{
		ResourceMetrics: []ResourceMetric{
			{
				ScopeMetrics: []ScopeMetric{
					{},
				},
			},
		},
	}

	now := time.Now().UnixNano()
	for scanner.Scan() {
		line := scanner.Text()

		switch {
		case strings.HasPrefix(line, "# TYPE"):
			// Currently only gauge is used
		case strings.HasPrefix(line, "# HELP"):
			// I don't use HELP line
		default:
			// metric line
			metric, err := parseMetricLine(line)
			if err != nil {
				return fmt.Errorf("error parsing metric line: %w", err)
			}
			metric.Gauge.DataPoints[0].TimeUnixNano = now

			data.ResourceMetrics[0].ScopeMetrics[0].Metrics = append(
				data.ResourceMetrics[0].ScopeMetrics[0].Metrics,
				metric,
			)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error on scanning: %w", err)
	}

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(data); err != nil {
		return err
	}

	if _, err := buf.WriteTo(os.Stdout); err != nil {
		return err
	}

	return nil
}

func parseMetricLine(line string) (Metric, error) {
	return parseMetricName(line, Metric{}, "")
}

func parseMetricName(line string, metric Metric, name string) (Metric, error) {
	switch r, size := utf8.DecodeRuneInString(line); r {
	case '{':
		metric.Name = name
		return parseLabels(line[size:], metric)
	default:
		return parseMetricName(line[size:], metric, name+string([]rune{r}))
	}
}

func parseLabels(line string, metric Metric) (Metric, error) {
	switch r, size := utf8.DecodeRuneInString(line); r {
	case '}':
		return parseMetricValue(line[size:], metric)
	case '"', '=':
		return metric, fmt.Errorf("unexpected `%c`", r)
	case ',':
		return parseLabelKey(line[size:], metric, "")
	default:
		return parseLabelKey(line, metric, "")
	}
}

func parseLabelKey(line string, metric Metric, labelKey string) (Metric, error) {
	switch r, size := utf8.DecodeRuneInString(line); r {
	case '=':
		if r2, size2 := utf8.DecodeRuneInString(line[size:]); r2 == '"' {
			return parseLabelValue(line[size+size2:], metric, labelKey, "")
		} else {
			return metric, fmt.Errorf("`\"` is expected but got %c", r2)
		}
	default:
		return parseLabelKey(line[size:], metric, labelKey+string([]rune{r}))
	}
}

func parseLabelValue(line string, metric Metric, labelKey, labelValue string) (Metric, error) {
	switch r, size := utf8.DecodeRuneInString(line); r {
	case '"':
		metric.Gauge.DataPoints = append(metric.Gauge.DataPoints, DataPoint{
			Attributes: []Attribute{
				{
					Key: labelKey,
					Value: map[string]string{
						"stringValue": labelValue,
					},
				},
			},
		})
		return parseLabels(line[size:], metric)
	default:
		return parseLabelValue(line[size:], metric, labelKey, labelValue+string([]rune{r}))
	}
}

func parseMetricValue(line string, metric Metric) (Metric, error) {
	r, size := utf8.DecodeRuneInString(line)
	if r != ' ' {
		return metric, fmt.Errorf("` ` is expected but got `%c`", r)
	}

	if i, err := strconv.Atoi(line[size:]); err == nil {
		metric.Gauge.DataPoints[0].AsInt = i
		return metric, nil
	} else if f, err := strconv.ParseFloat(line[size:], 64); err == nil {
		metric.Gauge.DataPoints[0].AsDouble = f
		return metric, nil
	} else {
		return metric, fmt.Errorf("couldn't parse the value part: %s", line[size:])
	}
}

func push(r io.Reader) error {
	API_KEY := os.Getenv("GRAFANA_API_KEY")
	HOST := "https://otlp-gateway-prod-ap-northeast-0.grafana.net/otlp/v1/metrics"

	URL := fmt.Sprintf("https://%s/otlp/v1/metrics", HOST)

	req, err := http.NewRequest(http.MethodPost, URL, r)
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+API_KEY)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	log.Println("Response Status:", resp.Status)
	return nil
}

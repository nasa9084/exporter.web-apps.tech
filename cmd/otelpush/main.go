package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/http/httputil"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type Data struct {
	ResourceMetrics []ResourceMetric `json:"resourceMetrics"`
}

type ResourceMetric struct {
	ScopeMetrics []ScopeMetric `json:"scopeMetrics"`
}

type ScopeMetric struct {
	Metrics []Metric `json:"metrics"`
}

type Metric struct {
	Name        string `json:"name"`
	Unit        string `json:"unit"`
	Description string `json:"description"`
	Gauge       Gauge  `json:"gauge"`
}

type Gauge struct {
	DataPoints []DataPoint `json:"dataPoints"`
}

type DataPoint interface {
	SetTimeUnixNano(int64)
	SetAttributes([]Attribute)
}

type IntDataPoint struct {
	AsInt        int         `json:"asInt"`
	TimeUnixNano int64       `json:"timeUnixNano"`
	Attributes   []Attribute `json:"attributes"`
}

type DoubleDataPoint struct {
	AsDouble     float64     `json:"asDouble"`
	TimeUnixNano int64       `json:"timeUnixNano"`
	Attributes   []Attribute `json:"attributes"`
}

type Attribute struct {
	Key   string            `json:"key"`
	Value map[string]string `json:"value"`
}

func main() {
	if err := execute(); err != nil {
		log.Fatal(err)
	}
}

func execute() error {
	resp, err := retryHTTPGet(fmt.Sprintf("http://localhost:%s/metrics", os.Getenv("EXPORTER_PORT")), 5)
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

			metric.Gauge.DataPoints[0].SetTimeUnixNano(now)

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

	return push(&buf)
}

func retryHTTPGet(target string, retryCount int) (*http.Response, error) {
	for i := 0; i < retryCount; i++ {
		resp, err := http.Get(target)
		if err != nil {
			log.Print("failed to get metrics: %s", err)

			sleep := int(math.Pow(2, float64(i+1)))
			log.Printf("wait for %d seconds", sleep)
			time.Sleep(time.Duration(sleep) * time.Second)
			continue
		}

		return resp, nil
	}

	return nil, fmt.Errorf("failed %d times and gave up", retryCount)
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
		dp, err := parseMetricValue(line[size:], metric)
		if err != nil {
			return metric, err
		}
		metric.Gauge.DataPoints = append(metric.Gauge.DataPoints, dp)
		return metric, nil
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
		metric.Gauge.DataPoints[0].SetAttributes([]Attribute{{
			Key: labelKey,
			Value: map[string]string{
				"stringValue": labelValue,
			},
		}})
		return parseLabels(line[size:], metric)
	default:
		return parseLabelValue(line[size:], metric, labelKey, labelValue+string([]rune{r}))
	}
}

func parseMetricValue(line string, metric Metric) (DataPoint, error) {
	r, size := utf8.DecodeRuneInString(line)
	if r != ' ' {
		return nil, fmt.Errorf("` ` is expected but got `%c`", r)
	}

	if i, err := strconv.Atoi(line[size:]); err == nil {
		return &IntDataPoint{AsInt: i}, nil
	} else if f, err := strconv.ParseFloat(line[size:], 64); err == nil {
		return &DoubleDataPoint{AsDouble: f}, nil
	} else {
		return nil, fmt.Errorf("couldn't parse the value part: %s", line[size:])
	}
}

func push(r io.Reader) error {
	apiKey := os.Getenv("GRAFANA_API_KEY")
	if apiKey == "" {
		return errors.New("GRAFANA_API_KEY is not configured")
	}

	url := fmt.Sprintf("https://otlp-gateway-prod-ap-northeast-0.grafana.net/otlp/v1/metrics")

	req, err := http.NewRequest(http.MethodPost, url, r)
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	b, err := httputil.DumpRequestOut(req, true)
	if err != nil {
		return err
	}

	log.Printf("Request:\n%s", b)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	log.Println("Response Status:", resp.Status)

	if resp.StatusCode != http.StatusOK {
		b, err := httputil.DumpResponse(resp, true)
		if err != nil {
			return err
		}

		log.Printf("Response Body:\n%s", b)

		return fmt.Errorf("unexpected http status code: %s", resp.Status)
	}

	return nil
}

func (idp *IntDataPoint) SetTimeUnixNano(t int64) {
	idp.TimeUnixNano = t
}

func (idp *IntDataPoint) SetAttributes(attributes []Attribute) {
	idp.Attributes = attributes
}

func (ddp *DoubleDataPoint) SetTimeUnixNano(t int64) {
	ddp.TimeUnixNano = t
}

func (ddp *DoubleDataPoint) SetAttributes(attributes []Attribute) {
	ddp.Attributes = attributes
}

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
	"slices"
	"strconv"
	"strings"
	"time"
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

type DataPoint struct {
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
			log.Printf("failed to get metrics: %s", err)

			sleep := int(math.Pow(2, float64(i+1)))
			log.Printf("wait for %d seconds", sleep)
			time.Sleep(time.Duration(sleep) * time.Second)
			continue
		}

		return resp, nil
	}

	return nil, fmt.Errorf("failed %d times and gave up", retryCount)
}

func NewMetric() Metric {
	return Metric{
		Gauge: Gauge{
			DataPoints: []DataPoint{{
				TimeUnixNano: clock.Now().UnixNano(),
			}},
		},
	}
}

type StateFunc func(*bufio.Reader, chan Token) StateFunc

type Token interface {
	Apply(*Metric) error
}

type TokenFunc func(*Metric) error

func (f TokenFunc) Apply(m *Metric) error { return f(m) }

type LabelToken struct {
	Key, Value string
}

func (t LabelToken) Apply(m *Metric) error {
	m.Gauge.DataPoints[0].Attributes = append(m.Gauge.DataPoints[0].Attributes, Attribute{
		Key: t.Key,
		Value: map[string]string{
			"stringValue": t.Value,
		},
	})
	return nil
}

func parseMetricLine(line string) (Metric, error) {
	buf := bufio.NewReader(strings.NewReader(line))

	ch := make(chan Token, 1)
	go func() {
		defer close(ch)

		var state StateFunc = parseMetricName
		for state != nil {
			state = state(buf, ch)
		}
	}()

	metric := NewMetric()
	for token := range ch {
		if err := token.Apply(&metric); err != nil {
			return metric, err
		}
	}

	return metric, nil
}

func parseMetricName(r *bufio.Reader, ch chan Token) StateFunc {
	var buf bytes.Buffer

	for {
		c, err := peekRune(r)
		if err != nil {
			return parseError(err)
		}

		switch c {
		case '{':
			ch <- TokenFunc(func(m *Metric) error {
				m.Name = buf.String()
				return nil
			})

			if err := consumeRune(r); err != nil {
				return parseError(err)
			}

			return parseLabels
		default:
			if err := consumeRune(r); err != nil {
				return parseError(err)
			}

			buf.WriteRune(c)
		}
	}
}

func parseLabels(r *bufio.Reader, ch chan Token) StateFunc {
	c, err := peekRune(r)
	if err != nil {
		return parseError(err)
	}

	switch c {
	case ',', ' ':
		if err := consumeRune(r); err != nil {
			return parseError(err)
		}

		return parseLabels
	case '}':
		if err := consumeRune(r); err != nil {
			return parseError(err)
		}

		return parseMetricValue
	default:
		return parseLabel
	}
}

func parseLabel(r *bufio.Reader, ch chan Token) StateFunc {
	var token LabelToken
	if err := parseLabelKey(r, &token); err != nil {
		return parseError(err)
	}

	if c, err := readRune(r); err != nil {
		return parseError(err)
	} else if c != '"' {
		return parseError(fmt.Errorf("`\"` is expected but got %c", c))
	}

	if err := parseLabelValue(r, &token); err != nil {
		return parseError(err)
	}

	ch <- token

	return parseLabels
}

func parseLabelKey(r *bufio.Reader, token *LabelToken) error {
	var buf bytes.Buffer
	for {
		c, err := readRune(r)
		if err != nil {
			return err
		}

		switch c {
		case '=':
			token.Key = buf.String()
			return nil
		default:
			buf.WriteRune(c)
		}
	}
}

func parseLabelValue(r *bufio.Reader, token *LabelToken) error {
	var buf bytes.Buffer
	for {
		c, err := readRune(r)
		if err != nil {
			return err
		}

		switch c {
		case '"':
			token.Value = buf.String()
			return nil
		default:
			buf.WriteRune(c)
		}
	}
}

func parseMetricValue(r *bufio.Reader, ch chan Token) StateFunc {
	c, err := peekRune(r)
	if err != nil {
		return parseError(err)
	}

	switch c {
	case ' ':
		if err := consumeRune(r); err != nil {
			return parseError(err)
		}

		return parseMetricValue
	default:
	}

	var buf bytes.Buffer
	acceptable := []rune{'0', '1', '2', '3', '4', '5', '6', '7', '8', '9', '.'}
	for {
		c, err := readRune(r)
		if err != nil {
			if errors.Is(err, io.EOF) {
				value := buf.String()
				if f, err := strconv.ParseFloat(value, 64); err != nil {
					return parseError(err)
				} else {
					ch <- TokenFunc(func(m *Metric) error {
						m.Gauge.DataPoints[0].AsDouble = f
						return nil
					})
					return nil
				}
			}

			return parseError(err)
		}

		if slices.Contains(acceptable, c) {
			buf.WriteRune(c)
		} else {
			return parseError(fmt.Errorf("parseMetricValue: unexpected %c", c))
		}
	}
}

func readRune(r *bufio.Reader) (rune, error) {
	c, _, err := r.ReadRune()

	return c, err
}

func consumeRune(r *bufio.Reader) error {
	_, err := readRune(r)
	return err
}

func peekRune(r *bufio.Reader) (rune, error) {
	c, _, err := r.ReadRune()
	if err != nil {
		return '\ufffd', err
	}

	if err := r.UnreadRune(); err != nil {
		return '\ufffd', err
	}

	return c, nil
}

func parseError(err error) StateFunc {
	return func(_ *bufio.Reader, ch chan Token) StateFunc {
		ch <- TokenFunc(func(*Metric) error { return err })
		return nil
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

type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time {
	return time.Now()
}

var clock Clock = systemClock{}

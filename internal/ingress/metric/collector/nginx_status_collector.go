/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package collector

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"regexp"
	"strconv"

	"github.com/golang/glog"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	ac      = regexp.MustCompile(`Active connections: (\d+)`)
	sahr    = regexp.MustCompile(`(\d+)\s(\d+)\s(\d+)`)
	reading = regexp.MustCompile(`Reading: (\d+)`)
	writing = regexp.MustCompile(`Writing: (\d+)`)
	waiting = regexp.MustCompile(`Waiting: (\d+)`)
)

type (
	nginxStatusCollector struct {
		scrapeChan     chan scrapeRequest
		ngxHealthPort  int
		ngxStatusPath  string
		data           *nginxStatusData
		watchNamespace string
		ingressClass   string
	}

	nginxStatusData struct {
		connectionsTotal *prometheus.Desc
		requestsTotal    *prometheus.Desc
		connections      *prometheus.Desc
	}

	basicStatus struct {
		// Active total number of active connections
		Active int
		// Accepted total number of accepted client connections
		Accepted int
		// Handled total number of handled connections. Generally, the parameter value is the same as accepts unless some resource limits have been reached (for example, the worker_connections limit).
		Handled int
		// Requests total number of client requests.
		Requests int
		// Reading current number of connections where nginx is reading the request header.
		Reading int
		// Writing current number of connections where nginx is writing the response back to the client.
		Writing int
		// Waiting current number of idle client connections waiting for a request.
		Waiting int
	}
)

// InitNGINXStatusCollector returns a new prometheus collector the default nginx status module
func InitNGINXStatusCollector(watchNamespace, ingressClass string, ngxHealthPort int) error {
	const ns string = "nginx"
	const ngxStatusPath = "/nginx_status"
	p := nginxStatusCollector{
		scrapeChan:     make(chan scrapeRequest),
		ngxHealthPort:  ngxHealthPort,
		ngxStatusPath:  ngxStatusPath,
		watchNamespace: watchNamespace,
		ingressClass:   ingressClass,
	}

	p.data = &nginxStatusData{
		connectionsTotal: prometheus.NewDesc(
			prometheus.BuildFQName(ns, "", "connections_total"),
			"total number of connections with state {active, accepted, handled}",
			[]string{"ingress_class", "namespace", "state"}, nil),

		requestsTotal: prometheus.NewDesc(
			prometheus.BuildFQName(ns, "", "requests_total"),
			"total number of client requests",
			[]string{"ingress_class", "namespace"}, nil),

		connections: prometheus.NewDesc(
			prometheus.BuildFQName(ns, "", "connections"),
			"current number of client connections with state {reading, writing, waiting}",
			[]string{"ingress_class", "namespace", "state"}, nil),
	}

	err := prometheus.Register(p)

	if err != nil {
		return fmt.Errorf("error while registering nginx status collector : %v", err)
	}

	go p.Run()

	return nil
}

// Describe implements prometheus.Collector.
func (p nginxStatusCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- p.data.connectionsTotal
	ch <- p.data.requestsTotal
	ch <- p.data.connections
}

// Collect implements prometheus.Collector.
func (p nginxStatusCollector) Collect(ch chan<- prometheus.Metric) {
	req := scrapeRequest{results: ch, done: make(chan struct{})}
	p.scrapeChan <- req
	<-req.done
}

func (p nginxStatusCollector) Run() {
	for req := range p.scrapeChan {
		ch := req.results
		p.scrape(ch)
		req.done <- struct{}{}
	}
}

func (p nginxStatusCollector) Stop() {
	close(p.scrapeChan)
}

func httpBody(url string) ([]byte, error) {
	resp, err := http.DefaultClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("unexpected error scraping nginx : %v", err)
	}

	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("unexpected error scraping nginx (%v)", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return nil, fmt.Errorf("unexpected error scraping nginx (status %v)", resp.StatusCode)
	}

	return data, nil
}

func toInt(data []string, pos int) int {
	if len(data) == 0 {
		return 0
	}
	if pos > len(data) {
		return 0
	}
	if v, err := strconv.Atoi(data[pos]); err == nil {
		return v
	}
	return 0
}

func parse(data string) *basicStatus {
	acr := ac.FindStringSubmatch(data)
	sahrr := sahr.FindStringSubmatch(data)
	readingr := reading.FindStringSubmatch(data)
	writingr := writing.FindStringSubmatch(data)
	waitingr := waiting.FindStringSubmatch(data)

	return &basicStatus{
		toInt(acr, 1),
		toInt(sahrr, 1),
		toInt(sahrr, 2),
		toInt(sahrr, 3),
		toInt(readingr, 1),
		toInt(writingr, 1),
		toInt(waitingr, 1),
	}
}

func getNginxStatus(port int, path string) (*basicStatus, error) {
	url := fmt.Sprintf("http://0.0.0.0:%v%v", port, path)
	glog.V(3).Infof("start scraping url: %v", url)

	data, err := httpBody(url)

	if err != nil {
		return nil, fmt.Errorf("unexpected error scraping nginx status page: %v", err)
	}

	return parse(string(data)), nil
}

// nginxStatusCollector scrape the nginx status
func (p nginxStatusCollector) scrape(ch chan<- prometheus.Metric) {
	s, err := getNginxStatus(p.ngxHealthPort, p.ngxStatusPath)
	if err != nil {
		glog.Warningf("unexpected error obtaining nginx status info: %v", err)
		return
	}

	ch <- prometheus.MustNewConstMetric(p.data.connectionsTotal,
		prometheus.CounterValue, float64(s.Active), p.ingressClass, p.watchNamespace, "active")
	ch <- prometheus.MustNewConstMetric(p.data.connectionsTotal,
		prometheus.CounterValue, float64(s.Accepted), p.ingressClass, p.watchNamespace, "accepted")
	ch <- prometheus.MustNewConstMetric(p.data.connectionsTotal,
		prometheus.CounterValue, float64(s.Handled), p.ingressClass, p.watchNamespace, "handled")
	ch <- prometheus.MustNewConstMetric(p.data.requestsTotal,
		prometheus.CounterValue, float64(s.Requests), p.ingressClass, p.watchNamespace)
	ch <- prometheus.MustNewConstMetric(p.data.connections,
		prometheus.GaugeValue, float64(s.Reading), p.ingressClass, p.watchNamespace, "reading")
	ch <- prometheus.MustNewConstMetric(p.data.connections,
		prometheus.GaugeValue, float64(s.Writing), p.ingressClass, p.watchNamespace, "writing")
	ch <- prometheus.MustNewConstMetric(p.data.connections,
		prometheus.GaugeValue, float64(s.Waiting), p.ingressClass, p.watchNamespace, "waiting")
}

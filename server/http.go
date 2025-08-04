package server

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"

	sreCommon "github.com/devopsext/sre/common"
	"github.com/devopsext/utils"
	"github.com/devopsext/webrender/common"
	"github.com/devopsext/webrender/processor"
)

type HttpServerOptions struct {
	HealthcheckURL string
	ImageURL       string

	ServerName string
	Listen     string
	Tls        bool
	Insecure   bool
	Cert       string
	Key        string
	Chain      string
}

type HttpServer struct {
	options    HttpServerOptions
	processors *common.Processors
	logger     sreCommon.Logger
	meter      sreCommon.Meter
}

type HttpProcessHandleFunc = func(w http.ResponseWriter, r *http.Request)

func (h *HttpServer) processURL(url string, mux *http.ServeMux, p common.HttpProcessor) {

	urls := strings.Split(url, ",")
	for _, url := range urls {

		labels := make(sreCommon.Labels)
		labels["url"] = url

		requests := h.meter.Counter("", "requests", "Count of all http server requests", labels, "http", "server")
		errors := h.meter.Counter("", "errors", "Count of all server input errors", labels, "http", "server")

		mux.HandleFunc(url, func(w http.ResponseWriter, r *http.Request) {

			requests.Inc()
			err := p.HandleHttpRequest(w, r)
			if err != nil {
				errors.Inc()
			}
		})
	}
}

func (h *HttpServer) Start(wg *sync.WaitGroup) {

	wg.Add(1)
	go func(wg *sync.WaitGroup) {

		defer wg.Done()
		h.logger.Info("Start http server...")

		var caPool *x509.CertPool
		var certificates []tls.Certificate

		if h.options.Tls {

			// load certififcate
			var cert []byte
			if _, err := os.Stat(h.options.Cert); err == nil {

				cert, err = os.ReadFile(h.options.Cert)
				if err != nil {
					h.logger.Panic(err)
				}
			} else {
				cert = []byte(h.options.Cert)
			}

			// load key
			var key []byte
			if _, err := os.Stat(h.options.Key); err == nil {
				key, err = os.ReadFile(h.options.Key)
				if err != nil {
					h.logger.Panic(err)
				}
			} else {
				key = []byte(h.options.Key)
			}

			// make pair from certificate and pair
			pair, err := tls.X509KeyPair(cert, key)
			if err != nil {
				h.logger.Panic(err)
			}

			certificates = append(certificates, pair)

			// load CA chain
			var chain []byte
			if _, err := os.Stat(h.options.Chain); err == nil {
				chain, err = os.ReadFile(h.options.Chain)
				if err != nil {
					h.logger.Panic(err)
				}
			} else {
				chain = []byte(h.options.Chain)
			}

			// make pool of chains
			caPool = x509.NewCertPool()
			if !caPool.AppendCertsFromPEM(chain) {
				h.logger.Debug("CA chain is invalid")
			}
		}

		mux := http.NewServeMux()
		if !utils.IsEmpty(h.options.HealthcheckURL) {
			mux.HandleFunc(h.options.HealthcheckURL, func(w http.ResponseWriter, r *http.Request) {
				if _, err := w.Write([]byte("OK")); err != nil {
					h.logger.Error("Can't write response: %v", err)
					http.Error(w, fmt.Sprintf("could not write response: %v", err), http.StatusInternalServerError)
				}
			})
		}

		processors := h.getProcessors()
		for u, p := range processors {
			h.processURL(u, mux, p)
		}

		listener, err := net.Listen("tcp", h.options.Listen)
		if err != nil {
			h.logger.Panic(err)
		}

		h.logger.Info("Http server is up. Listening...")

		srv := &http.Server{
			Handler:  mux,
			ErrorLog: nil,
		}

		if h.options.Tls {

			srv.TLSConfig = &tls.Config{
				Certificates:       certificates,
				RootCAs:            caPool,
				InsecureSkipVerify: h.options.Insecure,
				ServerName:         h.options.ServerName,
			}

			err = srv.ServeTLS(listener, "", "")
			if err != nil {
				h.logger.Panic(err)
			}
		} else {
			err = srv.Serve(listener)
			if err != nil {
				h.logger.Panic(err)
			}
		}
	}(wg)
}

func (h *HttpServer) setProcessor(m map[string]common.HttpProcessor, url string, t string) {

	if !utils.IsEmpty(url) {
		p := h.processors.FindHttpProcessor(t)
		if p != nil {
			m[url] = p
		}
	}
}

func (h *HttpServer) getProcessors() map[string]common.HttpProcessor {

	m := make(map[string]common.HttpProcessor)
	h.setProcessor(m, h.options.ImageURL, processor.ImageProcessorType())
	return m
}

func NewHttpServer(options HttpServerOptions, processors *common.Processors, observability *common.Observability) *HttpServer {

	meter := observability.Metrics()

	return &HttpServer{
		options:    options,
		processors: processors,
		logger:     observability.Logs(),
		meter:      meter,
	}
}

package processor

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-playground/form"

	sreCommon "github.com/devopsext/sre/common"
	"github.com/devopsext/utils"
	"github.com/devopsext/webrender/browser"
	"github.com/devopsext/webrender/common"
)

type ImageProcessorRequest struct {
	URL       string                 `form:"url"`
	Kind      string                 `form:"kind,omitempty"`
	Width     int                    `form:"width,omitempty"`
	Height    int                    `form:"height,omitempty"`
	UserAgent string                 `form:"userAgent,omitempty"`
	Timeout   int                    `form:"timeout,omitempty"`
	Delay     int                    `form:"delay,omitempty"`
	AsPDF     bool                   `form:"asPDF,omitempty"`
	Headers   map[string]interface{} `form:"headers,omitempty"`
}

type ImageProcessorOptions struct {
	Width       int
	Height      int
	UserAgent   string
	Timeout     int
	Delay       int
	BrowserPath string
	BrowserKind string
	AsPDF       bool
}

type ImageProcessor struct {
	options       ImageProcessorOptions
	observability *common.Observability
	logger        sreCommon.Logger
	meter         sreCommon.Meter
}

func ImageProcessorType() string {
	return "Image"
}

func (p *ImageProcessor) Type() string {
	return ImageProcessorType()
}

func (p *ImageProcessor) chromeImage(r *ImageProcessorRequest) ([]byte, error) {

	width := r.Width
	if width == 0 {
		width = p.options.Width
	}

	height := r.Height
	if height == 0 {
		height = p.options.Height
	}

	userAgent := r.UserAgent
	if utils.IsEmpty(userAgent) {
		userAgent = p.options.UserAgent
	}

	timeout := r.Timeout
	if timeout == 0 {
		timeout = p.options.Timeout
	}

	delay := r.Delay
	if delay == 0 {
		delay = p.options.Delay
	}

	options := browser.ChromeBrowserOptions{
		Width:      width,
		Height:     height,
		Path:       p.options.BrowserPath,
		UserAgent:  userAgent,
		Timeout:    timeout,
		Delay:      delay,
		FullPage:   true,
		AsPDF:      r.AsPDF,
		HeadersMap: r.Headers,
	}
	chrome := browser.NewChromeBrowser(options, p.observability)

	u, err := url.Parse(r.URL)
	if err != nil {
		return nil, err
	}

	image, err := chrome.Image(u)
	if err != nil {
		return nil, err
	}

	return image.Data, nil
}

func (p *ImageProcessor) HandleHttpRequest(w http.ResponseWriter, r *http.Request) error {

	channel := strings.TrimLeft(r.URL.Path, "/")

	labels := make(sreCommon.Labels)
	labels["channel"] = channel

	requests := p.meter.Counter("", "requests", "Count of all google processor requests", labels, "google", "processor")
	errs := p.meter.Counter("", "errors", "Count of all google processor errors", labels, "google", "processor")

	requests.Inc()

	err := r.ParseForm()
	if err != nil {
		errs.Inc()
		http.Error(w, fmt.Sprintf("could not parse form: %v", err), http.StatusInternalServerError)
		return err
	}

	decoder := form.NewDecoder()

	var request ImageProcessorRequest
	err = decoder.Decode(&request, r.Form)
	if err != nil {
		errs.Inc()
		http.Error(w, fmt.Sprintf("could not decode form: %v", err), http.StatusInternalServerError)
		return err
	}

	browser := request.Kind
	if utils.IsEmpty(browser) {
		browser = "chrome"
	}

	var data []byte

	switch browser {
	default:
		data, err = p.chromeImage(&request)
	}

	if err != nil {
		errs.Inc()
		http.Error(w, fmt.Sprintf("could not make image: %v", err), http.StatusInternalServerError)
		return err
	}

	if _, err := w.Write(data); err != nil {
		errs.Inc()
		http.Error(w, fmt.Sprintf("could not write response: %v", err), http.StatusInternalServerError)
		return err
	}
	return nil
}

func NewImageProcessor(options ImageProcessorOptions, observability *common.Observability) *ImageProcessor {

	return &ImageProcessor{
		options:       options,
		observability: observability,
		logger:        observability.Logs(),
		meter:         observability.Metrics(),
	}
}

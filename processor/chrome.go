package processor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/chromedp/cdproto/inspector"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	sreCommon "github.com/devopsext/sre/common"
	"github.com/devopsext/webrender/common"
)

type ChromeRequest struct {
}

type ChromeScreenshot struct {
	data []byte
	dom  string
}

type ChromeProcessorOptions struct {
	Width      int
	Height     int
	UserAgent  string
	JsCode     string
	Timeout    int
	Delay      int
	FullPage   bool
	Path       string
	Proxy      string
	Headers    []string
	HeadersMap map[string]interface{}

	// http codes to screenshot (used as a filter)
	ScreenshotCodes []int

	// save screenies as PDF's instead
	AsPDF bool
}

type ChromeProcessor struct {
	options ChromeProcessorOptions
	logger  sreCommon.Logger
	meter   sreCommon.Meter
}

func ChromeProcessorType() string {
	return "Chrome"
}

func (p *ChromeProcessor) Type() string {
	return ChromeProcessorType()
}

// buildTasks builds the chromedp tasks slice
func (p *ChromeProcessor) buildTasks(url *url.URL, doNavigate bool, buf *[]byte, dom *string) chromedp.Tasks {
	var actions chromedp.Tasks

	if len(p.options.HeadersMap) > 0 {
		actions = append(actions, network.Enable(), network.SetExtraHTTPHeaders(network.Headers(p.options.HeadersMap)))
	}

	if doNavigate {
		actions = append(actions, chromedp.Navigate(url.String()))
		if len(p.options.JsCode) > 0 {
			actions = append(actions, chromedp.Evaluate(p.options.JsCode, nil))
		}
		if p.options.Delay > 0 {
			actions = append(actions, chromedp.Sleep(time.Duration(p.options.Delay)*time.Second))
		}
		actions = append(actions, chromedp.Stop())
	}

	// grab the dom
	actions = append(actions, chromedp.OuterHTML(":root", dom, chromedp.ByQueryAll))

	// should we print as pdf?
	if p.options.AsPDF {
		actions = append(actions, chromedp.ActionFunc(func(ctx context.Context) error {
			var err error
			*buf, _, err = page.PrintToPDF().
				WithDisplayHeaderFooter(true).
				Do(ctx)
			return err
		}))

		return actions
	}

	// otherwise screenshot as png
	if p.options.FullPage {
		actions = append(actions, chromedp.FullScreenshot(buf, 100))
	} else {
		actions = append(actions, chromedp.CaptureScreenshot(buf))
	}

	return actions
}

// https://github.com/chromedp/examples/blob/255873ca0d76b00e0af8a951a689df3eb4f224c3/screenshot/main.go
func (p *ChromeProcessor) Screenshot(url *url.URL) (*ChromeScreenshot, error) {

	r := &ChromeScreenshot{}

	// setup chromedp default options
	options := []chromedp.ExecAllocatorOption{}
	options = append(options, chromedp.DefaultExecAllocatorOptions[:]...)
	options = append(options, chromedp.UserAgent(p.options.UserAgent))
	options = append(options, chromedp.DisableGPU)
	options = append(options, chromedp.Flag("ignore-certificate-errors", true)) // RIP shittyproxy.go
	options = append(options, chromedp.WindowSize(p.options.Width, p.options.Height))

	if p.options.Path != "" {
		options = append(options, chromedp.ExecPath(p.options.Path))
	}

	if p.options.Proxy != "" {
		options = append(options, chromedp.ProxyServer(p.options.Proxy))
	}

	actx, acancel := chromedp.NewExecAllocator(context.Background(), options...)
	defer acancel()
	browserCtx, cancelBrowserCtx := chromedp.NewContext(actx)
	defer cancelBrowserCtx()

	// create the initial context to act as the 'tab', where we will perform the initial navigation
	// if this context loads successfully, then the screenshot will have been captured
	//
	//		Note:	You're not supposed to delay the initial run context, so we use WithTimeout
	//				 https://pkg.go.dev/github.com/chromedp/chromedp#Run

	tabCtx, cancelTabCtx := context.WithTimeout(browserCtx, time.Duration(p.options.Timeout)*time.Second)
	defer cancelTabCtx()

	// Run the initial browser
	if err := chromedp.Run(browserCtx); err != nil {
		return nil, err
	}

	// prevent browser crashes from locking the context (prevents hanging)
	chromedp.ListenTarget(browserCtx, func(ev interface{}) {
		if _, ok := ev.(*inspector.EventTargetCrashed); ok {
			cancelBrowserCtx()
		}
	})

	chromedp.ListenTarget(tabCtx, func(ev interface{}) {
		if _, ok := ev.(*inspector.EventTargetCrashed); ok {
			cancelTabCtx()
		}
	})

	// squash JavaScript dialog boxes such as alert();
	chromedp.ListenTarget(tabCtx, func(ev interface{}) {
		if _, ok := ev.(*page.EventJavascriptDialogOpening); ok {
			go func() {
				if err := chromedp.Run(tabCtx,
					page.HandleJavaScriptDialog(true),
				); err != nil {
					cancelTabCtx()
				}
			}()
		}
	})

	// log console.* events, as well as any thrown exceptions
	chromedp.ListenTarget(tabCtx, func(ev interface{}) {
		switch ev := ev.(type) {
		case *runtime.EventConsoleAPICalled:

			// use a buffer to read each arg passed to the console.* call

		case *runtime.EventExceptionThrown:
		default:
			p.logger.Debug("%v", ev)
		}
	})

	// keep a keyed reference so we can map network logs to requestid's and
	// update them as responses are received

	// log network events
	chromedp.ListenTarget(tabCtx, func(ev interface{}) {
		switch ev := ev.(type) {
		// http
		case *network.EventRequestWillBeSent:
			// record a fresh request that will be sent
		case *network.EventResponseReceived:
			// update the networkLog map with updated information about response
		case *network.EventLoadingFailed:
			// update the network map with the error experienced
		// websockets
		case *network.EventWebSocketCreated:
		case *network.EventWebSocketHandshakeResponseReceived:
		case *network.EventWebSocketFrameError:
		default:
			p.logger.Debug("%v", ev)
		}
	})

	// perform navigation on the tab context and attempt to take a clean screenshot
	err := chromedp.Run(tabCtx, p.buildTasks(url, true, &r.data, &r.dom))

	if errors.Is(err, context.DeadlineExceeded) {
		// if the context timeout exceeded (e.g. on a long page load) then
		// just take the screenshot this will take a screenshot of whatever
		// loaded before failing

		// create a new tab context for this scenario, since our previous
		// context expired using a context timeout delay again to help
		// prevent hanging scenarios
		newTabCtx, cancelNewTabCtx := context.WithTimeout(browserCtx, time.Duration(p.options.Timeout)*time.Second)
		defer cancelNewTabCtx()

		// listen for crashes on this backup context as well
		chromedp.ListenTarget(newTabCtx, func(ev interface{}) {
			if _, ok := ev.(*inspector.EventTargetCrashed); ok {
				cancelNewTabCtx()
			}
		})

		// attempt to capture the screenshot of the tab and replace error accordingly
		err = chromedp.Run(newTabCtx, p.buildTasks(url, false, &r.data, &r.dom))
	}

	if err != nil {
		return nil, err
	}

	// close the tab so that we dont receive more network events
	cancelTabCtx()
	return r, nil
}

func (p *ChromeProcessor) HandleHttpRequest(w http.ResponseWriter, r *http.Request) error {

	channel := strings.TrimLeft(r.URL.Path, "/")

	labels := make(sreCommon.Labels)
	labels["channel"] = channel

	requests := p.meter.Counter("requests", "Count of all google processor requests", labels, "google", "processor")
	errs := p.meter.Counter("errors", "Count of all google processor errors", labels, "google", "processor")

	requests.Inc()

	/*var body []byte
	if r.Body != nil {
		if data, err := io.ReadAll(r.Body); err == nil {
			body = data
		}
	}

	/*if len(body) == 0 {
		errs.Inc()
		err := errors.New("empty body")
		http.Error(w, err.Error(), http.StatusBadRequest)
		return err
	}

	/*var request ChromeRequest
	if err := json.Unmarshal(body, &request); err != nil {
		errs.Inc()
		http.Error(w, "Error unmarshaling message", http.StatusInternalServerError)
		return err
	}*/

	u, err := url.Parse("https://google.com")
	if err != nil {
		errs.Inc()
		http.Error(w, fmt.Sprintf("could not parse URL: %v", err), http.StatusInternalServerError)
		return err
	}

	ss, err := p.Screenshot(u)
	if err != nil {
		errs.Inc()
		http.Error(w, fmt.Sprintf("could not make screenshot: %v", err), http.StatusInternalServerError)
		return err
	}

	if _, err := w.Write(ss.data); err != nil {
		errs.Inc()
		http.Error(w, fmt.Sprintf("could not write response: %v", err), http.StatusInternalServerError)
		return err
	}
	return nil
}

func NewChromeProcessor(options ChromeProcessorOptions, observability *common.Observability) *ChromeProcessor {

	return &ChromeProcessor{
		options: options,
		logger:  observability.Logs(),
		meter:   observability.Metrics(),
	}
}

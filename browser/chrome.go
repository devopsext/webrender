package browser

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"net/url"
	"time"

	"github.com/chromedp/cdproto/inspector"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	sreCommon "github.com/devopsext/sre/common"
	"github.com/devopsext/webrender/common"
)

type ChromeBrowserImage struct {
	Data []byte
	DOM  string
}

type ChromeBrowserOptions struct {
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
	AsPDF           bool

	// Image cropping parameters
	OffsetX    int // X offset for cropping
	OffsetY    int // Y offset for cropping (to skip header)
	CropWidth  int // Cropped width
	CropHeight int // Cropped height
}

type ChromeBrowser struct {
	options ChromeBrowserOptions
	logger  sreCommon.Logger
	meter   sreCommon.Meter
}

// cropImage crops the image based on the provided offset and dimensions
func (c *ChromeBrowser) cropImage(imageData []byte) ([]byte, error) {
	if c.options.OffsetX == 0 && c.options.OffsetY == 0 && c.options.CropWidth == 0 && c.options.CropHeight == 0 {
		return imageData, nil
	}

	img, _, err := image.Decode(bytes.NewReader(imageData))
	if err != nil {
		return nil, err
	}

	bounds := img.Bounds()

	x0 := c.options.OffsetX
	y0 := c.options.OffsetY
	x1 := bounds.Max.X
	y1 := bounds.Max.Y

	if c.options.CropWidth > 0 {
		x1 = x0 + c.options.CropWidth
	}
	if c.options.CropHeight > 0 {
		y1 = y0 + c.options.CropHeight
	}

	if x0 < bounds.Min.X {
		x0 = bounds.Min.X
	}
	if y0 < bounds.Min.Y {
		y0 = bounds.Min.Y
	}
	if x1 > bounds.Max.X {
		x1 = bounds.Max.X
	}
	if y1 > bounds.Max.Y {
		y1 = bounds.Max.Y
	}

	cropRect := image.Rect(x0, y0, x1, y1)
	croppedImg := img.(interface {
		SubImage(r image.Rectangle) image.Image
	}).SubImage(cropRect)

	// encode the cropped image back to PNG
	var buf bytes.Buffer
	err = png.Encode(&buf, croppedImg)
	if err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// buildTasks builds the chromedp tasks slice
func (c *ChromeBrowser) buildTasks(url *url.URL, doNavigate bool, buf *[]byte, dom *string) chromedp.Tasks {
	var actions chromedp.Tasks

	if len(c.options.HeadersMap) > 0 {
		actions = append(actions, network.Enable(), network.SetExtraHTTPHeaders(network.Headers(c.options.HeadersMap)))
	}

	if doNavigate {
		actions = append(actions, chromedp.Navigate(url.String()))
		if len(c.options.JsCode) > 0 {
			actions = append(actions, chromedp.Evaluate(c.options.JsCode, nil))
		}
		if c.options.Delay > 0 {
			actions = append(actions, chromedp.Sleep(time.Duration(c.options.Delay)*time.Second))
		}
		actions = append(actions, chromedp.Stop())
	}

	// grab the dom
	actions = append(actions, chromedp.OuterHTML(":root", dom, chromedp.ByQueryAll))

	// should we print as pdf?
	if c.options.AsPDF {
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
	if c.options.FullPage {
		actions = append(actions, chromedp.FullScreenshot(buf, 100))
	} else {
		actions = append(actions, chromedp.CaptureScreenshot(buf))
	}

	return actions
}

// https://github.com/chromedp/examples/blob/255873ca0d76b00e0af8a951a689df3eb4f224c3/screenshot/main.go
func (c *ChromeBrowser) Image(url *url.URL) (*ChromeBrowserImage, error) {

	r := &ChromeBrowserImage{}

	// setup chromedp default options
	options := []chromedp.ExecAllocatorOption{}
	options = append(options, chromedp.DefaultExecAllocatorOptions[:]...)
	options = append(options, chromedp.UserAgent(c.options.UserAgent))
	options = append(options, chromedp.DisableGPU)
	options = append(options, chromedp.Flag("ignore-certificate-errors", true)) // RIP shittyproxy.go
	options = append(options, chromedp.WindowSize(c.options.Width, c.options.Height))

	if c.options.Path != "" {
		options = append(options, chromedp.ExecPath(c.options.Path))
	}

	if c.options.Proxy != "" {
		options = append(options, chromedp.ProxyServer(c.options.Proxy))
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

	tabCtx, cancelTabCtx := context.WithTimeout(browserCtx, time.Duration(c.options.Timeout)*time.Second)
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
			c.logger.Debug("%v", ev)

		case *runtime.EventExceptionThrown:
		default:
			//c.logger.Debug("%v", ev)
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
			c.logger.Debug("%v", ev)
		case *network.EventResponseReceived:
			// update the networkLog map with updated information about response
			c.logger.Debug("%v", ev)
		case *network.EventLoadingFailed:
			// update the network map with the error experienced
			c.logger.Debug("%v", ev)
		// websockets
		case *network.EventWebSocketCreated:
		case *network.EventWebSocketHandshakeResponseReceived:
		case *network.EventWebSocketFrameError:
		default:
			// c.logger.Debug("%v", ev)
		}
	})

	// perform navigation on the tab context and attempt to take a clean screenshot
	err := chromedp.Run(tabCtx, c.buildTasks(url, true, &r.Data, &r.DOM))

	if errors.Is(err, context.DeadlineExceeded) {
		// if the context timeout exceeded (e.g. on a long page load) then
		// just take the screenshot this will take a screenshot of whatever
		// loaded before failing

		// create a new tab context for this scenario, since our previous
		// context expired using a context timeout delay again to help
		// prevent hanging scenarios
		newTabCtx, cancelNewTabCtx := context.WithTimeout(browserCtx, time.Duration(c.options.Timeout)*time.Second)
		defer cancelNewTabCtx()

		// listen for crashes on this backup context as well
		chromedp.ListenTarget(newTabCtx, func(ev interface{}) {
			if _, ok := ev.(*inspector.EventTargetCrashed); ok {
				cancelNewTabCtx()
			}
		})

		// attempt to capture the screenshot of the tab and replace error accordingly
		err = chromedp.Run(newTabCtx, c.buildTasks(url, false, &r.Data, &r.DOM))
	}

	if err != nil {
		return nil, err
	}

	if !c.options.AsPDF {
		croppedData, err := c.cropImage(r.Data)
		if err != nil {
			c.logger.Debug("Failed to crop image: %v", err)
			// Continue with original image if cropping fails
		} else {
			r.Data = croppedData
		}
	}

	// close the tab so that we dont receive more network events
	cancelTabCtx()
	return r, nil
}

func NewChromeBrowser(options ChromeBrowserOptions, observability *common.Observability) *ChromeBrowser {

	return &ChromeBrowser{
		options: options,
		logger:  observability.Logs(),
		meter:   observability.Metrics(),
	}
}

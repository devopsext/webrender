package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"sync"
	"syscall"

	sreCommon "github.com/devopsext/sre/common"
	sreProvider "github.com/devopsext/sre/provider"
	utils "github.com/devopsext/utils"
	"github.com/devopsext/webrender/common"
	"github.com/devopsext/webrender/processor"
	"github.com/devopsext/webrender/server"
	"github.com/spf13/cobra"
)

var version = "unknown"
var APPNAME = "WEBRENDER"
var appName = strings.ToLower(APPNAME)

var logs = sreCommon.NewLogs()
var metrics = sreCommon.NewMetrics()
var stdout *sreProvider.Stdout
var mainWG sync.WaitGroup

type RootOptions struct {
	Logs    []string
	Metrics []string
}

var rootOptions = RootOptions{
	Logs:    strings.Split(envGet("LOGS", "stdout").(string), ","),
	Metrics: strings.Split(envGet("METRICS", "prometheus").(string), ","),
}

var stdoutOptions = sreProvider.StdoutOptions{
	Format:          envGet("STDOUT_FORMAT", "text").(string),
	Level:           envGet("STDOUT_LEVEL", "info").(string),
	Template:        envGet("STDOUT_TEMPLATE", "{{.file}} {{.msg}}").(string),
	TimestampFormat: envGet("STDOUT_TIMESTAMP_FORMAT", time.RFC3339Nano).(string),
	TextColors:      envGet("STDOUT_TEXT_COLORS", true).(bool),
}

var prometheusOptions = sreProvider.PrometheusOptions{
	URL:    envGet("PROMETHEUS_METRICS_URL", "/metrics").(string),
	Listen: envGet("PROMETHEUS_METRICS_LISTEN", "127.0.0.1:8080").(string),
	Prefix: envGet("PROMETHEUS_METRICS_PREFIX", appName).(string),
}

var httpServerOptions = server.HttpServerOptions{
	HealthcheckURL: envGet("HTTP_HEALTHCHECK_URL", "/healthcheck").(string),
	ChromeURL:      envGet("HTTP_CHROME_URL", "/chrome").(string),
	ServerName:     envGet("HTTP_SERVER_NAME", "").(string),
	Listen:         envGet("HTTP_LISTEN", ":80").(string),
	Tls:            envGet("HTTP_TLS", false).(bool),
	Insecure:       envGet("HTTP_INSECURE", false).(bool),
	Cert:           envGet("HTTP_CERT", "").(string),
	Key:            envGet("HTTP_KEY", "").(string),
	Chain:          envGet("HTTP_CHAIN", "").(string),
}

var chromeProcessorOptions = processor.ChromeProcessorOptions{
	Path:      envGet("CHROME_PATH", "").(string),
	Width:     envGet("CHROME_WIDTH", 1920).(int),
	Height:    envGet("CHROME_HEIGHT", 1280).(int),
	Timeout:   envGet("CHROME_TIMEOUT", 10).(int),
	Delay:     envGet("CHROME_DELAY", 3).(int),
	UserAgent: envGet("CHROME_USER_AGENT", appName).(string),
}

func getOnlyEnv(key string) string {
	value, ok := os.LookupEnv(key)
	if ok {
		return value
	}
	return fmt.Sprintf("$%s", key)
}

func envGet(s string, def interface{}) interface{} {
	return utils.EnvGet(fmt.Sprintf("%s_%s", APPNAME, s), def)
}

func envStringExpand(s string, def string) string {
	snew := envGet(s, def).(string)
	return os.Expand(snew, getOnlyEnv)
}

func envFileContentExpand(s string, def string) string {
	snew := envGet(s, def).(string)
	bytes, err := utils.Content(snew)
	if err != nil {
		return def
	}
	return os.Expand(string(bytes), getOnlyEnv)
}

func interceptSyscall() {

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)
	go func() {
		<-c
		logs.Info("Exiting...")
		os.Exit(1)
	}()
}

func Execute() {

	rootCmd := &cobra.Command{
		Use:   "webrender",
		Short: "WebRender",
		PersistentPreRun: func(cmd *cobra.Command, args []string) {

			stdoutOptions.Version = version
			stdout = sreProvider.NewStdout(stdoutOptions)
			if utils.Contains(rootOptions.Logs, "stdout") && stdout != nil {
				stdout.SetCallerOffset(2)
				logs.Register(stdout)
			}

			logs.Info("Booting...")

			// Metrics

			prometheusOptions.Version = version
			prometheus := sreProvider.NewPrometheusMeter(prometheusOptions, logs, stdout)
			if utils.Contains(rootOptions.Metrics, "prometheus") && prometheus != nil {
				prometheus.StartInWaitGroup(&mainWG)
				metrics.Register(prometheus)
			}
		},
		Run: func(cmd *cobra.Command, args []string) {

			obs := common.NewObservability(logs, metrics)

			processors := common.NewProcessors()
			processors.Add(processor.NewChromeProcessor(chromeProcessorOptions, obs))

			servers := common.NewServers()
			servers.Add(server.NewHttpServer(httpServerOptions, processors, obs))
			servers.Start(&mainWG)
			mainWG.Wait()
		},
	}

	flags := rootCmd.PersistentFlags()

	flags.StringSliceVar(&rootOptions.Logs, "logs", rootOptions.Logs, "Log providers: stdout")
	flags.StringSliceVar(&rootOptions.Metrics, "metrics", rootOptions.Metrics, "Metric providers: prometheus")

	flags.StringVar(&stdoutOptions.Format, "stdout-format", stdoutOptions.Format, "Stdout format: json, text, template")
	flags.StringVar(&stdoutOptions.Level, "stdout-level", stdoutOptions.Level, "Stdout level: info, warn, error, debug, panic")
	flags.StringVar(&stdoutOptions.Template, "stdout-template", stdoutOptions.Template, "Stdout template")
	flags.StringVar(&stdoutOptions.TimestampFormat, "stdout-timestamp-format", stdoutOptions.TimestampFormat, "Stdout timestamp format")
	flags.BoolVar(&stdoutOptions.TextColors, "stdout-text-colors", stdoutOptions.TextColors, "Stdout text colors")
	flags.BoolVar(&stdoutOptions.Debug, "stdout-debug", stdoutOptions.Debug, "Stdout debug")

	flags.StringVar(&prometheusOptions.URL, "prometheus-url", prometheusOptions.URL, "Prometheus endpoint url")
	flags.StringVar(&prometheusOptions.Listen, "prometheus-listen", prometheusOptions.Listen, "Prometheus listen")
	flags.StringVar(&prometheusOptions.Prefix, "prometheus-prefix", prometheusOptions.Prefix, "Prometheus prefix")

	flags.StringVar(&httpServerOptions.HealthcheckURL, "http-healthcheck-url", httpServerOptions.HealthcheckURL, "Http healthcheck url")
	flags.StringVar(&httpServerOptions.ServerName, "http-server-name", httpServerOptions.ServerName, "Http server name")
	flags.StringVar(&httpServerOptions.Listen, "http-listen", httpServerOptions.Listen, "Http listen")
	flags.BoolVar(&httpServerOptions.Tls, "http-tls", httpServerOptions.Tls, "Http TLS")
	flags.BoolVar(&httpServerOptions.Insecure, "http-insecure", httpServerOptions.Insecure, "Http insecure skip verify")
	flags.StringVar(&httpServerOptions.Cert, "http-cert", httpServerOptions.Cert, "Http cert file or content")
	flags.StringVar(&httpServerOptions.Key, "http-key", httpServerOptions.Key, "Http key file or content")
	flags.StringVar(&httpServerOptions.Chain, "http-chain", httpServerOptions.Chain, "Http CA chain file or content")

	interceptSyscall()

	rootCmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the version number",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(version)
		},
	})

	if err := rootCmd.Execute(); err != nil {
		logs.Error(err)
		os.Exit(1)
	}
}

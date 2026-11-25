package core

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"server/internal/app/gui"
	"server/internal/app/logging"
	"server/internal/app/tui"
	"server/internal/domain/ingest"
	"server/internal/domain/signal"
	"server/internal/platform/tls"
	"server/internal/ports/httpapi"
	"server/internal/ports/tcp"
	"server/internal/ports/udp"
	"server/internal/ports/vrws"
)

var (
	addr         = flag.String("addr", ":5555", "Listen address (e.g. :5555)")
	httpAddr     = flag.String("http", ":8080", "HTTP API listen address")
	httpsAddr    = flag.String("https", ":443", "HTTPS listen address")
	tlsCert      = flag.String("tlsCert", "", "Path to TLS certificate")
	tlsKey       = flag.String("tlsKey", "", "Path to TLS private key")
	certDir      = flag.String("certDir", "certs", "Directory for auto-generated TLS cert")
	acmeWebroot  = flag.String("acmeWebroot", "", "Directory for ACME http-01")
	proto        = flag.String("proto", "udp", "Protocol: tcp or udp")
	gpu          = flag.Bool("gpu", true, "GPU mode (ebiten): fast rendering")
	timeWindow   = flag.Duration("time", 500*time.Millisecond, "Time window")
	adxlRate     = flag.Int("adxlRate", 4000, "ADXL sample rate (Hz)")
	adsRate      = flag.Int("adsRate", 40000, "ADS8681 sample rate (Hz)")
	refresh      = flag.Duration("refresh", 40*time.Millisecond, "Plot refresh interval")
	plotW        = flag.Int("width", 100, "Plot width in chars")
	plotH        = flag.Int("height", 6, "Plot height in chars")
	logLines     = flag.Int("loglines", 12, "Number of ESP32 log lines")
	glyphLog     = flag.Bool("glyph", false, "Write detailed glyph merge log")
	glyphFile    = flag.String("glyphFile", "", "Path to glyph merge log file")
	diagInterval = flag.Duration("diagInterval", 10*time.Second, "Diagnostic log interval (0 = disabled)")
	diagFile     = flag.String("diagFile", "", "Diagnostic log file path (empty = stderr)")
)

func Main() {
	flag.Parse()

	var glyphLogger *log.Logger
	if *glyphLog {
		path := *glyphFile
		if path == "" {
			path = fmt.Sprintf("glyph_merge_%s.log", time.Now().Format("20060102_150405"))
		}
		fh, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "glyph log file %s: %v\n", path, err)
			os.Exit(1)
		}
		glyphLogger = log.New(fh, "", log.LstdFlags|log.Lmicroseconds)
	}

	configStore := httpapi.NewConfigStore()
	bufs := signal.NewBuffers()

	// HTTP/HTTPS API & VR WS
	if *httpAddr != "" || *httpsAddr != "" {
		mux := http.NewServeMux()
		httpapi.RegisterHTTPAPI(mux, configStore, bufs)
		hub := vrws.NewHub()
		mux.HandleFunc("/ws", hub.ServeWS)

		if *acmeWebroot != "" {
			acmeChallengeDir := filepath.Join(*acmeWebroot, ".well-known", "acme-challenge")
			mux.Handle("/.well-known/acme-challenge/", http.StripPrefix("/.well-known/acme-challenge/", http.FileServer(http.Dir(acmeChallengeDir))))
		}

		if *httpsAddr != "" {
			cert, key := *tlsCert, *tlsKey
			if cert == "" || key == "" {
				cert, key, _, _, _ = tls.EnsureTLSCert(*certDir)
			}
			go http.ListenAndServeTLS(*httpsAddr, cert, key, mux)
		}
		if *httpAddr != "" {
			go http.ListenAndServe(*httpAddr, mux)
		}
	}

	ingestCfgFn := func() ingest.Config {
		p := configStore.GetProtocol()
		demod := p.Frequency
		if !p.ReceiverLinked {
			demod = p.ReceiverFreq
		}
		return ingest.Config{
			AdxlRate:    *adxlRate,
			AdsRate:     *adsRate,
			DemodFreqHz: demod,
			TimeWindow:  *timeWindow,
		}
	}

	// Network Ingest
	if *proto == "udp" {
		l := udp.NewListener(*addr, bufs, ingestCfgFn)
		l.TsfOffsetFn = configStore.GetTsfOffsetUs
		go func() {
			if err := l.Listen(); err != nil {
				log.Printf("[ingest/udp] listen error: %v", err)
			}
		}()
	} else {
		l := tcp.NewListener(*addr, bufs, ingestCfgFn)
		l.TsfOffsetFn = configStore.GetTsfOffsetUs
		go func() {
			if err := l.Listen(); err != nil {
				log.Printf("[ingest/tcp] listen error: %v", err)
			}
		}()
	}

	// Periodic diagnostics logger
	if *diagInterval > 0 {
		dl := logging.NewDiagLogger(bufs, *diagInterval, float64((*timeWindow).Milliseconds()), *diagFile)
		dl.TsfOffsetFn = configStore.GetTsfOffsetUs
		go dl.Run()
	}

	if *gpu {
		gui.Run(bufs, configStore, glyphLogger)
	} else {
		app := tui.NewApp(bufs, ingestCfgFn, *refresh, plotW, plotH, timeWindow, logLines)
		if err := app.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "TUI error: %v\n", err)
			os.Exit(1)
		}
	}
}

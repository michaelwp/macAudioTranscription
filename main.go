package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gen2brain/malgo"
	"golang.org/x/term"
)

const (
	sampleRate  = 16000
	numChannels = 1

	colorReset  = "\033[0m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorCyan   = "\033[36m"
	colorGray   = "\033[90m"
	colorRed    = "\033[31m"
	colorBold   = "\033[1m"
	clearLine   = "\r\033[K"
)

// ── display ───────────────────────────────────────────────────────────────────

type display struct {
	mu         sync.Mutex
	spinFrames []string
	spinIdx    int
	processing int
	level      float64 // current audio RMS level 0..1
	stopCh     chan struct{}
}

func newDisplay() *display {
	return &display{
		spinFrames: []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
		stopCh:     make(chan struct{}),
	}
}

func (d *display) printHeader(chunkSec int, outPath string) {
	w, _, _ := term.GetSize(int(os.Stdout.Fd()))
	if w <= 0 {
		w = 80
	}
	sep := strings.Repeat("─", w)
	fmt.Printf("%s%s Mac Audio Transcription%s\n", colorBold+colorCyan, "◉", colorReset)
	fmt.Printf("%s%s%s\n\n", colorGray, sep, colorReset)
	fmt.Printf("  Chunk size : %s%ds%s\n", colorCyan, chunkSec, colorReset)
	if outPath != "" {
		fmt.Printf("  Saving to  : %s%s%s\n", colorCyan, outPath, colorReset)
	}
	fmt.Println()
}

func levelBar(level float64, width int) string {
	filled := int(level * float64(width))
	if filled > width {
		filled = width
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	color := colorGreen
	if level > 0.7 {
		color = colorRed
	} else if level > 0.4 {
		color = colorYellow
	}
	return color + bar + colorReset
}

func (d *display) renderStatus() {
	spin := d.spinFrames[d.spinIdx%len(d.spinFrames)]
	bar := levelBar(d.level, 12)

	var extra string
	if d.processing > 0 {
		extra = fmt.Sprintf("  %s[transcribing %d…]%s", colorYellow, d.processing, colorReset)
	}

	fmt.Printf("%s%s%s %s  %s%s",
		clearLine, colorYellow, spin, colorReset,
		bar, extra,
	)
}

func (d *display) startSpinner() {
	d.renderStatus()
	go func() {
		t := time.NewTicker(80 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-d.stopCh:
				return
			case <-t.C:
				d.mu.Lock()
				d.spinIdx++
				d.renderStatus()
				d.mu.Unlock()
			}
		}
	}()
}

func (d *display) setLevel(v float64) {
	d.mu.Lock()
	d.level = v
	d.mu.Unlock()
}

func (d *display) setProcessing(n int) {
	d.mu.Lock()
	d.processing = n
	d.renderStatus()
	d.mu.Unlock()
}

func (d *display) printLine(text string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	ts := time.Now().Format("15:04:05")
	fmt.Printf("%s%s[%s]%s %s%s%s\n",
		clearLine,
		colorGray, ts, colorReset,
		colorGreen+colorBold, text, colorReset,
	)
	d.renderStatus()
}

func (d *display) errLine(msg string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	fmt.Printf("%s%s[error]%s %s\n", clearLine, colorRed, colorReset, msg)
	d.renderStatus()
}

func (d *display) stop() {
	close(d.stopCh)
	fmt.Printf("%s\n", clearLine)
}

// ── audio helpers ─────────────────────────────────────────────────────────────

func rms(samples []int16) float64 {
	if len(samples) == 0 {
		return 0
	}
	var sum float64
	for _, s := range samples {
		v := float64(s)
		sum += v * v
	}
	return math.Sqrt(sum / float64(len(samples)))
}

func writeWAV(w io.Writer, samples []int16) {
	dataSize := uint32(len(samples) * 2)
	binary.Write(w, binary.BigEndian, []byte("RIFF"))
	binary.Write(w, binary.LittleEndian, 36+dataSize)
	binary.Write(w, binary.BigEndian, []byte("WAVE"))
	binary.Write(w, binary.BigEndian, []byte("fmt "))
	binary.Write(w, binary.LittleEndian, uint32(16))
	binary.Write(w, binary.LittleEndian, uint16(1))
	binary.Write(w, binary.LittleEndian, uint16(numChannels))
	binary.Write(w, binary.LittleEndian, uint32(sampleRate))
	binary.Write(w, binary.LittleEndian, uint32(sampleRate*numChannels*2))
	binary.Write(w, binary.LittleEndian, uint16(numChannels*2))
	binary.Write(w, binary.LittleEndian, uint16(16))
	binary.Write(w, binary.BigEndian, []byte("data"))
	binary.Write(w, binary.LittleEndian, dataSize)
	binary.Write(w, binary.LittleEndian, samples)
}

// ── Whisper API ───────────────────────────────────────────────────────────────

func transcribeWAV(apiKey string, wavData []byte) (string, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", "audio.wav")
	if err != nil {
		return "", err
	}
	fw.Write(wavData)
	mw.WriteField("model", "whisper-1")
	mw.Close()

	req, err := http.NewRequest("POST", "https://api.openai.com/v1/audio/translations", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API %d: %s", resp.StatusCode, string(b))
	}

	var result struct {
		Text string `json:"text"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	return result.Text, nil
}

// ── file mode ─────────────────────────────────────────────────────────────────

func transcribeFile(apiKey, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading file: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Transcribing %s%s%s…\n", colorCyan, path, colorReset)
	text, err := transcribeWAV(apiKey, data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("\n%s%s%s\n", colorGreen+colorBold, text, colorReset)
}

// ── main ──────────────────────────────────────────────────────────────────────

func main() {
	apiKey := flag.String("key", os.Getenv("OPENAI_API_KEY"), "OpenAI API key (or set OPENAI_API_KEY)")
	chunkSec := flag.Int("chunk", 5, "Seconds of audio per transcription request")
	// silenceRMS: true RMS of int16 samples (0–32768). 200 ≈ near-silent room.
	silenceRMS := flag.Float64("silence", 200, "Skip chunks with RMS below this (0 = send everything)")
	outputFile := flag.String("out", "", "Optional file to append transcript lines to")
	fileFlag := flag.String("file", "", "Transcribe an existing WAV file instead of mic input")
	flag.Parse()

	if *apiKey == "" {
		fmt.Fprintln(os.Stderr, "Error: OpenAI API key required. Use -key flag or set OPENAI_API_KEY.")
		os.Exit(1)
	}

	if *fileFlag != "" {
		transcribeFile(*apiKey, *fileFlag)
		return
	}

	// ── mic mode ──────────────────────────────────────────────────────────────

	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to init audio context: %v\n", err)
		os.Exit(1)
	}
	defer ctx.Uninit()

	deviceConfig := malgo.DefaultDeviceConfig(malgo.Capture)
	deviceConfig.Capture.Format = malgo.FormatS16
	deviceConfig.Capture.Channels = uint32(numChannels)
	deviceConfig.SampleRate = uint32(sampleRate)

	var bufMu sync.Mutex
	var buffer []int16

	callbacks := malgo.DeviceCallbacks{
		Data: func(_, inputSamples []byte, _ uint32) {
			samples := make([]int16, len(inputSamples)/2)
			for i := range samples {
				samples[i] = int16(binary.LittleEndian.Uint16(inputSamples[i*2:]))
			}
			bufMu.Lock()
			buffer = append(buffer, samples...)
			bufMu.Unlock()
		},
	}

	device, err := malgo.InitDevice(ctx.Context, deviceConfig, callbacks)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to init audio device: %v\n", err)
		os.Exit(1)
	}
	defer device.Uninit()

	if err := device.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start audio device: %v\n", err)
		os.Exit(1)
	}
	defer device.Stop()

	var outFile *os.File
	if *outputFile != "" {
		outFile, err = os.OpenFile(*outputFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Cannot open output file: %v\n", err)
			os.Exit(1)
		}
		defer outFile.Close()
	}

	disp := newDisplay()
	disp.printHeader(*chunkSec, *outputFile)
	disp.startSpinner()

	// Level meter: update display with current audio level every 100ms
	go func() {
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-disp.stopCh:
				return
			case <-t.C:
				bufMu.Lock()
				snap := make([]int16, len(buffer))
				copy(snap, buffer)
				bufMu.Unlock()
				if len(snap) > 0 {
					r := rms(snap)
					// Normalise to 0..1 against a typical loud speech peak (~10000)
					disp.setLevel(math.Min(r/10000.0, 1.0))
				}
			}
		}
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	ticker := time.NewTicker(time.Duration(*chunkSec) * time.Second)
	defer ticker.Stop()

	var pendingMu sync.Mutex
	pending := 0

	for {
		select {
		case <-ticker.C:
			bufMu.Lock()
			if len(buffer) < sampleRate/2 {
				bufMu.Unlock()
				continue
			}
			chunk := make([]int16, len(buffer))
			copy(chunk, buffer)
			buffer = buffer[:0]
			bufMu.Unlock()

			chunkRMS := rms(chunk)
			if *silenceRMS > 0 && chunkRMS < *silenceRMS {
				continue // silent chunk, skip
			}

			pendingMu.Lock()
			pending++
			disp.setProcessing(pending)
			pendingMu.Unlock()

			go func(samples []int16) {
				defer func() {
					pendingMu.Lock()
					pending--
					disp.setProcessing(pending)
					pendingMu.Unlock()
				}()

				var wav bytes.Buffer
				writeWAV(&wav, samples)

				text, err := transcribeWAV(*apiKey, wav.Bytes())
				if err != nil {
					disp.errLine(err.Error())
					return
				}
				if text == "" {
					return
				}
				disp.printLine(text)
				if outFile != nil {
					fmt.Fprintln(outFile, text)
				}
			}(chunk)

		case <-sigChan:
			for {
				pendingMu.Lock()
				p := pending
				pendingMu.Unlock()
				if p == 0 {
					break
				}
				disp.setProcessing(p)
				time.Sleep(300 * time.Millisecond)
			}
			disp.stop()
			fmt.Printf("%sStopped.%s\n", colorGray, colorReset)
			return
		}
	}
}

# macAudioTranscript

A macOS terminal app that listens to your microphone and transcribes speech in real time using OpenAI Whisper.

## Features

- Live microphone capture with a real-time audio level meter
- Timestamped transcript lines printed as speech is recognized
- Silence detection — silent chunks are skipped automatically
- Optional transcript save to a file
- One-shot transcription of an existing WAV file

## Requirements

- macOS (arm64 or amd64)
- Go 1.21+
- An [OpenAI API key](https://platform.openai.com/api-keys)

## Installation

```bash
git clone https://github.com/michaelwp/macAudioTranscription
cd macAudioTranscription
go build -o transcript .
```

## Usage

### Live microphone transcription

```bash
export OPENAI_API_KEY="sk-..."
./transcript
```

### Pass the key inline

```bash
./transcript -key sk-...
```

### Save transcript to a file

```bash
./transcript -out transcript.txt
```

### Transcribe an existing WAV file

```bash
./transcript -file recording.wav
```

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-key` | `$OPENAI_API_KEY` | OpenAI API key |
| `-chunk` | `5` | Seconds of audio per transcription request |
| `-silence` | `200` | RMS threshold below which a chunk is treated as silence and skipped (0 = send everything) |
| `-out` | *(none)* | File to append transcript lines to |
| `-file` | *(none)* | Transcribe a WAV file instead of mic input |

## Screen layout

```
◉ Mac Audio Transcription
────────────────────────────────────────────────────────────────────────────────

  Chunk size : 5s

[15:04:12] Hello, this is a test of the transcription system.
[15:04:19] The audio is being captured and sent to Whisper.

⠸  ████████░░░░  [transcribing 1…]
```

- The **level bar** reflects live microphone input. If it does not move when you speak, check macOS microphone permissions under **System Settings → Privacy & Security → Microphone**.
- Each **transcript line** is timestamped and printed above the status bar.
- The **spinner** and `[transcribing N…]` counter show in-flight API requests.

## Troubleshooting

**No transcript appears**
- Make sure `OPENAI_API_KEY` is set and valid.
- Check the level bar — if it is flat when you speak, the mic is not being captured. Grant microphone access and rerun.
- Try `-silence 0` to bypass silence detection and force every chunk to be sent.

**Transcription is cut off**
- Increase `-chunk` (e.g. `-chunk 10`) to send longer audio segments.

**Too much noise transcribed**
- Increase `-silence` (e.g. `-silence 800`) to raise the silence threshold.

## How it works

1. Audio is captured from the default microphone at 16 kHz, 16-bit mono PCM using [miniaudio](https://github.com/mackron/miniaudio) via the [malgo](https://github.com/gen2brain/malgo) Go bindings.
2. Every `-chunk` seconds the buffer is flushed, encoded as a WAV file in memory, and sent to the [OpenAI Whisper API](https://platform.openai.com/docs/guides/speech-to-text) (`whisper-1` model).
3. The transcription result is printed to the terminal with a timestamp.

## License

MIT

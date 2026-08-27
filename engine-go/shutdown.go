package main

// Graceful shutdown: on Ctrl+C / SIGTERM the bot flushes chat history, detaches the
// DevTools session from Chrome (never closing the user's tabs) and — only when
// STOP_BRAIN_ON_EXIT=true — stops the llama/whisper servers it launched itself.
// By default the brain stays up so the model remains warm in VRAM across bot restarts.

import (
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

var (
	engineProcMu sync.Mutex
	engineProcs  []*os.Process // llama-server / whisper-server / TTS / embed — процессы WE launched
	brainProc    *os.Process   // именно МОЗГ (llama-server на PORT_BRAIN), если подняли мы
)

// registerEngineProc remembers a child engine process for optional stop-on-exit.
func registerEngineProc(p *os.Process) {
	engineProcMu.Lock()
	engineProcs = append(engineProcs, p)
	engineProcMu.Unlock()
}

// trackBrainProc remembers the llama-server process WE started. Переключение модели
// обязано гасить ТОЛЬКО его, а не весь engineProcs (там ещё whisper/TTS/embed — их
// трогать нельзя, иначе после смены модели умирает озвучка и распознавание речи).
func trackBrainProc(p *os.Process) {
	engineProcMu.Lock()
	brainProc = p
	engineProcMu.Unlock()
}

// forgetBrainProc отвязывает pid мозга (убит при переключении) от реестра движков.
func forgetBrainProc(pid int) {
	engineProcMu.Lock()
	defer engineProcMu.Unlock()
	if brainProc != nil && brainProc.Pid == pid {
		brainProc = nil
	}
	var out []*os.Process
	for _, p := range engineProcs {
		if p != nil && p.Pid == pid {
			continue
		}
		out = append(out, p)
	}
	engineProcs = out
}

// stopLaunchedEngines kills every engine process this bot started (never ones it found
// already running on their ports).
func stopLaunchedEngines() {
	engineProcMu.Lock()
	defer engineProcMu.Unlock()
	for _, p := range engineProcs {
		log.Printf("[SHUTDOWN] stopping engine pid %d", p.Pid)
		if err := p.Kill(); err != nil {
			log.Printf("[SHUTDOWN] kill pid %d: %v", p.Pid, err)
		}
	}
	engineProcs = nil
}

// installShutdownHook wires the signal handler. Call once from main before polling starts.
func installShutdownHook(cfg Config) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ch
		log.Printf("[SHUTDOWN] останавливаюсь: сохраняю историю и отсоединяюсь от Chrome")
		saveHistoryNow()
		closeMemory()
		closeJournal()
		// Detach politely, but never hang shutdown behind a stuck browser action.
		done := make(chan struct{})
		go func() {
			closeBrowserSession()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			log.Printf("[SHUTDOWN] browser detach timed out — exiting anyway")
		}
		if cfg.StopBrainOnExit {
			stopLaunchedEngines()
		}
		log.Printf("[SHUTDOWN] bye")
		os.Exit(0)
	}()
}

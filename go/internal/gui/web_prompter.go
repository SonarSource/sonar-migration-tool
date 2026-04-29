package gui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"

	"github.com/sonar-solutions/sonar-migration-tool/internal/wizard"
)

// WebPrompter implements wizard.Prompter over a WebSocket connection.
// Prompt methods block on a per-request channel until the browser responds.
// Display methods are fire-and-forget.
type WebPrompter struct {
	done    <-chan struct{}
	doneErr func() error
	sendFn  func(ServerMessage)
	mu      sync.Mutex
	pending map[string]chan ClientMessage
}

// NewWebPrompter returns a WebPrompter that sends messages via sendFn
// and respects cancellation from ctx.
func NewWebPrompter(ctx context.Context, sendFn func(ServerMessage)) *WebPrompter {
	return &WebPrompter{
		done:    ctx.Done(),
		doneErr: ctx.Err,
		sendFn:  sendFn,
		pending: make(map[string]chan ClientMessage),
	}
}

// HandleResponse routes a browser response to the waiting prompt goroutine.
func (wp *WebPrompter) HandleResponse(msg ClientMessage) {
	wp.mu.Lock()
	ch, ok := wp.pending[msg.ID]
	if ok {
		delete(wp.pending, msg.ID)
	}
	wp.mu.Unlock()

	if ok {
		ch <- msg
	}
}

// --- Prompt methods (blocking) ---

func (wp *WebPrompter) PromptURL(message string, validate bool) (string, error) {
	resp, err := wp.prompt(ServerMessage{
		Type:     TypePromptURL,
		Message:  message,
		Validate: validate,
	})
	if err != nil {
		return "", err
	}
	return toString(resp.Value), nil
}

func (wp *WebPrompter) PromptText(message, defaultVal string) (string, error) {
	resp, err := wp.prompt(ServerMessage{
		Type:    TypePromptText,
		Message: message,
		Default: defaultVal,
	})
	if err != nil {
		return "", err
	}
	return toString(resp.Value), nil
}

func (wp *WebPrompter) PromptPassword(message string) (string, error) {
	resp, err := wp.prompt(ServerMessage{
		Type:    TypePromptPassword,
		Message: message,
	})
	if err != nil {
		return "", err
	}
	return toString(resp.Value), nil
}

func (wp *WebPrompter) Confirm(message string, defaultVal bool) (bool, error) {
	resp, err := wp.prompt(ServerMessage{
		Type:    TypePromptConfirm,
		Message: message,
		Default: defaultVal,
	})
	if err != nil {
		return false, err
	}
	return toBool(resp.Value), nil
}

func (wp *WebPrompter) ConfirmReview(title string, details []wizard.KV) (bool, error) {
	resp, err := wp.prompt(ServerMessage{
		Type:    TypePromptConfirmReview,
		Title:   title,
		Details: ToKVPairs(details),
	})
	if err != nil {
		return false, err
	}
	return toBool(resp.Value), nil
}

// --- Display methods (fire-and-forget) ---

func (wp *WebPrompter) DisplayWelcome() {
	wp.sendFn(ServerMessage{Type: TypeDisplayWelcome})
}

func (wp *WebPrompter) DisplayPhaseProgress(phase wizard.WizardPhase) {
	wp.sendFn(ServerMessage{
		Type:  TypeDisplayPhaseProgress,
		Phase: string(phase),
		Index: wizard.PhaseIndex(phase),
		Total: wizard.PhaseCount(),
		Name:  wizard.PhaseDisplayName(phase),
	})
}

func (wp *WebPrompter) DisplayMessage(msg string) {
	wp.sendFn(ServerMessage{Type: TypeDisplayMessage, Message: msg})
}

func (wp *WebPrompter) DisplayError(msg string) {
	wp.sendFn(ServerMessage{Type: TypeDisplayError, Message: msg})
}

func (wp *WebPrompter) DisplayWarning(msg string) {
	wp.sendFn(ServerMessage{Type: TypeDisplayWarning, Message: msg})
}

func (wp *WebPrompter) DisplaySuccess(msg string) {
	wp.sendFn(ServerMessage{Type: TypeDisplaySuccess, Message: msg})
}

func (wp *WebPrompter) DisplaySummary(title string, stats []wizard.KV) {
	wp.sendFn(ServerMessage{
		Type:  TypeDisplaySummary,
		Title: title,
		Stats: ToKVPairs(stats),
	})
}

func (wp *WebPrompter) DisplayResumeInfo(state *wizard.WizardState) {
	msg := ServerMessage{
		Type:  TypeDisplayResumeInfo,
		Phase: string(state.Phase),
	}
	if state.SourceURL != nil {
		msg.SourceURL = *state.SourceURL
	}
	if state.TargetURL != nil {
		msg.TargetURL = *state.TargetURL
	}
	if state.ExtractID != nil {
		msg.ExtractID = *state.ExtractID
	}
	wp.sendFn(msg)
}

func (wp *WebPrompter) DisplayWizardComplete() {
	wp.sendFn(ServerMessage{Type: TypeDisplayWizardComplete})
}

// --- internal helpers ---

// prompt sends a message and blocks until the browser responds or ctx is cancelled.
func (wp *WebPrompter) prompt(msg ServerMessage) (ClientMessage, error) {
	id := newPromptID()
	msg.ID = id

	ch := make(chan ClientMessage, 1)
	wp.mu.Lock()
	wp.pending[id] = ch
	wp.mu.Unlock()

	wp.sendFn(msg)

	select {
	case resp := <-ch:
		return resp, nil
	case <-wp.done:
		wp.mu.Lock()
		delete(wp.pending, id)
		wp.mu.Unlock()
		return ClientMessage{}, wp.doneErr()
	}
}

func newPromptID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(b)
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

func toBool(v any) bool {
	switch b := v.(type) {
	case bool:
		return b
	case string:
		return b == "true"
	default:
		return false
	}
}

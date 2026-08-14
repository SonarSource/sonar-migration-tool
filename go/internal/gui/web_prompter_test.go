// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package gui

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/wizard"
)

// collectMessages returns a sendFn and a snapshot function. The snapshot
// function returns a copy of all received messages under the mutex, so tests
// can safely read message state from the main goroutine without a DATA RACE.
func collectMessages() (func(ServerMessage), func() []ServerMessage) {
	var mu sync.Mutex
	var msgs []ServerMessage
	send := func(msg ServerMessage) {
		mu.Lock()
		msgs = append(msgs, msg)
		mu.Unlock()
	}
	snapshot := func() []ServerMessage {
		mu.Lock()
		defer mu.Unlock()
		cp := make([]ServerMessage, len(msgs))
		copy(cp, msgs)
		return cp
	}
	return send, snapshot
}

func TestPromptURLBlocksAndReturns(t *testing.T) {
	sendFn, snapshot := collectMessages()
	ctx := context.Background()
	wp := NewWebPrompter(ctx, sendFn)

	var result string
	var err error
	done := make(chan struct{})

	go func() {
		result, err = wp.PromptURL("Server URL:", true)
		close(done)
	}()

	// Wait for the prompt to be sent.
	time.Sleep(20 * time.Millisecond)
	if len(snapshot()) != 1 {
		t.Fatalf("expected 1 message sent, got %d", len(snapshot()))
	}
	sent := snapshot()[0]
	if sent.Type != TypePromptURL {
		t.Fatalf("expected type %q, got %q", TypePromptURL, sent.Type)
	}
	if sent.ID == "" {
		t.Fatal("prompt ID should not be empty")
	}

	// Respond.
	wp.HandleResponse(ClientMessage{
		Type:  TypePromptResponse,
		ID:    sent.ID,
		Value: "https://sonar.example.com/",
	})

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("PromptURL did not return in time")
	}

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "https://sonar.example.com/" {
		t.Errorf("got %q, want %q", result, "https://sonar.example.com/")
	}
}

func TestPromptTextWithDefault(t *testing.T) {
	sendFn, snapshot := collectMessages()
	ctx := context.Background()
	wp := NewWebPrompter(ctx, sendFn)

	done := make(chan struct{})
	var result string

	go func() {
		result, _ = wp.PromptText("Enterprise key:", "default-key")
		close(done)
	}()

	time.Sleep(20 * time.Millisecond)
	sent := snapshot()[0]
	if sent.Default != "default-key" {
		t.Errorf("default: got %v, want %q", sent.Default, "default-key")
	}

	wp.HandleResponse(ClientMessage{ID: sent.ID, Value: "my-key"})
	<-done

	if result != "my-key" {
		t.Errorf("got %q, want %q", result, "my-key")
	}
}

func TestPromptPasswordMasked(t *testing.T) {
	sendFn, snapshot := collectMessages()
	ctx := context.Background()
	wp := NewWebPrompter(ctx, sendFn)

	done := make(chan struct{})
	var result string

	go func() {
		result, _ = wp.PromptPassword("Token:")
		close(done)
	}()

	time.Sleep(20 * time.Millisecond)
	sent := snapshot()[0]
	if sent.Type != TypePromptPassword {
		t.Fatalf("type: got %q", sent.Type)
	}

	wp.HandleResponse(ClientMessage{ID: sent.ID, Value: "secret-token"})
	<-done

	if result != "secret-token" {
		t.Errorf("got %q", result)
	}
}

func TestConfirmReturnsBool(t *testing.T) {
	sendFn, snapshot := collectMessages()
	ctx := context.Background()
	wp := NewWebPrompter(ctx, sendFn)

	done := make(chan struct{})
	var result bool

	go func() {
		result, _ = wp.Confirm("Proceed?", false)
		close(done)
	}()

	time.Sleep(20 * time.Millisecond)
	wp.HandleResponse(ClientMessage{ID: snapshot()[0].ID, Value: true})
	<-done

	if !result {
		t.Error("expected true")
	}
}

func TestConfirmReviewSendsDetails(t *testing.T) {
	sendFn, snapshot := collectMessages()
	ctx := context.Background()
	wp := NewWebPrompter(ctx, sendFn)

	details := []wizard.KV{
		{Key: "URL", Value: "https://example.com"},
		{Key: "Token", Value: "********"},
	}

	done := make(chan struct{})
	var result bool

	go func() {
		result, _ = wp.ConfirmReview("Review Credentials", details)
		close(done)
	}()

	time.Sleep(20 * time.Millisecond)
	sent := snapshot()[0]
	if sent.Type != TypePromptConfirmReview {
		t.Fatalf("type: got %q", sent.Type)
	}
	if sent.Title != "Review Credentials" {
		t.Errorf("title: got %q", sent.Title)
	}
	if len(sent.Details) != 2 {
		t.Fatalf("details: got %d, want 2", len(sent.Details))
	}

	wp.HandleResponse(ClientMessage{ID: sent.ID, Value: false})
	<-done

	if result {
		t.Error("expected false")
	}
}

func TestPromptExtractFormRoundTrips(t *testing.T) {
	sendFn, snapshot := collectMessages()
	ctx := context.Background()
	wp := NewWebPrompter(ctx, sendFn)

	done := make(chan struct{})
	var url, token string
	var includeProjectData, includeIssueSync bool

	go func() {
		url, token, includeProjectData, includeIssueSync, _ = wp.PromptExtractForm(
			"https://sq.example.com", true, true, true)
		close(done)
	}()

	time.Sleep(20 * time.Millisecond)
	sent := snapshot()[0]
	if sent.Type != TypePromptExtractForm {
		t.Fatalf("type: got %q", sent.Type)
	}
	if sent.DefaultURL != "https://sq.example.com" {
		t.Errorf("default_url: got %q", sent.DefaultURL)
	}
	if !sent.TokenOptional {
		t.Error("token_optional: got false, want true")
	}
	if !sent.DefaultIncludeProjectData || !sent.DefaultIncludeIssueSync {
		t.Errorf("defaults: got %v/%v, want true/true", sent.DefaultIncludeProjectData, sent.DefaultIncludeIssueSync)
	}

	wp.HandleResponse(ClientMessage{ID: sent.ID, Value: map[string]any{
		"url":                "https://sq.other.com",
		"token":              "secret",
		"includeProjectData": false,
		"includeIssueSync":   false,
	}})
	<-done

	if url != "https://sq.other.com" {
		t.Errorf("url: got %q", url)
	}
	if token != "secret" {
		t.Errorf("token: got %q", token)
	}
	if includeProjectData {
		t.Error("includeProjectData: want false")
	}
	if includeIssueSync {
		t.Error("includeIssueSync: want false")
	}
}

func TestPromptExtractFormCancelledByContext(t *testing.T) {
	sendFn, _ := collectMessages()
	ctx, cancel := context.WithCancel(context.Background())
	wp := NewWebPrompter(ctx, sendFn)

	done := make(chan struct{})
	var err error

	go func() {
		_, _, _, _, err = wp.PromptExtractForm("https://sq.example.com", false, true, true)
		close(done)
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("PromptExtractForm did not return after cancel")
	}

	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestPromptMigrateFormRoundTrips(t *testing.T) {
	sendFn, snapshot := collectMessages()
	ctx := context.Background()
	wp := NewWebPrompter(ctx, sendFn)

	done := make(chan struct{})
	var url, token, enterpriseKey string
	var includeProjectData, includeIssueSync bool

	go func() {
		url, token, enterpriseKey, includeProjectData, includeIssueSync, _ = wp.PromptMigrateForm(
			"https://sonarcloud.io", true, "my-enterprise", true, true)
		close(done)
	}()

	time.Sleep(20 * time.Millisecond)
	sent := snapshot()[0]
	if sent.Type != TypePromptMigrateForm {
		t.Fatalf("type: got %q", sent.Type)
	}
	if sent.DefaultURL != "https://sonarcloud.io" {
		t.Errorf("default_url: got %q", sent.DefaultURL)
	}
	if !sent.TokenOptional {
		t.Error("token_optional: got false, want true")
	}
	if sent.DefaultEnterpriseKey != "my-enterprise" {
		t.Errorf("default_enterprise_key: got %q", sent.DefaultEnterpriseKey)
	}
	if !sent.DefaultIncludeProjectData || !sent.DefaultIncludeIssueSync {
		t.Errorf("defaults: got %v/%v, want true/true", sent.DefaultIncludeProjectData, sent.DefaultIncludeIssueSync)
	}

	wp.HandleResponse(ClientMessage{ID: sent.ID, Value: map[string]any{
		"url":                "https://other.sonarcloud.io",
		"token":              "secret",
		"enterpriseKey":      "other-enterprise",
		"includeProjectData": false,
		"includeIssueSync":   false,
	}})
	<-done

	if url != "https://other.sonarcloud.io" {
		t.Errorf("url: got %q", url)
	}
	if token != "secret" {
		t.Errorf("token: got %q", token)
	}
	if enterpriseKey != "other-enterprise" {
		t.Errorf("enterpriseKey: got %q", enterpriseKey)
	}
	if includeProjectData {
		t.Error("includeProjectData: want false")
	}
	if includeIssueSync {
		t.Error("includeIssueSync: want false")
	}
}

func TestPromptMigrateFormCancelledByContext(t *testing.T) {
	sendFn, _ := collectMessages()
	ctx, cancel := context.WithCancel(context.Background())
	wp := NewWebPrompter(ctx, sendFn)

	done := make(chan struct{})
	var err error

	go func() {
		_, _, _, _, _, err = wp.PromptMigrateForm("https://sonarcloud.io", false, "my-enterprise", true, true)
		close(done)
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("PromptMigrateForm did not return after cancel")
	}

	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestContextCancellation(t *testing.T) {
	sendFn, _ := collectMessages()
	ctx, cancel := context.WithCancel(context.Background())
	wp := NewWebPrompter(ctx, sendFn)

	done := make(chan struct{})
	var err error

	go func() {
		_, err = wp.PromptURL("URL:", false)
		close(done)
	}()

	// Cancel before responding.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("PromptURL did not return after cancel")
	}

	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestContextCancellationCleansPending(t *testing.T) {
	sendFn, _ := collectMessages()
	ctx, cancel := context.WithCancel(context.Background())
	wp := NewWebPrompter(ctx, sendFn)

	done := make(chan struct{})
	go func() {
		wp.PromptURL("URL:", false)
		close(done)
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()
	<-done

	wp.mu.Lock()
	pending := len(wp.pending)
	wp.mu.Unlock()

	if pending != 0 {
		t.Errorf("expected 0 pending, got %d", pending)
	}
}

func TestDisplayMethodsAreFineAndForget(t *testing.T) {
	sendFn, snapshot := collectMessages()
	ctx := context.Background()
	wp := NewWebPrompter(ctx, sendFn)

	wp.DisplayWelcome()
	wp.DisplayPhaseProgress(wizard.PhaseExtract)
	wp.DisplayMessage("hello")
	wp.DisplayError("bad")
	wp.DisplayWarning("careful")
	wp.DisplaySuccess("done")
	wp.DisplaySummary("Stats", []wizard.KV{{Key: "A", Value: "1"}})
	wp.DisplayResumeInfo(&wizard.WizardState{Phase: wizard.PhaseExtract})
	wp.DisplayWizardComplete()

	if len(snapshot()) != 9 {
		t.Errorf("expected 9 display messages, got %d", len(snapshot()))
	}

	expectedTypes := []string{
		TypeDisplayWelcome, TypeDisplayPhaseProgress, TypeDisplayMessage,
		TypeDisplayError, TypeDisplayWarning, TypeDisplaySuccess,
		TypeDisplaySummary, TypeDisplayResumeInfo, TypeDisplayWizardComplete,
	}
	msgs := snapshot()
	for i, want := range expectedTypes {
		if msgs[i].Type != want {
			t.Errorf("msg %d: got %q, want %q", i, msgs[i].Type, want)
		}
	}
}

func TestDisplayPhaseProgressFields(t *testing.T) {
	sendFn, snapshot := collectMessages()
	ctx := context.Background()
	wp := NewWebPrompter(ctx, sendFn)

	wp.DisplayPhaseProgress(wizard.PhaseStructure)
	msg := snapshot()[0]

	if msg.Phase != string(wizard.PhaseStructure) {
		t.Errorf("phase: got %q", msg.Phase)
	}
	if msg.Index != 2 {
		t.Errorf("index: got %d, want 2", msg.Index)
	}
	if msg.Total != 6 {
		t.Errorf("total: got %d, want 6", msg.Total)
	}
	if msg.Name != "Structure" {
		t.Errorf("name: got %q", msg.Name)
	}
}

func TestDisplayResumeInfoWithNilPointers(t *testing.T) {
	sendFn, snapshot := collectMessages()
	ctx := context.Background()
	wp := NewWebPrompter(ctx, sendFn)

	// All pointer fields nil.
	wp.DisplayResumeInfo(&wizard.WizardState{Phase: wizard.PhaseOrgMapping})
	msg := snapshot()[0]

	if msg.SourceURL != "" || msg.TargetURL != "" || msg.ExtractID != "" {
		t.Errorf("nil pointers should produce empty strings: source=%q target=%q extract=%q",
			msg.SourceURL, msg.TargetURL, msg.ExtractID)
	}
}

func TestHandleResponseIgnoresUnknownID(t *testing.T) {
	sendFn, _ := collectMessages()
	ctx := context.Background()
	wp := NewWebPrompter(ctx, sendFn)

	// Should not panic or block.
	wp.HandleResponse(ClientMessage{ID: "nonexistent", Value: "test"})
}

func TestMultiplePromptsSequential(t *testing.T) {
	sendFn, snapshot := collectMessages()
	ctx := context.Background()
	wp := NewWebPrompter(ctx, sendFn)

	for i := 0; i < 3; i++ {
		done := make(chan string)
		go func() {
			result, _ := wp.PromptText("Q:", "")
			done <- result
		}()

		time.Sleep(20 * time.Millisecond)
		wp.HandleResponse(ClientMessage{ID: snapshot()[i].ID, Value: "answer"})
		<-done
	}

	wp.mu.Lock()
	pending := len(wp.pending)
	wp.mu.Unlock()

	if pending != 0 {
		t.Errorf("expected 0 pending after all resolved, got %d", pending)
	}
}

func TestToBoolStringCoercion(t *testing.T) {
	tests := []struct {
		input any
		want  bool
	}{
		{true, true},
		{false, false},
		{"true", true},
		{"false", false},
		{"", false},
		{nil, false},
		{42, false},
	}
	for _, tt := range tests {
		got := toBool(tt.input)
		if got != tt.want {
			t.Errorf("toBool(%v) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestToStringCoercion(t *testing.T) {
	if got := toString("hello"); got != "hello" {
		t.Errorf("toString(string) = %q", got)
	}
	if got := toString(42); got != "42" {
		t.Errorf("toString(int) = %q", got)
	}
	if got := toString(nil); got != "<nil>" {
		t.Errorf("toString(nil) = %q", got)
	}
}

func TestPromptChoiceNumericResponse(t *testing.T) {
	sendFn, snapshot := collectMessages()
	ctx := context.Background()
	wp := NewWebPrompter(ctx, sendFn)

	done := make(chan struct{})
	var result int

	go func() {
		result, _ = wp.PromptChoice("Pick one:", []string{"A", "B", "C"})
		close(done)
	}()

	time.Sleep(20 * time.Millisecond)
	sent := snapshot()[0]
	if sent.Type != TypePromptChoice {
		t.Fatalf("type: got %q", sent.Type)
	}
	if len(sent.Options) != 3 {
		t.Fatalf("options: got %d, want 3", len(sent.Options))
	}

	// Respond with float64 (as JSON numbers decode).
	wp.HandleResponse(ClientMessage{ID: sent.ID, Value: float64(2)})
	<-done

	if result != 2 {
		t.Errorf("got %d, want 2", result)
	}
}

func TestPromptChoiceStringResponse(t *testing.T) {
	sendFn, snapshot := collectMessages()
	ctx := context.Background()
	wp := NewWebPrompter(ctx, sendFn)

	done := make(chan struct{})
	var result int

	go func() {
		result, _ = wp.PromptChoice("Pick:", []string{"alpha", "beta", "gamma"})
		close(done)
	}()

	time.Sleep(20 * time.Millisecond)
	sent := snapshot()[0]

	// Respond with string matching an option.
	wp.HandleResponse(ClientMessage{ID: sent.ID, Value: "beta"})
	<-done

	if result != 1 {
		t.Errorf("got %d, want 1 (beta)", result)
	}
}

func TestPromptChoiceUnknownStringResponse(t *testing.T) {
	sendFn, snapshot := collectMessages()
	ctx := context.Background()
	wp := NewWebPrompter(ctx, sendFn)

	done := make(chan struct{})
	var result int

	go func() {
		result, _ = wp.PromptChoice("Pick:", []string{"alpha", "beta"})
		close(done)
	}()

	time.Sleep(20 * time.Millisecond)
	sent := snapshot()[0]

	// Respond with a string that doesn't match any option.
	wp.HandleResponse(ClientMessage{ID: sent.ID, Value: "unknown"})
	<-done

	if result != 0 {
		t.Errorf("got %d, want 0 for unmatched string", result)
	}
}

func TestSetBackEnabled(t *testing.T) {
	sendFn, _ := collectMessages()
	ctx := context.Background()
	wp := NewWebPrompter(ctx, sendFn)

	wp.SetBackEnabled(true)
	wp.mu.Lock()
	enabled := wp.backEnabled
	wp.mu.Unlock()
	if !enabled {
		t.Error("expected backEnabled to be true")
	}

	wp.SetBackEnabled(false)
	wp.mu.Lock()
	enabled = wp.backEnabled
	wp.mu.Unlock()
	if enabled {
		t.Error("expected backEnabled to be false")
	}
}

func TestPromptChoiceCancelled(t *testing.T) {
	sendFn, _ := collectMessages()
	ctx, cancel := context.WithCancel(context.Background())
	wp := NewWebPrompter(ctx, sendFn)

	done := make(chan struct{})
	var err error

	go func() {
		_, err = wp.PromptChoice("Pick:", []string{"A", "B"})
		close(done)
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()
	<-done

	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestConfirmReviewCancelled(t *testing.T) {
	sendFn, _ := collectMessages()
	ctx, cancel := context.WithCancel(context.Background())
	wp := NewWebPrompter(ctx, sendFn)

	done := make(chan struct{})
	var err error

	go func() {
		_, err = wp.ConfirmReview("Review", nil)
		close(done)
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()
	<-done

	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

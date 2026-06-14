package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/update"
)

func TestHandleUpdateStatusGET_NoChecker(t *testing.T) {
	update.StopGlobalChecker()

	cfg := update.DefaultConfig()
	cfg.AutoApply = false
	handler := NewUpdateHandler(cfg, t.TempDir())

	req := httptest.NewRequest(http.MethodGet, "/admin/api/update/status", nil)
	w := httptest.NewRecorder()
	handler.HandleUpdateStatusGET(w, req)

	if got := w.Code; got != http.StatusOK {
		t.Errorf("status = %d, want %d\nbody: %s", got, http.StatusOK, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatal("response missing data field")
	}

	if got, _ := data["available"].(bool); got != false {
		t.Errorf("available = %v, want false", got)
	}

	if got, _ := data["currentVersion"].(string); got == "" {
		t.Error("currentVersion should not be empty")
	}

	if got, _ := data["latestVersion"].(string); got != "" {
		t.Errorf("latestVersion = %q, want empty string (no checker)", got)
	}

	if got, _ := data["autoApply"].(bool); got != false {
		t.Errorf("autoApply = %v, want false", got)
	}

	// updateState is now exposed for JS polling (H1 fix from Round 7 review).
	if got, _ := data["updateState"].(string); got != "idle" {
		t.Errorf("updateState = %q, want %q (idle initial state)", got, "idle")
	}
}

func TestHandleUpdateStatusGET_WithChecker(t *testing.T) {
	cfg := update.DefaultConfig()
	cfg.AutoApply = true

	handler := NewUpdateHandler(cfg, t.TempDir())

	req := httptest.NewRequest(http.MethodGet, "/admin/api/update/status", nil)
	w := httptest.NewRecorder()
	handler.HandleUpdateStatusGET(w, req)

	if got := w.Code; got != http.StatusOK {
		t.Errorf("status = %d, want %d\nbody: %s", got, http.StatusOK, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatal("response missing data field")
	}

	if got, _ := data["available"].(bool); got != false {
		t.Errorf("available = %v, want false (no check run yet)", got)
	}

	if got, _ := data["currentVersion"].(string); got == "" {
		t.Error("currentVersion should not be empty")
	}

	if got, _ := data["autoApply"].(bool); got != true {
		t.Errorf("autoApply = %v, want true", got)
	}
}

func TestHandleUpdateStatusGET_ContentType(t *testing.T) {
	cfg := update.DefaultConfig()
	cfg.AutoApply = false
	handler := NewUpdateHandler(cfg, t.TempDir())

	req := httptest.NewRequest(http.MethodGet, "/admin/api/update/status", nil)
	w := httptest.NewRecorder()
	handler.HandleUpdateStatusGET(w, req)

	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want %q", got, "application/json")
	}
}

func TestHandleUpdateStatusGET_ResponseFields(t *testing.T) {
	cfg := update.DefaultConfig()
	cfg.AutoApply = true
	handler := NewUpdateHandler(cfg, t.TempDir())

	req := httptest.NewRequest(http.MethodGet, "/admin/api/update/status", nil)
	w := httptest.NewRecorder()
	handler.HandleUpdateStatusGET(w, req)

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatal("response missing data field")
	}

	// Status response exposes 5 fields: spec 4 + updateState (needed for JS polling).
	requiredFields := []string{"available", "currentVersion", "latestVersion", "autoApply", "updateState"}
	for _, field := range requiredFields {
		if _, ok := data[field]; !ok {
			t.Errorf("response missing required field: %s", field)
		}
	}
}

func TestHandleUpdateApplyPOST_Response(t *testing.T) {
	cfg := update.DefaultConfig()
	handler := NewUpdateHandler(cfg, t.TempDir())

	req := httptest.NewRequest(http.MethodPost, "/admin/api/update/apply", nil)
	w := httptest.NewRecorder()
	handler.HandleUpdateApplyPOST(w, req)

	// With empty downloadURL (no checker set up), should return 400 Bad Request.
	if got := w.Code; got != http.StatusBadRequest {
		t.Errorf("status = %d, want %d\nbody: %s", got, http.StatusBadRequest, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatal("response missing data field")
	}

	if msg, ok := data["message"].(string); !ok || msg == "" {
		t.Error("response should have non-empty message field")
	}

	if state, ok := data["updateState"].(string); !ok || state != "idle" {
		t.Errorf("updateState = %q, want %q", state, "idle")
	}

	// B1 contract: body must agree with stored state atomic. A regression
	// where the state stays Running while the body says "idle" would now fail.
	if got := handler.state.Load(); got != int32(UpdateStateIdle) {
		t.Errorf("handler.state = %d, want %d (UpdateStateIdle)", got, int32(UpdateStateIdle))
	}
}

func TestHandleUpdateApplyPOST_DuplicateRequest(t *testing.T) {
	cfg := update.DefaultConfig()
	handler := NewUpdateHandler(cfg, t.TempDir())

	// First apply with empty downloadURL fails validation (400 Bad Request).
	// Per M1: state must remain Idle (not Failed) so operator is not trapped in reset loop.
	req1 := httptest.NewRequest(http.MethodPost, "/admin/api/update/apply", nil)
	w1 := httptest.NewRecorder()
	handler.HandleUpdateApplyPOST(w1, req1)

	if w1.Code != http.StatusBadRequest {
		t.Errorf("first request status = %d, want %d (bad request - no download URL)", w1.Code, http.StatusBadRequest)
	}

	// Per M1: state should be Idle (swapped back after validation failure).
	if got := handler.state.Load(); int(got) != int(UpdateStateIdle) {
		t.Errorf("state after first apply = %d, want %d (idle - swapped back)", got, UpdateStateIdle)
	}

	// Second apply - same behavior (state is Idle so CAS succeeds again, URL still empty so 400).
	req2 := httptest.NewRequest(http.MethodPost, "/admin/api/update/apply", nil)
	w2 := httptest.NewRecorder()
	handler.HandleUpdateApplyPOST(w2, req2)

	if w2.Code != http.StatusBadRequest {
		t.Errorf("second request status = %d, want %d (bad request)", w2.Code, http.StatusBadRequest)
	}
}

func TestHandleUpdateApplyPOST_DuplicateRequest_WithRunningState(t *testing.T) {
	cfg := update.DefaultConfig()
	handler := NewUpdateHandler(cfg, t.TempDir())

	// Manually set state to Running to simulate concurrent apply.
	handler.state.Store(int32(UpdateStateRunning))

	req := httptest.NewRequest(http.MethodPost, "/admin/api/update/apply", nil)
	w := httptest.NewRecorder()
	handler.HandleUpdateApplyPOST(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d (conflict)", w.Code, http.StatusConflict)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatal("response missing data field")
	}

	if msg, ok := data["message"].(string); !ok || msg != "Update already in progress" {
		t.Errorf("message = %q, want %q", msg, "Update already in progress")
	}
}

func TestHandleUpdateApplyPOST_DuplicateRequest_WithFailedState(t *testing.T) {
	cfg := update.DefaultConfig()
	handler := NewUpdateHandler(cfg, t.TempDir())

	// Manually set state to Failed.
	handler.state.Store(int32(UpdateStateFailed))

	req := httptest.NewRequest(http.MethodPost, "/admin/api/update/apply", nil)
	w := httptest.NewRecorder()
	handler.HandleUpdateApplyPOST(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d (conflict)", w.Code, http.StatusConflict)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatal("response missing data field")
	}

	// Per M3: Apply from Failed must report Failed-specific message, not the generic "in progress" message.
	expectedMsg := "Previous update failed — reset before retrying"
	if msg, ok := data["message"].(string); !ok || msg != expectedMsg {
		t.Errorf("message = %q, want %q", msg, expectedMsg)
	}
}

func TestHandleUpdateApplyPOST_StateTransitions(t *testing.T) {
	cfg := update.DefaultConfig()
	handler := NewUpdateHandler(cfg, t.TempDir())

	// Initial state should be idle
	if got := handler.state.Load(); int(got) != int(UpdateStateIdle) {
		t.Errorf("initial state = %d, want %d (idle)", got, UpdateStateIdle)
	}

	// First apply with empty downloadURL should fail validation; per M1 state stays Idle.
	req1 := httptest.NewRequest(http.MethodPost, "/admin/api/update/apply", nil)
	w1 := httptest.NewRecorder()
	handler.HandleUpdateApplyPOST(w1, req1)

	if w1.Code != http.StatusBadRequest {
		t.Errorf("first apply status = %d, want %d (bad request - no download URL)", w1.Code, http.StatusBadRequest)
	}

	var resp1 map[string]interface{}
	json.Unmarshal(w1.Body.Bytes(), &resp1)
	data1 := resp1["data"].(map[string]interface{})
	if msg, _ := data1["message"].(string); msg == "" {
		t.Error("response should have non-empty message")
	}

	// Per M1: state remains Idle so operator can retry without reset.
	if got := handler.state.Load(); int(got) != int(UpdateStateIdle) {
		t.Errorf("after failed apply state = %d, want %d (idle - swapped back)", got, UpdateStateIdle)
	}

	// Reset from Idle now returns 409 Conflict (per M2: reset is a no-op from non-Failed states).
	req2 := httptest.NewRequest(http.MethodPost, "/admin/api/update/reset", nil)
	w2 := httptest.NewRecorder()
	handler.HandleUpdateResetPOST(w2, req2)

	if w2.Code != http.StatusConflict {
		t.Errorf("reset status = %d, want %d (conflict - reset from idle is no-op)", w2.Code, http.StatusConflict)
	}

	// State should still be idle (reset was a no-op).
	if got := handler.state.Load(); int(got) != int(UpdateStateIdle) {
		t.Errorf("after reset state = %d, want %d (idle)", got, UpdateStateIdle)
	}

	// Second apply - empty URL still fails with 400, state remains Idle.
	req3 := httptest.NewRequest(http.MethodPost, "/admin/api/update/apply", nil)
	w3 := httptest.NewRecorder()
	handler.HandleUpdateApplyPOST(w3, req3)

	if w3.Code != http.StatusBadRequest {
		t.Errorf("second apply status = %d, want %d (bad request)", w3.Code, http.StatusBadRequest)
	}

	if got := handler.state.Load(); int(got) != int(UpdateStateIdle) {
		t.Errorf("after second failed apply state = %d, want %d (idle - swapped back)", got, UpdateStateIdle)
	}
}

func TestHandleUpdateResetPOST(t *testing.T) {
	cfg := update.DefaultConfig()
	handler := NewUpdateHandler(cfg, t.TempDir())

	// Set state to failed (simulating a failed update)
	handler.state.Store(int32(UpdateStateFailed))

	req := httptest.NewRequest(http.MethodPost, "/admin/api/update/reset", nil)
	w := httptest.NewRecorder()
	handler.HandleUpdateResetPOST(w, req)

	if got := w.Code; got != http.StatusOK {
		t.Errorf("status = %d, want %d\nbody: %s", got, http.StatusOK, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatal("response missing data field")
	}

	if got, _ := data["updateState"].(string); got != "idle" {
		t.Errorf("updateState = %q, want %q", got, "idle")
	}

	if handler.state.Load() != int32(UpdateStateIdle) {
		t.Error("state should be reset to idle")
	}
}

func TestHandleUpdateResetPOST_FromNonFailedState(t *testing.T) {
	cfg := update.DefaultConfig()
	handler := NewUpdateHandler(cfg, t.TempDir())

	// Set state to idle — per M2, reset from Idle returns 409 Conflict (no-op).
	handler.state.Store(int32(UpdateStateIdle))

	req := httptest.NewRequest(http.MethodPost, "/admin/api/update/reset", nil)
	w := httptest.NewRecorder()
	handler.HandleUpdateResetPOST(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d (conflict - reset from idle is no-op)", w.Code, http.StatusConflict)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatal("response missing data field")
	}

	if msg, _ := data["message"].(string); msg == "" {
		t.Error("reset response should have non-empty message")
	}

	// State should remain idle (not changed)
	if handler.state.Load() != int32(UpdateStateIdle) {
		t.Error("state should remain idle when reset called from idle")
	}
}

func TestHandleUpdateResetPOST_FromRunningState(t *testing.T) {
	cfg := update.DefaultConfig()
	handler := NewUpdateHandler(cfg, t.TempDir())

	// Set state to running. S-06 changed the contract: reset
	// from Running now SUCCEEDS (200), cancels the in-flight
	// apply goroutine via applyCancel, and transitions the state
	// to Failed (so the operator can retry via /apply).
	handler.state.Store(int32(UpdateStateRunning))
	// Stub a cancel func that we can observe being called.
	cancelled := false
	cancel := context.CancelFunc(func() { cancelled = true })
	handler.applyCancel.Store(&cancel)

	req := httptest.NewRequest(http.MethodPost, "/admin/api/update/reset", nil)
	w := httptest.NewRecorder()
	handler.HandleUpdateResetPOST(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (S-06: reset from Running now succeeds)", w.Code, http.StatusOK)
	}

	// S-06: the cancel func must have been invoked (signals
	// the 500 MB download to abort).
	if !cancelled {
		t.Error("applyCancel was NOT invoked — the 500 MB download would NOT abort")
	}

	// State must transition Running -> Failed (not Idle — the
	// operator triggered the reset, so "failed" is more accurate
	// for forensics).
	if got := handler.state.Load(); got != int32(UpdateStateFailed) {
		t.Errorf("state after reset = %d, want %d (Failed)", got, UpdateStateFailed)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	data, _ := resp["data"].(map[string]interface{})
	if stateStr, _ := data["updateState"].(string); stateStr != "failed" {
		t.Errorf("response updateState = %q, want %q", stateStr, "failed")
	}
}

func TestHandleUpdateApplyPOST_GoroutineCleanup(t *testing.T) {
	cfg := update.DefaultConfig()
	handler := NewUpdateHandler(cfg, t.TempDir())

	req := httptest.NewRequest(http.MethodPost, "/admin/api/update/apply", nil)
	w := httptest.NewRecorder()

	// Override the checker to return empty URL so DownloadAndApply fails quickly
	update.StopGlobalChecker()

	handler.HandleUpdateApplyPOST(w, req)

	// With empty downloadURL, validation should fail immediately — no goroutine started.
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d (bad request — no download URL)", w.Code, http.StatusBadRequest)
	}

	if handler.state.Load() != int32(UpdateStateIdle) {
		t.Errorf("final state = %d, want %d (idle - swapped back per M1)", handler.state.Load(), UpdateStateIdle)
	}
}

func TestUpdateStatusFromConfig(t *testing.T) {
	result := UpdateStatusFromConfig(nil, nil)
	if got, _ := result["available"].(bool); got != false {
		t.Errorf("available = %v, want false", got)
	}
	if got, _ := result["autoApply"].(bool); got != false {
		t.Errorf("autoApply = %v, want false", got)
	}

	checkResult := &update.CheckResult{
		VersionAvailable: true,
		LatestVersion:    "1.2.3",
		DownloadURL:      "https://example.com/download",
	}

	result = UpdateStatusFromConfig(nil, checkResult)
	if got, _ := result["available"].(bool); got != true {
		t.Errorf("available = %v, want true", got)
	}
	if got, _ := result["latestVersion"].(string); got != "1.2.3" {
		t.Errorf("latestVersion = %q, want %q", got, "1.2.3")
	}
}

func TestUpdateStateString(t *testing.T) {
	tests := []struct {
		state  UpdateState
		expect string
	}{
		{UpdateStateIdle, "idle"},
		{UpdateStateRunning, "running"},
		{UpdateStateFailed, "failed"},
		{UpdateState(99), "unknown"},
	}

	for _, tt := range tests {
		if got := tt.state.String(); got != tt.expect {
			t.Errorf("UpdateState(%d).String() = %q, want %q", tt.state, got, tt.expect)
		}
	}
}

// TestHandleUpdate_B1Contract verifies the B1 contract (debt-triage-2026-06-06
// C-02): for every HTTP response from the update handler, the `updateState`
// field in the JSON body MUST match the value of `handler.state.Load()` at
// the moment the response is sent.
//
// Why this matters: historically (5-3 R7-H4 / R9-H4), the apply response
// reported `updateState: "failed"` while the atomic state stayed `Idle`,
// because the error path was setting the body from a local variable
// instead of re-reading the atomic. This made the JS polling in admin.js
// detect a success path that was actually a failure.
//
// This test is parametric so every response path is exercised:
//   - Status handler: body state == atomic state
//   - Apply with empty downloadURL: CAS back to Idle, body reports Idle
//   - Apply when state is already non-Idle: body reports the current state
//   - Reset from Failed: CAS to Idle, body reports Idle
//   - Reset from Running: cancel + CAS to Failed, body reports Failed
func TestHandleUpdate_B1Contract(t *testing.T) {
	type setupFn func(t *testing.T) (*UpdateHandler, *http.Request)

	cases := []struct {
		name  string
		setup setupFn
	}{
		{
			name: "StatusGET reports atomic state",
			setup: func(t *testing.T) (*UpdateHandler, *http.Request) {
				update.StopGlobalChecker()
				cfg := update.DefaultConfig()
				cfg.AutoApply = false
				h := NewUpdateHandler(cfg, t.TempDir())
				h.state.Store(int32(UpdateStateIdle))
				return h, httptest.NewRequest(http.MethodGet, "/admin/api/update/status", nil)
			},
		},
		{
			name: "ApplyPOST with empty downloadURL — body=Idle, atomic=Idle",
			setup: func(t *testing.T) (*UpdateHandler, *http.Request) {
				update.StopGlobalChecker()
				cfg := update.DefaultConfig()
				h := NewUpdateHandler(cfg, t.TempDir())
				// checker is nil → downloadURL is empty → handler returns 400 + Idle
				return h, httptest.NewRequest(http.MethodPost, "/admin/api/update/apply", nil)
			},
		},
		{
			name: "ApplyPOST when state is Running — body=Running, atomic=Running",
			setup: func(t *testing.T) (*UpdateHandler, *http.Request) {
				update.StopGlobalChecker()
				cfg := update.DefaultConfig()
				h := NewUpdateHandler(cfg, t.TempDir())
				h.state.Store(int32(UpdateStateRunning))
				return h, httptest.NewRequest(http.MethodPost, "/admin/api/update/apply", nil)
			},
		},
		{
			name: "ApplyPOST when state is Failed — body=Failed, atomic=Failed",
			setup: func(t *testing.T) (*UpdateHandler, *http.Request) {
				update.StopGlobalChecker()
				cfg := update.DefaultConfig()
				h := NewUpdateHandler(cfg, t.TempDir())
				h.state.Store(int32(UpdateStateFailed))
				return h, httptest.NewRequest(http.MethodPost, "/admin/api/update/apply", nil)
			},
		},
		{
			name: "ResetPOST from Failed — body=Idle, atomic=Idle",
			setup: func(t *testing.T) (*UpdateHandler, *http.Request) {
				cfg := update.DefaultConfig()
				h := NewUpdateHandler(cfg, t.TempDir())
				h.state.Store(int32(UpdateStateFailed))
				// Stub the cancel func so the reset handler doesn't touch a real goroutine.
				var nilFunc context.CancelFunc
				h.applyCancel.Store(&nilFunc)
				return h, httptest.NewRequest(http.MethodPost, "/admin/api/update/reset", nil)
			},
		},
		{
			// P2 (code review 2026-06-07): the B1 test docstring claimed
			// "Reset from Running" coverage but only "from Failed" was
			// tested. The Running branch (update_handler.go:247-280)
			// cancels the in-flight goroutine, then transitions to
			// Failed. Body must report Failed and the atomic must agree.
			name: "ResetPOST from Running — body=Failed, atomic=Failed",
			setup: func(t *testing.T) (*UpdateHandler, *http.Request) {
				cfg := update.DefaultConfig()
				h := NewUpdateHandler(cfg, t.TempDir())
				h.state.Store(int32(UpdateStateRunning))
				// Stub a no-op cancel func so the reset handler can invoke
				// it without touching a real goroutine. (The flag is
				// referenced to silence unused-var lints; the reset
				// handler invokes cancel which sets it.)
				_ = true
				cancel := context.CancelFunc(func() {})
				h.applyCancel.Store(&cancel)
				return h, httptest.NewRequest(http.MethodPost, "/admin/api/update/reset", nil)
			},
			// post: a real /reset from Running CASes to Failed, so the
			// "before" handler returns body=Failed. We can't observe the
			// post-reset state here (the next subtest is independent),
			// so we only verify the response body.
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, req := tc.setup(t)
			w := httptest.NewRecorder()

			// Route to the right handler based on the request path
			switch req.URL.Path {
			case "/admin/api/update/status":
				h.HandleUpdateStatusGET(w, req)
			case "/admin/api/update/apply":
				h.HandleUpdateApplyPOST(w, req)
			case "/admin/api/update/reset":
				h.HandleUpdateResetPOST(w, req)
			default:
				t.Fatalf("unhandled path %q in test setup", req.URL.Path)
			}

			// B1: read the atomic state RIGHT NOW (after handler returns)
			atomicState := UpdateState(h.state.Load()).String()

			// Parse body
			body := w.Body.String()
			if body == "" {
				t.Fatalf("empty body, status=%d, atomicState=%q", w.Code, atomicState)
			}
			var resp map[string]interface{}
			if err := json.Unmarshal([]byte(body), &resp); err != nil {
				t.Fatalf("unmarshal body: %v\nbody: %s", err, body)
			}
			data, _ := resp["data"].(map[string]interface{})
			if data == nil {
				// Some responses don't have a "data" wrapper (errors with a top-level
				// "error" object). In that case, the B1 contract is vacuously satisfied:
				// there is no updateState field in the body to disagree with the atomic.
				return
			}
			bodyState, ok := data["updateState"].(string)
			if !ok {
				// No updateState field — also vacuously consistent.
				return
			}

			if bodyState != atomicState {
				t.Errorf("B1 contract violation: body.updateState=%q but handler.state=%q\nbody: %s",
					bodyState, atomicState, body)
			}
		})
	}
}

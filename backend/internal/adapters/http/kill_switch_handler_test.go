package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/risk"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubCtrl struct {
	state atomic.Int32
	last  struct {
		state  risk.KillSwitchState
		reason string
		actor  string
	}
}

func (c *stubCtrl) State() risk.KillSwitchState {
	return risk.KillSwitchState(c.state.Load())
}

func (c *stubCtrl) SetState(s risk.KillSwitchState, reason, actor string) risk.KillSwitchState {
	prev := risk.KillSwitchState(c.state.Swap(int32(s)))
	c.last.state = s
	c.last.reason = reason
	c.last.actor = actor
	return prev
}

type stubEvents struct {
	ev *KillSwitchEventDTO
}

func (s stubEvents) LastKillSwitchEvent(_ context.Context) (*KillSwitchEventDTO, error) {
	return s.ev, nil
}

func newHandler(ctrl *stubCtrl, ev *KillSwitchEventDTO) *KillSwitchHandler {
	var reader KillSwitchEventReader
	if ev != nil {
		reader = stubEvents{ev: ev}
	}
	return NewKillSwitchHandler(ctrl, reader, zerolog.Nop())
}

func doPost(t *testing.T, h http.Handler, body any) *httptest.ResponseRecorder {
	t.Helper()
	buf, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/kill-switch", bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestKillSwitchHandler_PostValidTransition(t *testing.T) {
	ctrl := &stubCtrl{}
	h := newHandler(ctrl, nil)

	rr := doPost(t, h, map[string]string{"state": "REDUCING", "reason": "operator halt drill", "actor": "alice"})
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]string
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, "REDUCING", resp["state"])
	assert.Equal(t, "ACTIVE", resp["previous"])
	assert.NotEmpty(t, resp["at"])
	assert.Equal(t, risk.KillSwitchReducing, ctrl.State())
	assert.Equal(t, "alice", ctrl.last.actor)
	assert.Equal(t, "operator halt drill", ctrl.last.reason)
}

func TestKillSwitchHandler_PostInvalidState(t *testing.T) {
	ctrl := &stubCtrl{}
	h := newHandler(ctrl, nil)

	rr := doPost(t, h, map[string]string{"state": "BOGUS", "reason": "x"})
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "unknown kill switch state")
	assert.Equal(t, risk.KillSwitchActive, ctrl.State(), "state must not change on validation failure")
}

func TestKillSwitchHandler_PostMissingState(t *testing.T) {
	rr := doPost(t, newHandler(&stubCtrl{}, nil), map[string]string{"reason": "x"})
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestKillSwitchHandler_PostMissingReason(t *testing.T) {
	rr := doPost(t, newHandler(&stubCtrl{}, nil), map[string]string{"state": "HALTED"})
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "reason is required")
}

func TestKillSwitchHandler_PostInvalidJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/kill-switch", bytes.NewReader([]byte("{not json")))
	rr := httptest.NewRecorder()
	newHandler(&stubCtrl{}, nil).ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestKillSwitchHandler_Get(t *testing.T) {
	ctrl := &stubCtrl{}
	ctrl.state.Store(int32(risk.KillSwitchReducing))

	ev := &KillSwitchEventDTO{
		At:       time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC),
		OldState: "ACTIVE",
		NewState: "REDUCING",
		Reason:   "daily loss",
		Actor:    "system",
	}
	h := newHandler(ctrl, ev)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/kill-switch", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var resp killSwitchGetResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, "REDUCING", resp.State)
	require.NotNil(t, resp.LastEvent)
	assert.Equal(t, "REDUCING", resp.LastEvent.NewState)
	assert.Equal(t, "daily loss", resp.LastEvent.Reason)
}

func TestKillSwitchHandler_MethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/kill-switch", nil)
	rr := httptest.NewRecorder()
	newHandler(&stubCtrl{}, nil).ServeHTTP(rr, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

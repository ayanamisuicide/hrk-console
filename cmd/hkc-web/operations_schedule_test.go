package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

// testOperationStore создаёт изолированное хранилище операций во временном каталоге.
func testOperationStore(t *testing.T) *operationStore {
	t.Helper()
	store, err := openOperationStore(filepath.Join(t.TempDir(), "operations.json"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// TestMaintenanceBlocksStartButAllowsStop проверяет запрет старта при доступной остановке во время
// обслуживания.
func TestMaintenanceBlocksStartButAllowsStop(t *testing.T) {
	s := newTestServer(t)
	s.operations = testOperationStore(t)
	if _, err := s.operations.setMaintenance(true, "planned work"); err != nil {
		t.Fatal(err)
	}
	result, status := s.performAction("start")
	if status != http.StatusConflict || result.OK || result.Message == "" {
		t.Fatalf("start during maintenance: %d %+v", status, result)
	}
	if _, status := s.performAction("stop"); status != http.StatusOK {
		t.Fatalf("stop during maintenance: %d", status)
	}
}

// TestOperationStorePersistsAndRequeues проверяет сохранение очереди и повторный захват незавершённой
// задачи после перезапуска.
func TestOperationStorePersistsAndRequeues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.json")
	store, err := openOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.setMaintenance(true, "deploy"); err != nil {
		t.Fatal(err)
	}
	item, err := store.createSchedule("restart", time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.data.Schedules[0].Status = "running"
	if err := store.saveLocked(); err != nil {
		store.mu.Unlock()
		t.Fatal(err)
	}
	store.mu.Unlock()

	reopened, err := openOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if state := reopened.maintenanceState(); !state.Enabled || state.Message != "deploy" {
		t.Fatalf("maintenance was not persisted: %+v", state)
	}
	items := reopened.listSchedules()
	if len(items) != 1 || items[0].ID != item.ID || items[0].Status != "pending" {
		t.Fatalf("schedule was not requeued: %+v", items)
	}
}

// TestMaintenanceAndScheduleHTTP проверяет административный API обслуживания и расписаний.
func TestMaintenanceAndScheduleHTTP(t *testing.T) {
	s := newTestServer(t)
	s.operations = testOperationStore(t)

	maintenanceBody := bytes.NewBufferString(`{"enabled":true,"message":"upgrade","stopBot":false}`)
	response := httptest.NewRecorder()
	s.setMaintenance(response, adminRequest(http.MethodPut, "/api/admin/maintenance", maintenanceBody))
	if response.Code != http.StatusOK {
		t.Fatalf("maintenance: %d %s", response.Code, response.Body.String())
	}

	payload, _ := json.Marshal(map[string]any{"action": "stop", "runAt": time.Now().Add(time.Minute).UTC()})
	response = httptest.NewRecorder()
	s.createSchedule(response, adminRequest(http.MethodPost, "/api/admin/schedules", bytes.NewReader(payload)))
	if response.Code != http.StatusCreated {
		t.Fatalf("create schedule: %d %s", response.Code, response.Body.String())
	}
	var created scheduledAction
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	request := adminRequest(http.MethodDelete, "/api/admin/schedules/"+created.ID, nil)
	request.SetPathValue("id", created.ID)
	response = httptest.NewRecorder()
	s.deleteSchedule(response, request)
	if response.Code != http.StatusOK || len(s.operations.listSchedules()) != 0 {
		t.Fatalf("delete schedule: %d %s", response.Code, response.Body.String())
	}
}

// TestOperationsRequireAdmin проверяет отказ в операциях без административного токена.
func TestOperationsRequireAdmin(t *testing.T) {
	s := newTestServer(t)
	s.operations = testOperationStore(t)
	response := httptest.NewRecorder()
	s.listSchedules(response, httptest.NewRequest(http.MethodGet, "/api/admin/schedules", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("schedules without token: %d", response.Code)
	}
}

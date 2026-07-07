package gormstore

import (
	"testing"
	"time"

	"github.com/libtnb/sessions"
	"github.com/libtnb/sessions/middleware"
	"github.com/libtnb/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"net/http"
	"net/http/httptest"
)

const testID = "12345678901234567890123456789012"

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	// A pooled second connection would see its own empty :memory: database.
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db.DB failed: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	return db
}

func TestWriteReadRoundtrip(t *testing.T) {
	st := New(newTestDB(t))

	if err := st.Write(testID, "payload"); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	data, found, err := st.Read(testID)
	if err != nil || !found {
		t.Fatalf("Read: found=%v err=%v", found, err)
	}
	if data != "payload" {
		t.Fatalf("Read = %q, want %q", data, "payload")
	}

	// Overwrite keeps a single row with the new data
	if err = st.Write(testID, "updated"); err != nil {
		t.Fatalf("second Write failed: %v", err)
	}
	data, found, err = st.Read(testID)
	if err != nil || !found || data != "updated" {
		t.Fatalf("Read after overwrite: data=%q found=%v err=%v", data, found, err)
	}
}

func TestReadMissingReportsNotFound(t *testing.T) {
	st := New(newTestDB(t))

	data, found, err := st.Read(testID)
	if err != nil {
		t.Fatalf("Read of missing session must not error, got: %v", err)
	}
	if found || data != "" {
		t.Fatalf("Read of missing session: data=%q found=%v, want empty and false", data, found)
	}
}

func TestTouchRefreshesTimestamp(t *testing.T) {
	db := newTestDB(t)
	st := New(db)

	if err := st.Write(testID, "payload"); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	old := time.Now().Add(-time.Hour)
	if err := db.Table(defaultTableName).Where("id = ?", testID).Update("updated_at", old).Error; err != nil {
		t.Fatalf("backdate failed: %v", err)
	}

	found, err := st.Touch(testID)
	if err != nil || !found {
		t.Fatalf("Touch: found=%v err=%v", found, err)
	}

	var s gormSession
	if err = db.Table(defaultTableName).Where("id = ?", testID).First(&s).Error; err != nil {
		t.Fatalf("read back failed: %v", err)
	}
	if !s.UpdatedAt.After(time.Now().Add(-time.Minute)) {
		t.Fatalf("Touch did not refresh updated_at, got %v", s.UpdatedAt)
	}
}

func TestTouchMissingReportsNotFound(t *testing.T) {
	st := New(newTestDB(t))

	found, err := st.Touch(testID)
	if err != nil {
		t.Fatalf("Touch of missing session must not error, got: %v", err)
	}
	if found {
		t.Fatal("Touch of missing session reported found=true")
	}
}

func TestDestroy(t *testing.T) {
	st := New(newTestDB(t))

	if err := st.Write(testID, "payload"); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if err := st.Destroy(testID); err != nil {
		t.Fatalf("Destroy failed: %v", err)
	}
	if _, found, err := st.Read(testID); found || err != nil {
		t.Fatalf("session still readable after Destroy: found=%v err=%v", found, err)
	}

	// Destroying a missing session is not an error
	if err := st.Destroy(testID); err != nil {
		t.Fatalf("Destroy of missing session failed: %v", err)
	}
}

func TestGcRemovesOnlyExpired(t *testing.T) {
	db := newTestDB(t)
	st := New(db)

	if err := st.Write(testID, "old"); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	freshID := "abcdefghijklmnopqrstuvwxyz012345"
	if err := st.Write(freshID, "fresh"); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := db.Table(defaultTableName).Where("id = ?", testID).Update("updated_at", old).Error; err != nil {
		t.Fatalf("backdate failed: %v", err)
	}

	if err := st.Gc(600); err != nil {
		t.Fatalf("Gc failed: %v", err)
	}

	if _, found, _ := st.Read(testID); found {
		t.Fatal("expired session survived Gc")
	}
	if _, found, _ := st.Read(freshID); !found {
		t.Fatal("fresh session was removed by Gc")
	}
}

// TestSessionsIntegration drives the full sessions stack (manager,
// middleware, cookies) against this driver.
func TestSessionsIntegration(t *testing.T) {
	manager, err := sessions.NewManager(&sessions.ManagerOptions{
		Key:                  "12345678901234567890123456789012",
		Lifetime:             10,
		GcInterval:           10,
		DisableDefaultDriver: true,
	})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	defer func() { _ = manager.Close() }()
	if err = manager.Extend("gorm", New(newTestDB(t))); err != nil {
		t.Fatalf("Extend failed: %v", err)
	}

	handler := middleware.StartSession(manager, "gorm")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, err := manager.GetSession(r)
		if err != nil {
			t.Errorf("GetSession failed: %v", err)
			return
		}
		n, _ := s.Get("count", 0).(int)
		s.Put("count", n+1)
	}))

	rr1 := httptest.NewRecorder()
	handler.ServeHTTP(rr1, httptest.NewRequest(http.MethodGet, "/", nil))
	cookies := rr1.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected session cookie")
	}

	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.AddCookie(cookies[0])
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, req2)

	// The counter persisting to 2 proves Read/Write/Touch flow through gorm.
	var count any
	verify := middleware.StartSession(manager, "gorm")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, _ := manager.GetSession(r)
		count = s.Get("count")
	}))
	req3 := httptest.NewRequest(http.MethodGet, "/", nil)
	req3.AddCookie(cookies[0])
	verify.ServeHTTP(httptest.NewRecorder(), req3)

	if count != 2 {
		t.Fatalf("count = %v, want 2", count)
	}
}

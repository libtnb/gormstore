package gormstore

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/libtnb/sessions"
	"github.com/libtnb/sessions/driver"
	"github.com/libtnb/sessions/middleware"
	"github.com/libtnb/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const testID = "12345678901234567890123456789012"

// newTestDB connects to the database given by DATABASE_URI
// ("<sqlite3|postgres|mysql>://<dsn>", see the ./test script), defaulting to
// an in-memory sqlite database. Containerized databases may still be
// starting up, so the connection is retried for a while.
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	uri := os.Getenv("DATABASE_URI")
	if uri == "" {
		uri = "sqlite3://:memory:"
	}
	scheme, dsn, ok := strings.Cut(uri, "://")
	if !ok {
		t.Fatalf("invalid DATABASE_URI %q", uri)
	}

	var dialector gorm.Dialector
	switch scheme {
	case "sqlite3":
		dialector = sqlite.Open(dsn)
	case "postgres":
		dialector = postgres.Open(dsn)
	case "mysql":
		dialector = mysql.Open(dsn)
	default:
		t.Fatalf("unsupported DATABASE_URI scheme %q", scheme)
	}

	var (
		db  *gorm.DB
		err error
	)
	deadline := time.Now().Add(60 * time.Second)
	for {
		db, err = gorm.Open(dialector, &gorm.Config{Logger: logger.Discard})
		if err == nil {
			var pingErr error
			if sqlDB, dbErr := db.DB(); dbErr == nil {
				pingErr = sqlDB.Ping()
			} else {
				pingErr = dbErr
			}
			if pingErr == nil {
				break
			}
			err = pingErr
		}
		if time.Now().After(deadline) {
			t.Fatalf("connect to database failed: %v", err)
		}
		time.Sleep(time.Second)
	}

	if scheme == "sqlite3" {
		// A pooled second connection would see its own empty :memory: database.
		sqlDB, dbErr := db.DB()
		if dbErr != nil {
			t.Fatalf("db.DB failed: %v", dbErr)
		}
		sqlDB.SetMaxOpenConns(1)
	}
	return db
}

// newTestStore builds a store on a per-test table so tests never see each
// other's rows when they share one database instance.
func newTestStore(t *testing.T) (driver.Driver, *gorm.DB, string) {
	t.Helper()

	db := newTestDB(t)
	table := "sessions_" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "_"))
	db.Exec("DROP TABLE IF EXISTS " + table) // leftovers from a previous run
	return NewOptions(db, Options{TableName: table}), db, table
}

func TestWriteReadRoundtrip(t *testing.T) {
	st, _, _ := newTestStore(t)

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
	st, _, _ := newTestStore(t)

	data, found, err := st.Read(testID)
	if err != nil {
		t.Fatalf("Read of missing session must not error, got: %v", err)
	}
	if found || data != "" {
		t.Fatalf("Read of missing session: data=%q found=%v, want empty and false", data, found)
	}
}

func TestTouchRefreshesTimestamp(t *testing.T) {
	st, db, table := newTestStore(t)

	if err := st.Write(testID, "payload"); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	old := time.Now().Add(-time.Hour)
	if err := db.Table(table).Where("id = ?", testID).Update("updated_at", old).Error; err != nil {
		t.Fatalf("backdate failed: %v", err)
	}

	found, err := st.Touch(testID)
	if err != nil || !found {
		t.Fatalf("Touch: found=%v err=%v", found, err)
	}

	var s gormSession
	if err = db.Table(table).Where("id = ?", testID).First(&s).Error; err != nil {
		t.Fatalf("read back failed: %v", err)
	}
	if !s.UpdatedAt.After(time.Now().Add(-time.Minute)) {
		t.Fatalf("Touch did not refresh updated_at, got %v", s.UpdatedAt)
	}
}

func TestTouchMissingReportsNotFound(t *testing.T) {
	st, _, _ := newTestStore(t)

	found, err := st.Touch(testID)
	if err != nil {
		t.Fatalf("Touch of missing session must not error, got: %v", err)
	}
	if found {
		t.Fatal("Touch of missing session reported found=true")
	}
}

func TestDestroy(t *testing.T) {
	st, _, _ := newTestStore(t)

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
	st, db, table := newTestStore(t)

	if err := st.Write(testID, "old"); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	freshID := "abcdefghijklmnopqrstuvwxyz012345"
	if err := st.Write(freshID, "fresh"); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := db.Table(table).Where("id = ?", testID).Update("updated_at", old).Error; err != nil {
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
	st, _, _ := newTestStore(t)

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
	if err = manager.Extend("gorm", st); err != nil {
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

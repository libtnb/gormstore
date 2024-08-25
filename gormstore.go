/*
Package gormstore is a GORM backend for gorilla sessions

Simplest form:

	store := gormstore.New(gorm.Open(...), []byte("secret-hash-key"))

All options:

	store := gormstore.NewOptions(
		gorm.Open(...), // *gorm.DB
		gormstore.Options{
			TableName: "sessions",  // "sessions" is default
			SkipCreateTable: false, // false is default
		},
		[]byte("secret-hash-key"),       // 32 or 64 bytes recommended, required
		[]byte("secret-encryption-key")) // nil, 16, 24 or 32 bytes, optional

		// some more settings, see sessions.Options
		store.SessionOpts.Secure = true
		store.SessionOpts.HttpOnly = true
		store.SessionOpts.MaxAge = 60 * 60 * 24 * 60

If you want periodic cleanup of expired sessions:

	quit := make(chan struct{})
	go store.PeriodicCleanup(1*time.Hour, quit)

For more information about the keys see https://github.com/gorilla/securecookie

For API to use in HTTP handlers see https://github.com/gorilla/sessions
*/
package gormstore

import (
	"encoding/base32"
	"net/http"
	"strings"
	"time"

	"github.com/go-rat/securecookie"
	"github.com/gorilla/sessions"
	"gorm.io/gorm"
)

const sessionIDLen = 32
const defaultTableName = "sessions"
const defaultMaxAge = 60 * 60 * 24 * 30 // 30 days
const defaultPath = "/"

// Options for gormstore
type Options struct {
	TableName       string
	SkipCreateTable bool
}

// Store represent a gormstore
type Store struct {
	db           *gorm.DB
	opts         Options
	keys         [][]byte
	secureCookie *securecookie.SecureCookie
	SessionOpts  *sessions.Options
}

type gormSession struct {
	ID        string `sql:"unique_index"`
	Data      string `sql:"type:text"`
	CreatedAt time.Time
	UpdatedAt time.Time
	ExpiresAt time.Time `sql:"index"`
}

// New creates a new gormstore session
func New(db *gorm.DB, keys ...[]byte) *Store {
	return NewOptions(db, Options{}, keys...)
}

// NewOptions creates a new gormstore session with options
func NewOptions(db *gorm.DB, opts Options, keys ...[]byte) *Store {
	if len(keys) == 0 {
		panic("key required")
	}

	sc, _ := securecookie.New(keys[0], &securecookie.Options{
		RotatedKeys: keys[1:],
		Serializer:  securecookie.GobEncoder{},
	})
	st := &Store{
		db:           db,
		opts:         opts,
		keys:         keys,
		secureCookie: sc,
		SessionOpts: &sessions.Options{
			Path:   defaultPath,
			MaxAge: defaultMaxAge,
		},
	}
	if st.opts.TableName == "" {
		st.opts.TableName = defaultTableName
	}

	if !st.opts.SkipCreateTable {
		_ = st.sessionTable().AutoMigrate(&gormSession{})
	}

	return st
}

func (st *Store) sessionTable() *gorm.DB {
	return st.db.Table(st.opts.TableName)
}

// Get returns a session for the given name after adding it to the registry.
func (st *Store) Get(r *http.Request, name string) (*sessions.Session, error) {
	return sessions.GetRegistry(r).Get(st, name)
}

// New creates a session with name without adding it to the registry.
func (st *Store) New(r *http.Request, name string) (*sessions.Session, error) {
	session := sessions.NewSession(st, name)
	opts := *st.SessionOpts
	session.Options = &opts
	session.IsNew = true

	st.MaxAge(st.SessionOpts.MaxAge)

	// try fetch from db if there is a cookie
	s := st.getSessionFromCookie(r, session.Name())
	if s != nil {
		if err := st.secureCookie.Decode(session.Name(), s.Data, &session.Values); err != nil {
			return session, nil
		}
		session.ID = s.ID
		session.IsNew = false
	}

	return session, nil
}

// Save session and set cookie header
func (st *Store) Save(r *http.Request, w http.ResponseWriter, session *sessions.Session) error {
	s := st.getSessionFromCookie(r, session.Name())

	// delete if max age is < 0
	if session.Options.MaxAge < 0 {
		if s != nil {
			if err := st.sessionTable().Delete(s).Error; err != nil {
				return err
			}
		}
		http.SetCookie(w, sessions.NewCookie(session.Name(), "", session.Options))
		return nil
	}

	data, err := st.secureCookie.Encode(session.Name(), session.Values)
	if err != nil {
		return err
	}
	now := time.Now()
	expire := now.Add(time.Second * time.Duration(session.Options.MaxAge))

	if s == nil {
		// generate random session ID key suitable for storage in the db
		session.ID = strings.TrimRight(
			base32.StdEncoding.EncodeToString(
				securecookie.GenerateRandomKey(sessionIDLen)), "=")
		s = &gormSession{
			ID:        session.ID,
			Data:      data,
			CreatedAt: now,
			UpdatedAt: now,
			ExpiresAt: expire,
		}
		if err := st.sessionTable().Create(s).Error; err != nil {
			return err
		}
	} else {
		s.Data = data
		s.UpdatedAt = now
		s.ExpiresAt = expire
		if err := st.sessionTable().Save(s).Error; err != nil {
			return err
		}
	}

	// set session id cookie
	id, err := st.secureCookie.Encode(session.Name(), s.ID)
	if err != nil {
		return err
	}
	http.SetCookie(w, sessions.NewCookie(session.Name(), id, session.Options))

	return nil
}

// getSessionFromCookie looks for an existing gormSession from a session ID stored inside a cookie
func (st *Store) getSessionFromCookie(r *http.Request, name string) *gormSession {
	if cookie, err := r.Cookie(name); err == nil {
		sessionID := ""
		if err = st.secureCookie.Decode(name, cookie.Value, &sessionID); err != nil {
			return nil
		}
		s := &gormSession{}
		sr := st.sessionTable().Where("id = ? AND expires_at > ?", sessionID, time.Now()).Limit(1).Find(s)
		if sr.Error != nil || sr.RowsAffected == 0 {
			return nil
		}
		return s
	}
	return nil
}

// MaxAge sets the maximum age for the store and the underlying cookie
// implementation. Individual sessions can be deleted by setting
// Options.MaxAge = -1 for that session.
func (st *Store) MaxAge(age int) {
	st.SessionOpts.MaxAge = age
	securecookie.DefaultOptions.MaxAge = int64(age)
	sc, _ := securecookie.New(st.keys[0], &securecookie.Options{
		MaxAge:      int64(age),
		MaxLength:   securecookie.DefaultOptions.MaxLength,
		RotatedKeys: st.keys[1:],
		Serializer:  securecookie.GobEncoder{},
	})
	st.secureCookie = sc
}

// MaxLength restricts the maximum length of new sessions to l.
// If l is 0 there is no limit to the size of a session, use with caution.
// The default is 4096 (default for securecookie)
func (st *Store) MaxLength(l int) {
	securecookie.DefaultOptions.MaxLength = l
	sc, _ := securecookie.New(st.keys[0], &securecookie.Options{
		MaxAge:      securecookie.DefaultOptions.MaxAge,
		MaxLength:   l,
		RotatedKeys: st.keys[1:],
		Serializer:  securecookie.GobEncoder{},
	})
	st.secureCookie = sc
}

// Cleanup deletes expired sessions
func (st *Store) Cleanup() {
	st.sessionTable().Delete(&gormSession{}, "expires_at <= ?", time.Now())
}

// PeriodicCleanup runs Cleanup every interval. Close quit channel to stop.
func (st *Store) PeriodicCleanup(interval time.Duration, quit <-chan struct{}) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			st.Cleanup()
		case <-quit:
			return
		}
	}
}

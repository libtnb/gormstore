package gormstore

import (
	"time"

	"github.com/go-rat/session/driver"
	"gorm.io/gorm"
)

const defaultTableName = "sessions"

// Options for gormstore
type Options struct {
	TableName       string
	SkipCreateTable bool
}

// Store represent a gormstore
type Store struct {
	db   *gorm.DB
	opts Options
}

type gormSession struct {
	ID        string `gorm:"primaryKey;size:16"`
	Data      string `gorm:"type:text"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

// New creates a new gormstore session
func New(db *gorm.DB) driver.Driver {
	return NewOptions(db, Options{})
}

// NewOptions creates a new gormstore session with options
func NewOptions(db *gorm.DB, opts Options) driver.Driver {
	st := &Store{
		db:   db,
		opts: opts,
	}
	if st.opts.TableName == "" {
		st.opts.TableName = defaultTableName
	}

	if !st.opts.SkipCreateTable {
		_ = st.sessionTable().AutoMigrate(&gormSession{})
	}

	return st
}

func (st *Store) Close() error {
	return nil
}

func (st *Store) Destroy(id string) error {
	return st.sessionTable().Delete(&gormSession{}, "id = ?", id).Error
}

func (st *Store) Read(id string) (string, error) {
	// try fetch from db
	s := st.getSessionByID(id)
	if s != nil {
		return s.Data, nil
	}

	return "", nil
}

func (st *Store) Gc(maxLifetime int) error {
	return st.sessionTable().Delete(&gormSession{}, "updated_at < ?", time.Now().Add(-time.Duration(maxLifetime)*time.Second)).Error
}

func (st *Store) Write(id string, data string) error {
	s := &gormSession{
		ID:   id,
		Data: data,
	}
	return st.sessionTable().Save(s).Error
}

func (st *Store) sessionTable() *gorm.DB {
	return st.db.Table(st.opts.TableName)
}

// getSessionByID looks for an existing gormSession from a session ID stored in database
func (st *Store) getSessionByID(id string) *gormSession {
	s := &gormSession{}
	sr := st.sessionTable().Where("id = ?", id).Limit(1).Find(s)
	if sr.Error != nil {
		return nil
	}
	return s
}

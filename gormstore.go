package gormstore

import (
	"errors"
	"time"

	"github.com/libtnb/sessions/driver"
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
	ID        string    `gorm:"primaryKey;size:32"`
	Data      string    `gorm:"type:text"`
	CreatedAt time.Time `gorm:"autoCreateTime"`
	UpdatedAt time.Time `gorm:"autoUpdateTime"`
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

// Read returns the session data for the given ID. A missing session is
// reported via found=false; a query failure is returned as an error so the
// caller never mistakes a database outage for a missing session.
func (st *Store) Read(id string) (string, bool, error) {
	s := &gormSession{}
	if err := st.sessionTable().Where("id = ?", id).First(s).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", false, nil
		}
		return "", false, err
	}

	return s.Data, true, nil
}

func (st *Store) Gc(maxLifetime int) error {
	return st.sessionTable().Delete(&gormSession{}, "updated_at < ?", time.Now().Add(-time.Duration(maxLifetime)*time.Second)).Error
}

func (st *Store) Touch(id string) (bool, error) {
	result := st.sessionTable().Where("id = ?", id).Update("updated_at", time.Now())
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

func (st *Store) Write(id string, data string) error {
	s := &gormSession{ID: id}
	if err := st.sessionTable().Where("id = ?", id).FirstOrInit(s).Error; err != nil {
		return err
	}

	s.Data = data

	return st.sessionTable().Save(s).Error
}

func (st *Store) sessionTable() *gorm.DB {
	return st.db.Table(st.opts.TableName)
}

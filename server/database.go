package server

import (
	"fmt"
	"github.com/jinzhu/gorm"
	_ "github.com/jinzhu/gorm/dialects/mysql"
	_ "github.com/jinzhu/gorm/dialects/sqlite"
	_ "github.com/lib/pq"
	ft "github.com/raphaelreyna/goft"
	"io"
)

func NewDBConn(dbType, connstr string, retries uint) (db *gorm.DB, err error) {
	// Don't need to retry sqlite connection
	if dbType == "sqlite" {
		db, err = gorm.Open(connstr)
		return
	}
	// Create connection function to retry in case of error
	var connFunc ft.FailableNullaryFunc
	db, connFunc = newFNConnFunc(dbType, connstr)
	// Try up to `retries` times to connect to the database
	err = connFunc.Retry(retries, ft.DefaultBackoffSequence, ft.DefaultErrorHandler)
	return
}

func NewPGConnString(host, port, name, user, passwd, ssl string) string {
	connstr := "host=%s port=%s dbname=%s user=%s password=%s"
	connstr = connstr + " sslmode=%s connect_timeout=1"
	return fmt.Sprintf(connstr,
		host, port, name,
		user, passwd, ssl,
	)
}

func NewMySQLConnString(host, name, user, passwd string) string {
	connstr := "%s:%s@(%s)/%s?charset=utf8&parseTime=True&loc=Local"
	return fmt.Sprintf(connstr, user, passwd, host, name)
}

func newFNConnFunc(dbType, conn string) (db *gorm.DB, f ft.FailableNullaryFunc) {
	f = func() error {
		var err error
		db, err = gorm.Open("postgres", conn)
		if err != nil {
			return err
		}
		return nil
	}
	return
}

type DB interface {
	// Store should be capable of storing a given []byte or contents of an io.ReadCloser
	Store(uid string, i interface{}) error
	// Fetch should return either a []byte, or io.ReadCloser.
	// If the requested resource could not be found, both return values should be nil
	Fetch(uid string) (interface{}, error)
}

type SQLite struct {
	Conn string
	db   *gorm.DB
}

func (d *SQLite) Store(i interface{}, uid string) error {
	type fileBlob struct {
		uid  string
		data []byte
	}
	type fileReader struct {
		uid    string
		reader io.Reader
	}
	type jsonString struct {
		uid  string
		data string
	}
	return nil
}

package backupapp

import (
	"context"
	"database/sql"
	"fmt"
	mysql "github.com/go-sql-driver/mysql"
	"net"
	"time"
)

type ddlGuard interface {
	Check() error
	Close()
}
type mysqlDDLGuard struct {
	db       *sql.DB
	conn     *sql.Conn
	identity uint64
}

var acquireGlobalDDLGuard = func(cfg config) (ddlGuard, error) {
	options := mysql.NewConfig()
	options.User = cfg.DBUser
	options.Passwd = cfg.DBPass
	options.Net = "tcp"
	options.Addr = net.JoinHostPort(cfg.DBHost, cfg.DBPort)
	options.Timeout = cfg.PreflightTimeout
	options.ReadTimeout = cfg.PreflightTimeout
	options.WriteTimeout = cfg.PreflightTimeout
	db, e := sql.Open("mysql", options.FormatDSN())
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), cfg.PreflightTimeout)
	defer cancel()
	conn, e := db.Conn(ctx)
	if e != nil {
		db.Close()
		return nil, e
	}
	g := &mysqlDDLGuard{db: db, conn: conn}
	if _, e = conn.ExecContext(ctx, "SET SESSION lock_wait_timeout=10"); e == nil {
		_, e = conn.ExecContext(ctx, "LOCK INSTANCE FOR BACKUP")
	}
	if e != nil {
		conn.Close()
		db.Close()
		return nil, fmt.Errorf("cannot acquire DDL backup lock without weakening consistency: %w", e)
	}
	if e = conn.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&g.identity); e != nil {
		g.Close()
		return nil, e
	}
	return g, nil
}

func (g *mysqlDDLGuard) Check() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var id uint64
	if e := g.conn.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&id); e != nil {
		return fmt.Errorf("backup DDL guard lost during capture: %w", e)
	}
	if id != g.identity {
		return fmt.Errorf("backup DDL guard connection changed")
	}
	return nil
}
func (g *mysqlDDLGuard) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = g.conn.ExecContext(ctx, "UNLOCK INSTANCE")
	_ = g.conn.Close()
	_ = g.db.Close()
}

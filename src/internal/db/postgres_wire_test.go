package db

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"
)

func TestPostgresRejectsOversizedWireMessageBeforeReadingBody(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	serverDone := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()
		if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			serverDone <- err
			return
		}
		backend := pgproto3.NewBackend(conn, conn)
		message, err := backend.ReceiveStartupMessage()
		if err != nil {
			serverDone <- err
			return
		}
		startup, ok := message.(*pgproto3.StartupMessage)
		if !ok || startup.Parameters["user"] != "reader+with space" || startup.Parameters["database"] != "db #+name" {
			serverDone <- errors.New("PostgreSQL profile values changed during URI parsing")
			return
		}
		// Send only the header: rejecting it must not wait for or allocate its
		// declared body, so the test needs neither a large buffer nor a real DB.
		header := []byte{'S', 0, 0, 0, 0}
		binary.BigEndian.PutUint32(header[1:], uint32(maxPostgresMessageBytes+1+4))
		if _, err := conn.Write(header); err != nil {
			serverDone <- err
			return
		}
		var b [1]byte
		_, err = conn.Read(b[:])
		if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
			serverDone <- errors.New("client waited for oversized PostgreSQL message body")
			return
		}
		serverDone <- nil
	}()

	host, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{}
	db, err := service.open(Profile{
		Kind: "postgres", Host: host, Port: port, SSLMode: "disable",
		User: "reader+with space", Database: "db #+name",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = db.PingContext(ctx)
	var bodyLimit *pgproto3.ExceededMaxBodyLenErr
	if !errors.As(err, &bodyLimit) {
		t.Fatalf("PingContext error = %v, want PostgreSQL message limit", err)
	}
	if bodyLimit.MaxExpectedBodyLen != maxPostgresMessageBytes || bodyLimit.ActualBodyLen != maxPostgresMessageBytes+1 {
		t.Fatalf("wrong message limit: %+v", bodyLimit)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

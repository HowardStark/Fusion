package main

import (
	"errors"
	"io"
	"net"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

type Connection struct {
	iface   string
	ifaceID uint64
	conn    net.Conn
	outChan chan []byte
	running bool
	closed  error
	lock    sync.Mutex
}

func (conn *Connection) Read(data []byte) (int, error) {
	if conn.closed != nil {
		return 0, conn.closed
	}
	a, b := conn.conn.Read(data)
	return a, b
}

func (conn *Connection) ReadFull(data []byte) error {
	if conn.closed != nil {
		return conn.closed
	}
	_, b := io.ReadFull(conn.conn, data)
	return b
}

func (conn *Connection) WriteNonBlocking(data []byte) (bool, error) {
	conn.lock.Lock()
	defer conn.lock.Unlock()
	if conn.closed != nil {
		return false, conn.closed // if the writeloop has encountered an error, return it here
	}
	if !conn.running { // conn.closed == nil && !conn.running means it hasn't started yet
		conn.start()
	}
	select {
	case conn.outChan <- data:
		return true, nil
	default:
		return false, nil
	}
}

func (conn *Connection) Write(data []byte) error {
	ok, err := conn.WriteNonBlocking(data) // this makes sure that the connection is open, and writes immediately if possible
	if err != nil {
		return err
	}
	if !ok {
		defer func() {
			if r := recover(); r != nil {
				log.Error("Recovered Write panic", r) // this happens when the connection is closed between conn.lock.Unlock in the call to WriteNonBlocking, and the blocking channel write below. it happens more often than you'd think.
			}
		}()
		conn.outChan <- data // blocking write
		log.Debug("Had to fallback to blocking write")
	}
	return nil
}

func (conn *Connection) start() {
	conn.outChan = make(chan []byte, 4)
	conn.running = true
	go conn.writeloop()
}

func (conn *Connection) writeloop() {
	for {
		data := <-conn.outChan
		conn.conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
		a, err := conn.conn.Write(data)
		if err == nil && a != len(data) {
			panic("what the christ " + string(a) + " " + string(len(data)))
		}
		if err != nil || !conn.running {
			conn.lock.Lock()
			defer conn.lock.Unlock()
			conn.closed = err
			if conn.running {
				close(conn.outChan)
			}
			conn.running = false
			log.WithField("conn", conn.conn).WithError(err).Error("Closing connection in write loop")
			go conn.Close()
			return
		}
	}
}

func (conn *Connection) Close() {
	conn.conn.Close()
	conn.lock.Lock()
	defer conn.lock.Unlock()
	if conn.closed == nil {
		conn.closed = errors.New("close requested")
	}
	if conn.running {
		conn.running = false
		select {
		case conn.outChan <- []byte("goodbye"):
		default: // outChan is full, so no need to blockingly or otherwise write goodbye wake up the write loop thread; since it's full it's already going to be
		}
		close(conn.outChan)
	}
}

func (conn *Connection) LocalAddr() net.Addr {
	return conn.conn.LocalAddr()
}

func (conn *Connection) GetInterfaceID() uint64 {
	return conn.ifaceID
}

func (conn *Connection) GetInterfaceName() string {
	if conn.iface == "" {
		panic("we're server side why are you asking for the interface name i dont even know it lmao")
	}
	return conn.iface
}

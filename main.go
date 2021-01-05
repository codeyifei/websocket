package main

import (
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"golang.org/x/sync/errgroup"
	"log"
	"net/http"
	"sync"
)

var connections = make(map[string]*Connection)
var ErrWsIsClosed = errors.New("connection is closed")

type Connection struct {
	name      string
	wsConnect *websocket.Conn
	inChan    chan []byte
	outChan   chan []byte
	closeChan chan byte

	mutex    sync.Mutex
	isClosed bool
}

func InitConnection(name string, wsConn *websocket.Conn) *Connection {
	conn := &Connection{
		name:      name,
		wsConnect: wsConn,
		inChan:    make(chan []byte, 1024),
		outChan:   make(chan []byte, 1024),
		closeChan: make(chan byte, 1),
	}

	go conn.readLoop()
	go conn.writeLoop()

	if c, ok := connections[name]; ok {
		_ = c.Close()
	}
	connections[name] = conn

	return conn
}

func (c *Connection) ReadMessage() (data []byte, err error) {
	select {
	case data = <-c.inChan:
	case <-c.closeChan:
		err = ErrWsIsClosed
	}
	return
}

func (c *Connection) WriteMessage(data []byte) (err error) {
	select {
	case c.outChan <- data:
	case <-c.closeChan:
		err = ErrWsIsClosed
	}
	return
}

func (c *Connection) Close() error {
	if err := c.wsConnect.Close(); err != nil {
		return err
	}
	c.mutex.Lock()
	if !c.isClosed {
		close(c.closeChan)
		c.isClosed = true
		delete(connections, c.name)
	}
	c.mutex.Unlock()
	return nil
}

func (c *Connection) readLoop() {
	var (
		data []byte
		err  error
	)
	defer func() { _ = c.Close() }()
	for {
		if _, data, err = c.wsConnect.ReadMessage(); err != nil {
			return
		}
		select {
		case c.inChan <- data:
		case <-c.closeChan:
			return
		}
	}
}

func (c *Connection) writeLoop() {
	var (
		data []byte
		err  error
	)
	defer func() { _ = c.Close() }()
	for {
		select {
		case data = <-c.outChan:
		case <-c.closeChan:
			return
		}
		if err = c.wsConnect.WriteMessage(websocket.TextMessage, data); err != nil {
			return
		}
	}
}

var g errgroup.Group

func (c *Connection) SendToOther(data []byte) error {
	for _, conn := range connections {
		if conn.name == c.name {
			continue
		}
		g.Go(func(connect *Connection) func() error {
			return func() error {
				return connect.WriteMessage(data)
			}
		}(conn))
	}
	return g.Wait()
}

func main() {
	r := gin.Default()
	r.GET("/ws/:name", func(c *gin.Context) {
		name := c.Param("name")
		upgrader := &websocket.Upgrader{CheckOrigin: func(r *http.Request) bool {
			return true
		}}
		conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			http.NotFound(c.Writer, c.Request)
			return
		}

		connect := InitConnection(name, conn)
		for {
			data, err := connect.ReadMessage()
			if err != nil {
				if err == ErrWsIsClosed {
					return
				}
				log.Printf("[error] %s %s\n", name, err)
				continue
			}
			log.Printf("[info] %s: %s\n", name, data)
			if err := connect.SendToOther(data); err != nil {
				log.Printf("[error] %s %s\n", name, err)
			}
		}
	})
	r.POST("/send/:name", func(c *gin.Context) {
		var m Message
		if err := c.ShouldBindJSON(&m); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
			return
		}
		name := c.Param("name")
		if conn, ok := connections[name]; ok {
			log.Println(ok)
			if err := conn.WriteMessage([]byte(m.Data)); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
		}
		c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("%s is not online", name)})
	})
	if err := r.Run(":8080"); err != nil {
		panic(err)
	}
}

type Message struct {
	Data string `json:"data"`
}

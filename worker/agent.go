package worker

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"log"
	"net"
	"sync"
	"time"

	rt "github.com/quantcast/g2/pkg/runtime"
)

// The Agent of job server.
type Agent struct {
	sync.Mutex
	conn      net.Conn
	rw        *bufio.ReadWriter
	worker    *Worker
	in        chan []byte
	net, Addr string
}

// Create the Agent of job server.
func newAgent(net, addr string, worker *Worker) (a *Agent, err error) {
	a = &Agent{
		net:    net,
		Addr:   addr,
		worker: worker,
		in:     make(chan []byte, rt.QueueSize),
	}
	return
}

func (a *Agent) Connect() (err error) {
	a.Lock()
	defer a.Unlock()
	a.conn, err = net.Dial(a.net, a.Addr)
	if err != nil {
		return
	}
	a.rw = bufio.NewReadWriter(bufio.NewReader(a.conn),
		bufio.NewWriter(a.conn))
	go a.work()
	return
}

func (a *Agent) work() {
	log.Println("Starting Agent Work For:", a.Addr)
	defer func() {
		if err := recover(); err != nil {
			a.worker.err(err.(error), a)
		}
	}()

	var inpack *inPack
	var l int
	var err error
	var data, leftdata []byte
	for {
		if !a.worker.isShuttingDown() {
			if data, err = a.read(); err != nil {
				log.Println("got read error:", a.Addr)
				if opErr, ok := err.(*net.OpError); ok {
					if opErr.Temporary() {
						log.Println("opErr.Temporary():", a.Addr)
						continue
					} else {
						a.disconnect_error(err)
						// else - we're probably dc'ing due to a Close()
						log.Println("disconnect_error:", a.Addr)
						break
					}

				} else if err == io.EOF {
					a.disconnect_error(err)
					log.Println("got EOF: disconnect_error:", a.Addr, "Work thread exiting...")
					break
				}
				a.worker.err(err, a)
				// If it is unexpected error and the connection wasn't
				// closed by Gearmand, the Agent should close the conection
				// and reconnect to job server.
				log.Println("Agent reconnecting to server:", a.Addr)
				a.Close()
				a.conn, err = net.Dial(a.net, a.Addr)
				if err != nil {
					a.worker.err(err, a)
					break
				}
				a.rw = bufio.NewReadWriter(bufio.NewReader(a.conn),
					bufio.NewWriter(a.conn))
			}
			if len(leftdata) > 0 { // some data left for processing
				data = append(leftdata, data...)
			}
			if len(data) < rt.MinPacketLength { // not enough data
				leftdata = data
				continue
			}
			for {
				if inpack, l, err = decodeInPack(data); err != nil {
					a.worker.err(err, a) // when supplying the agent ref we are allowing to recycle the connection to this gearman server
					leftdata = data
					break
				} else {
					leftdata = nil
					inpack.a = a
					a.worker.in <- inpack
					if len(data) == l {
						break
					}
					if len(data) > l {
						data = data[l:]
					}
				}
			}
		}
	}
}

func (a *Agent) disconnect_error(err error) {
	if a.conn != nil {
		err = &WorkerDisconnectError{
			err:   err,
			agent: a,
		}
		a.worker.err(err, a)
	}
}

func (a *Agent) Close() {
	a.Lock()
	defer a.Unlock()
	if a.conn != nil {
		a.conn.Close()
		a.conn = nil
	}
}

func (a *Agent) Grab() {
	a.Lock()
	defer a.Unlock()
	a.grab()
}

func (a *Agent) grab() {
	outpack := getOutPack()
	outpack.dataType = rt.PT_GrabJobUniq
	a.write(outpack)
}

func (a *Agent) PreSleep() {
	a.Lock()
	defer a.Unlock()
	outpack := getOutPack()
	outpack.dataType = rt.PT_PreSleep
	a.write(outpack)
}

func (a *Agent) Reconnect() error {
	a.Lock()
	defer a.Unlock()
	for num_tries := 0; ; num_tries++ {
		conn, err := net.Dial(a.net, a.Addr)
		if err != nil {
			log.Println("Could not redial:", a.Addr, "try#", num_tries)
			if num_tries >= 10 {
				return err
			} else {
				time.Sleep(500 * time.Millisecond)
				continue
			}
		}
		log.Println("Successfully redialed:", a.Addr, "try#", num_tries)
		a.conn = conn
		a.rw = bufio.NewReadWriter(bufio.NewReader(a.conn),
			bufio.NewWriter(a.conn))

		a.worker.reRegisterFuncsForAgent(a)
		a.grab()

		go a.work()
		break
	}

	return nil
}

// read length bytes from the socket
func (a *Agent) read() (data []byte, err error) {
	n := 0

	tmp := rt.NewBuffer(rt.BufferSize)
	var buf bytes.Buffer

	// read the header so we can get the length of the data
	if n, err = a.rw.Read(tmp); err != nil {
		return
	}
	dl := int(binary.BigEndian.Uint32(tmp[8:12]))

	// write what we read so far
	buf.Write(tmp[:n])

	// read until we receive all the data
	for buf.Len() < dl+rt.MinPacketLength {
		if n, err = a.rw.Read(tmp); err != nil {
			return buf.Bytes(), err
		}

		buf.Write(tmp[:n])
	}

	return buf.Bytes(), err
}

// Internal write the encoded job.
func (a *Agent) write(outpack *outPack) (err error) {
	var n int
	buf := outpack.Encode()
	for i := 0; i < len(buf); i += n {
		n, err = a.rw.Write(buf[i:])
		if err != nil {
			return err
		}
	}
	return a.rw.Flush()
}

// Write with lock
func (a *Agent) Write(outpack *outPack) (err error) {
	a.Lock()
	defer a.Unlock()
	return a.write(outpack)
}

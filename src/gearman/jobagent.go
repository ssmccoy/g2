// Copyright 2011 Xing Xing <mikespook@gmail.com> All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package gearman

import (
    "net"
    "os"
    //    "log"
)

// The agent of job server.
type jobAgent struct {
    conn     net.Conn
    worker   *Worker
    running  bool
    incoming chan []byte
}

// Create the agent of job server.
func newJobAgent(addr string, worker *Worker) (jobagent *jobAgent, err os.Error) {
    conn, err := net.Dial(TCP, addr)
    if err != nil {
        return nil, err
    }
    jobagent = &jobAgent{conn: conn, worker: worker, running: true, incoming: make(chan []byte, QUEUE_CAP)}
    return jobagent, err
}

// Internal read
func (agent *jobAgent) read() (data []byte, err os.Error) {
    if len(agent.incoming) > 0 {
        // incoming queue is not empty
        data = <-agent.incoming
    } else {
        for {
            buf := make([]byte, BUFFER_SIZE)
            var n int
            if n, err = agent.conn.Read(buf); err != nil {
                if err == os.EOF && n == 0 {
                    err = nil
                    return
                }
                return
            }
            data = append(data, buf[0:n]...)
            if n < BUFFER_SIZE {
                break
            }
        }
    }
    // split package
    start := 0
    tl := len(data)
    for i := 0; i < tl; i++ {
        if string(data[start:start+4]) == RES_STR {
            l := int(byteToUint32([4]byte{data[start+8], data[start+9], data[start+10], data[start+11]}))
            total := l + 12
            if total == tl {
                return
            } else {
                agent.incoming <- data[total:]
                data = data[:total]
                return
            }
        } else {
            start++
        }
    }
    err = os.NewError("Invalid data struct.")
    return
}

// Main loop.
func (agent *jobAgent) Work() {
    noop := true
    for agent.running {
        // got noop msg and incoming queue is zero, grab job
        if noop && len(agent.incoming) == 0 {
            agent.WriteJob(NewWorkerJob(REQ, GRAB_JOB, nil))
        }
        rel, err := agent.read()
        if err != nil {
            agent.worker.ErrQueue <- err
            continue
        }
        job, err := DecodeWorkerJob(rel)
        if err != nil {
            agent.worker.ErrQueue <- err
            continue
        } else {
            switch job.DataType {
            case NOOP:
                noop = true
            case NO_JOB:
                noop = false
                agent.WriteJob(NewWorkerJob(REQ, PRE_SLEEP, nil))
            case ECHO_RES, JOB_ASSIGN_UNIQ, JOB_ASSIGN:
                job.agent = agent
                agent.worker.incoming <- job
            }
        }
    }
    return
}

// Send a job to the job server.
func (agent *jobAgent) WriteJob(job *WorkerJob) (err os.Error) {
    return agent.write(job.Encode())
}

// Internal write the encoded job.
func (agent *jobAgent) write(buf []byte) (err os.Error) {
    var n int
    for i := 0; i < len(buf); i += n {
        n, err = agent.conn.Write(buf[i:])
        if err != nil {
            return err
        }
    }
    return
}

// Close.
func (agent *jobAgent) Close() (err os.Error) {
    agent.running = false
    close(agent.incoming)
    err = agent.conn.Close()
    return
}
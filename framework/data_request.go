package framework

import (
	"strings"
	"time"

	"github.com/taskgraph/taskgraph/pkg/etcdutil"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
)

func (f *framework) sendRequest(dr *dataRequest) {
	addr, err := etcdutil.GetAddress(f.etcdClient, f.name, dr.taskID)
	if err != nil {
		// TODO: We should handle network faults later by retrying
		f.log.Panicf("getAddress(%d) failed: %v", dr.taskID, err)
		return
	}

	if dr.retry {
		f.log.Printf("retry request data from task %d, addr %s", dr.taskID, addr)
	} else {
		f.log.Printf("request data from task %d, addr %s", dr.taskID, addr)
	}

	cc, err := grpc.Dial(addr, grpc.WithTimeout(heartbeatInterval))
	// we need to retry if some task failed and there is a temporary Get request failure.
	if err != nil {
		f.log.Printf("grpc.Dial from task %d (addr: %s) failed: %v", dr.taskID, addr, err)
		// Should retry for other errors.
		go f.retrySendRequest(dr)
		return
	}
	reply := f.task.CreateOutputMessage(dr.method)
	err := grpc.Invoke(ctx, dr.method, dr.input, reply, cc)
	if err != nil {
		if strings.Contains(err.Error(), "server epoch mismatch") {
			// It's out of date. Should abort this data request.
			return
		}
		f.log.Printf("grpc.Invoke from task %d (addr: %s), method: %s, failed: %v", dr.taskID, addr, dr.method, err)
		go f.retrySendRequest(dr)
		return
	}
	f.dataRespChan <- &dataResponse{
		epoch:    dr.epoch,
		taskID:   dr.taskID,
		linkType: dr.linkType,
		input:    dr.input,
		output:   reply,
	}
}

func (f *framework) retrySendRequest(dr *dataRequest) {
	// we try again after the previous task key expires and hopefully another task
	// gets up and running.
	time.Sleep(2 * heartbeatInterval)
	dr.retry = true
	f.dataReqtoSendChan <- dr
}

// This is used by the server side to handle data requests coming from remote.
func (f *framework) GetTaskData(taskID, epoch uint64, linkType, req string) ([]byte, error) {
	dataChan := make(chan []byte, 1)
	f.dataReqChan <- &dataRequest{
		taskID:   taskID,
		epoch:    epoch,
		linkType: linkType,
		req:      req,
		dataChan: dataChan,
	}

	select {
	case d, ok := <-dataChan:
		if !ok {
			// it assumes that only epoch mismatch will close the channel
			return nil, frameworkhttp.ErrReqEpochMismatch
		}
		return d, nil
	case <-f.httpStop:
		// If a node stopped running and there is remaining requests, we need to
		// respond error message back. It is used to let client routines stop blocking --
		// especially helpful in test cases.

		// This is used to drain the channel queue and get the rest notified.
		<-f.dataReqChan
		return nil, frameworkhttp.ErrServerClosed
	}
}

// Framework http server for data request.
// Each request will be in the format: "/datareq?taskID=XXX&req=XXX".
// "taskID" indicates the requesting task. "req" is the meta data for this request.
// On success, it should respond with requested data in http body.
func (f *framework) startHTTP() {
	f.log.Printf("serving grpc on %s\n", f.ln.Addr())

	err := f.task.CreateServer().Serve(f.ln)
	select {
	case <-f.httpStop:
		f.log.Printf("grpc stops serving")
	default:
		if err != nil {
			f.log.Fatalf("grpc.Serve returns error: %v\n", err)
		}
	}
}

// Close listener, stop HTTP server;
// Write error message back to under-serving responses.
func (f *framework) stopHTTP() {
	close(f.httpStop)
	f.ln.Close()
}

func (f *framework) sendResponse(dr *dataResponse) {
	dr.dataChan <- dr.data
}

func (f *framework) handleDataReq(dr *dataRequest) {
	dataReceiver := make(chan []byte, 1)

	b, err := f.task.Serve(dr.taskID, dr.linkType, dr.req)
	if err != nil {
		// TODO: We should handle network faults later by retrying
		f.log.Fatalf("ServeAsParent Error with id = %d, %v\n", dr.taskID, err)
	}
	dataReceiver <- b

	go func() {
		select {
		case data, ok := <-dataReceiver:
			if !ok || data == nil {
				return
			}
			// Getting the data from task could take a long time. We need to let
			// the response-to-send go through event loop to check epoch.
			f.dataRespToSendChan <- &dataResponse{
				taskID:   dr.taskID,
				epoch:    dr.epoch,
				req:      dr.req,
				data:     data,
				dataChan: dr.dataChan,
			}
		case <-f.epochPassed:
			// We can't leave a go-routine to wait for the data channel forever.
			// Users might forget to close the channel if they didn't want to do anything.
			// We can clean it up in releaseEpochResource() as the epoch moves on.
			// Because we won't be interested even though the data would come later.
		}
	}()
}

func (f *framework) handleDataResp(ctx context.Context, resp *dataResponse) {
	f.task.DataReady(ctx, resp.TaskID, resp.Method, resp.Req, resp.Data)
}

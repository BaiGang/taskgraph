package bwmf

import (
	"encoding/json"
	"log"
	"math"
	"math/rand"
	"os"

	"github.com/taskgraph/taskgraph"
	"github.com/taskgraph/taskgraph/op"
	"github.com/taskgraph/taskgraph/pkg/common"
)

/* TODO:
- SetEpoch 0...
- Topology should be pushed to user. FlagMeta, Meta/Data Ready..
- Test Data. (Simple, real)
- FileSystem (hdfs, s3).. run real data.
*/

/*
The block wise matrix factorization task is designed for carry out block wise matrix
factorization for a variety of criteria (loss function) and constraints (nonnegativity
for example).

The main idea behind the bwmf is following:
We will have K tasks that handle both row task and column task in alternation. Each task
will read two copies of the data: one row shard and one column shard of A. It either hosts
one shard of D and a full copy of T, or one shard of T and a full copy of D, depending on
the epoch of iteration. "A full copy" consists of computation results from itself and
all "children".
Topology: the topology is different from task to task. Each task will consider itself parent
and all others children.
*/

// bwmfData is used to carry indexes and values associated with each index. Index here
// can be row or column id, and value can be K wide, one for each topic;
type bwmfData struct {
	Index  int
	Values []float64
}

type Shard []*bwmfData

func newShard(m, n, startIndex int) *Shard {
	var res Shard
	res = make([]*bwmfData, m)
	for i := 0; i < m; i++ {
		res[i].Index = startIndex + i
		res[i].Values = make([]float64, n)
	}
	return &res
}

// TODO: load matrix data
//
// Via DFS interface or directly loaded by each task?
func loadShardedMatrix(path string) (*[]sparseVec, bool) {
	panic("")
}

func (shard *Shard) randomFillValue() {
	for i, _ := range *shard {
		for j, _ := range (*shard)[i].Values {
			// Do we need to normalized it?
			(*shard)[i].Values[j] = rand.Float64()
		}
	}
}

type sparseVec struct {
	Indexes []int
	Values  []float64
}

// bwmfTasks holds two shards of original matrices (row and column), one shard of D,
// and one shard of T. It works differently for odd and even epoch:
// During odd epoch, 1. it fetch all T from other slaves, and finding better value for
// local shard of D; 2. after it is done, it let every one knows. Vice versa for even epoch.
// Task 0 will monitor the progress and responsible for starting the work of new epoch.
type bwmfTask struct {
	framework  taskgraph.Framework
	epoch      uint64
	taskID     uint64
	logger     *log.Logger
	numOfTasks uint64

	// The original data.
	rowShard, columnShard *[]sparseVec

	dtReady       *common.CountdownLatch
	childrenReady map[uint64]bool

	// XXX(baigang): store matrix data directly as `Parameter`?
	shardedD, shardedT *Shard
	fullD, fullT       *Shard

	// shardedD is size of m*k, t shard is size of k*n (but still its layed-out as n*k)
	// fullD is size of M*k, fullT is size of k*N (dittu, layout N*k)
	m, n, k, M, N int

	// parameters for projected gradient methods
	sigma, alpha, beta, tol float64

	// intermidiate data
	shardedDParam, shardedTParam, dParam, tParam taskgraph_op.Parameter

	// objective function, parameters and minimizer to solve bwmf
	loss         *KLDivLoss
	optimizer    *taskgraph_op.ProjectedGradient
	stopCriteria taskgraph_op.StopCriteria
}

// These two function carry out actual optimization.
func (this *bwmfTask) updateDShard() {

	this.loss.W = this.tParam
	this.loss.m = this.m
	this.loss.n = this.N

	ok := this.optimizer.Minimize(this.loss, this.stopCriteria, this.shardedDParam)
	if !ok {
		// TODO report error
	}

	// TODO copy data from shardedDParam to shardedD
	// TODO signal finish of the task?
}

func (this *bwmfTask) updateTShard() {

	this.loss.W = this.dParam
	this.loss.m = this.n
	this.loss.n = this.M

	//
	ok := this.optimizer.Minimize(this.loss, this.stopCriteria, this.shardedTParam)
	if !ok {
		// TODO report error
	}

	// TODO copy data from shardedTParam to shardedT
}

// Initialization: We need to read row and column shards of A.
func (this *bwmfTask) readShardsFromDisk() bool {
	return false
}

// Read shardedD and shardedT from last checkpoint if any.
func (this *bwmfTask) readLastCheckpoint() bool {
	return false
}

// Compute shardedT/shardedD

// This is useful to bring the task up to speed from scratch or if it recovers.
func (this *bwmfTask) Init(taskID uint64, framework taskgraph.Framework) {
	this.taskID = taskID
	this.framework = framework
	this.logger = log.New(os.Stdout, "", log.Ldate|log.Ltime|log.Lshortfile)

	// Use some unique identifier to set Index "in the future".
	// We can use taskID now.
	// TODO: We need a DFS interface to load parts for each task. Or we load it directly from paths on local disk.
	var rowShardedOk, columnShardOk bool
	this.rowShard, rowShardedOk = loadShardedMatrix("TODO/some/path/for/rowShard")
	this.columnShard, columnShardOk = loadShardedMatrix("TODO/some/path/for/columnShard")

	if !rowShardedOk || !columnShardOk {
		this.logger.Printf("Failed load matrix data on task: %d", this.taskID)
		return
	}

	this.m = len(*this.rowShard)
	this.n = len(*this.columnShard)

	// TODO where to get M, N and k?
	//
	// Obviously, M, N and k should be specified via some arguments. Are we to use etcd?
	//
	// Anyhow, suppose now we set the values of M, N and k. Below are some dummy codes.
	this.M = this.m * int(this.numOfTasks)
	this.N = this.n * int(this.numOfTasks)
	this.k = 5 // dummy

	// TODO we also need start indices for sharded D and T.
	this.shardedD = newShard(this.m, this.k, this.m*int(this.taskID))
	this.shardedT = newShard(this.n, this.k, this.n*int(this.taskID))

	readCkpt := this.readLastCheckpoint()
	if !readCkpt {
		this.shardedD.randomFillValue()
		this.shardedT.randomFillValue()
	} else {
		// Read from checkpoint
		if !this.readLastCheckpoint() {
			this.logger.Printf("Failed loading checkpoint data.")
			return
		}
	}

	this.m = len(*this.shardedD)
	this.k = len((*this.shardedD)[0].Values)
	this.n = len(*this.shardedT)
	// to check? len(this.shardedT[0].Values) should be equal to this.k

	this.M = 10 // XXX TODO
	this.N = 10 // XXX TODO

	this.shardedDParam = taskgraph_op.NewVecParameter(this.m * this.k)
	this.shardedTParam = taskgraph_op.NewVecParameter(this.n * this.k)
	this.dParam = taskgraph_op.NewVecParameter(this.M * this.k)
	this.tParam = taskgraph_op.NewVecParameter(this.N * this.k)

	this.loss = &KLDivLoss{
		V:      nil, // will change at each epoch
		WH:     nil,
		W:      nil,
		m:      0,
		n:      0,
		k:      this.k, // to be specified via arguments
		smooth: 1e-3,   // to be specified via arguments
	}

	// TODO maxIter should be specified via arguments
	this.stopCriteria = taskgraph_op.MakeFixCountStopCriteria(10)
	proj_len := 0
	if this.m > this.n {
		proj_len = this.m * this.k
	} else {
		proj_len = this.n * this.k
	}
	projector := &taskgraph_op.Projection{
		upper_bound: taskgraph_op.NewAllTheSameParameter(1e20, proj_len),
		lower_bound: taskgraph_op.NewAllTheSameParameter(1e-9, proj_len),
	}
	this.optimizer = taskgraph_op.ProjectedGradient{
		projector: projector,
		beta:      this.beta,
		sigma:     this.sigma,
		alpha:     this.alpha,
	}

	// Now all has been initalized.

	// At initialization:
	// Task 0 will start the iterations.
}

func (this *bwmfTask) Exit() {}

func (this *bwmfTask) SetEpoch(ctx taskgraph.Context, epoch uint64) {
	this.logger.Printf("slave SetEpoch, task: %d, epoch: %d\n", this.taskID, epoch)
	this.epoch = epoch
	this.childrenReady = make(map[uint64]bool)
	this.dtReady = common.NewCountdownLatch(int(this.numOfTasks))

	// Afterwards:
	// We need to get all D/T from last epoch so that we can carry out local
	// update on T/D.
	// Even epochs: Fix D, calculate T;
	// Odd epochs: Fix T, calculate D;

	// XXX(baigang): This is for requesting sharded D/T from all tasks
	if this.epoch%2 == 0 {
		for index := uint64(0); index < this.numOfTasks; index++ {
			ctx.DataRequest(index, "getD")
		}
	} else {
		for index := uint64(0); index < this.numOfTasks; index++ {
			ctx.DataRequest(index, "getT")
		}
	}

	go func() {
		// Wait for all shards (either D or T, depending on the epoch) to be ready.
		this.dtReady.Await()
		// We can compute local shard result from A and D/T.
		this.localCompute()

		if this.epoch%2 == 0 {
			updateTShard()
		} else {
			updateDShard()
		}

		// Notify task 0 about the result.
		ctx.FlagMetaToParent("computed")
	}()
}

func (this *bwmfTask) ParentMetaReady(ctx taskgraph.Context, parentID uint64, meta string) {}

func (this *bwmfTask) ParentDataReady(ctx taskgraph.Context, parentID uint64, req string, resp []byte) {
}

func (this *bwmfTask) ChildMetaReady(ctx taskgraph.Context, childID uint64, meta string) {
	// Task zero should maintain the barrier for iterations.
	if meta == "computed" {
		this.childrenReady[childID] = true
	}
	if uint64(len(this.childrenReady)) < this.numOfTasks {
		return
	}
	// if we have all data, start next iteration.
	ctx.IncEpoch()
}

// Other nodes has served with their local shards.
func (this *bwmfTask) ChildDataReady(ctx taskgraph.Context, childID uint64, req string, resp []byte) {

	// NOTE(baigang): In SetEpoch() we request "getD" on even epochs and "getT" on odd epoches.
	// Also, we do not reserve a `Shard` of full copy of D/T, instead we directly use the `Parameter`.
	shard := new(Shard)
	if this.epoch%2 == 0 {
		json.Unmarshal(resp, shard)
		// copy shard to dParam
	} else {
		json.Unmarshal(resp, shard)
		// copy shard to tParam
	}

	this.dtReady.CountDown()
}

// get request of D/T shards from others. Serve with local shard.
func (this *bwmfTask) ServeAsParent(fromID uint64, req string, dataReceiver chan<- []byte) {
	var b []byte
	var err error

	go func() {
		if this.epoch%2 == 0 {
			b, err = json.Marshal(this.shardedD)
			if err != nil {
				this.logger.Fatalf("Slave can't encode shardedD error: %v\n", err)
			}
		} else {
			b, err = json.Marshal(this.shardedT)
			if err != nil {
				this.logger.Fatalf("Slave can't encode shardedT error: %v\n", err)
			}
		}
		dataReceiver <- b
	}()
}

func (this *bwmfTask) ServeAsChild(fromID uint64, req string, dataReceiver chan<- []byte) {}

type BWMFTaskBuilder struct {
}

func (tb BWMFTaskBuilder) GetTask(taskID uint64) taskgraph.Task {
	return &bwmfTask{}
}

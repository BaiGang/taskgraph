TaskGraph
=========

[![Build Status](https://drone.io/github.com/taskgraph/taskgraph/status.png)](https://drone.io/github.com/taskgraph/taskgraph/latest)

[![Build Status](https://travis-ci.org/taskgraph/taskgraph.svg?branch=master)](https://travis-ci.org/taskgraph/taskgraph)

TaskGraph is a framework for writing fault tolerent distributed applications. It
assumes that application consists of a network of tasks, which are inter-connected
based on certain topology (hence graph). TaskGraph assume for each task (logical
unit of work), there are one primary node, and zero or more backup nodes. TaskGraph
then help with two types of node failure: failure happens to nodes from different
task, failure happens to the nodes from the same task. Framework monitors the
task/node's health, and take care of restarting the failed tasks, and also pass
on a standard set of events (typed neighbors fail/restart, primary/backup switch)
to task implementation so that it can do application dependent recovery.


TaskGrpah supports an event driven pull model for communication. When one task
want to talk to some other task, it set a flag via framework, and framework will
notify recipient, which can decide whether or when to fetch data via Framework.
Framework will handle communication failures via automatic retry, reconnect to
new task node, etc. In another word, it provides exactly-once semantic on data
request.


An TaskGraph application usually has three layers. And application implementation
need to configure TaskGraph in driver layer and also implement Task/TaskBuilder/Topology
based on application logic.

1. In driver (main function), applicaiton need to configure the task graph. This
include setting up TaskBuilder, which specify what task need to run as each node.
One also need to specify the network topology which specify who connect to whom
at each iteration. Then FrameWork.Start is called so that every node will get into
event loop.

2. TaskGraph framework handles fault tolerency within the framework. It uses etcd
and/or kubernetes for this purpose. It should also handle the communication between
logic task so that it can hide the communication between master and hot standby.

3. Application need to implement Task interface, to specify how they should react
to parent/child dia/restart event to carry out the correct application logic. Note
that application developer need to implement TaskBuilder/Topology that suit their
need (implementaion of these three interface are wired together in the driver).

For an example of driver and task implementation, check `example/regression`.

Note, for now, the completion of TaskGraph is not defined explicitly. Instead, each
application will have their way of exit based on application dependent logic. As
an example, the above application can stop if task 0 stops. We have the hooks in
Framework so any node can potentially exit the entire TaskGraph. We also have hook
in Task so that task implementation get a change to save the work.
